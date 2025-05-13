package networkprobe

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"reflect"
	"strconv"
	"sync"
	"syscall"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"

	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/config"
	networkprobe "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/crd/networkprobe/v1beta1"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/metrics"
	ovntypes "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/types"
)

func (c *Controller) handleNetworkProbe(probe *networkprobe.NetworkProbe, key string) (string, ovntypes.OvnK8sStatus, error) {
	desiredNetProbeState, err := c.NewNetworkProbe(probe)
	if err != nil {
		return "", networkProbeNotReadyReason, err
	}

	currentNetProbeState, exists := c.networkProbeCache[key]
	// if there is no network probe already
	if !exists {
		klog.Infof("Adding NetworkProbe %s/%s", desiredNetProbeState.namespace, desiredNetProbeState.name)
		// store it in cache
		c.networkProbeCache[key] = desiredNetProbeState
		// check whether probe is running on this node
		if !desiredNetProbeState.isProbeRunningOnThisNode {
			klog.Infof("Skipping networkprobe %s/%s on node %s as it doesn't match the node selector",
				desiredNetProbeState.namespace, desiredNetProbeState.name, c.nodeName)
			// update network probe status based on probe state
			return c.populateNetworkProbeStatusInfo(probe, desiredNetProbeState)
		}

		// start the probes
		c.startProbes(desiredNetProbeState)
		return c.populateNetworkProbeStatusInfo(probe, desiredNetProbeState)
	}

	klog.Infof("NetworkProbe %s/%s is already found in cache.. syncing it",
		desiredNetProbeState.namespace, desiredNetProbeState.name)

	// this case is for the nodes where they don't match the network probe node selector labels
	// and if there is a spec change, and we don't want to process the spec updates
	if !currentNetProbeState.isProbeRunningOnThisNode && !desiredNetProbeState.isProbeRunningOnThisNode {
		// update cache to have latest network probe state
		c.networkProbeCache[key] = desiredNetProbeState
		return c.populateNetworkProbeStatusInfo(probe, desiredNetProbeState)
	}

	// if the node label changes and matches network probe node selector label
	if desiredNetProbeState.isProbeRunningOnThisNode && !currentNetProbeState.isProbeRunningOnThisNode {
		c.startProbes(desiredNetProbeState)
		// update cache to have latest network probe state
		c.networkProbeCache[key] = desiredNetProbeState
		return c.populateNetworkProbeStatusInfo(probe, desiredNetProbeState)
	}

	// if the node label changes and doesn't match the network probe node selector.
	// stop the current probe
	if currentNetProbeState.isProbeRunningOnThisNode && !desiredNetProbeState.isProbeRunningOnThisNode {
		currentNetProbeState.stopProbes()
		// update cache to have latest network probe state
		c.networkProbeCache[key] = desiredNetProbeState
		c.eventRecorder.Eventf(probe, corev1.EventTypeNormal, "Stopped", "Stopped network probe %s/%s on node %s as node labels didn't match probe node selector",
			desiredNetProbeState.namespace, desiredNetProbeState.name, c.nodeName)
		return c.populateNetworkProbeStatusInfo(probe, desiredNetProbeState)
	}

	// if any of the dns or tcp or udp or http probe spec changes,
	// stop the existing probes and start new probes
	if !reflect.DeepEqual(desiredNetProbeState.networkProbeSpecInfo, currentNetProbeState.networkProbeSpecInfo) {
		currentNetProbeState.stopProbes()
		c.startProbes(desiredNetProbeState)
		// update cache to have latest network probe state
		c.networkProbeCache[key] = desiredNetProbeState
		return c.populateNetworkProbeStatusInfo(probe, desiredNetProbeState)
	}

	// handle network probe suspend state change
	if desiredNetProbeState.suspended {
		// update the status to suspended state
		currentNetProbeState.suspendProbe()
		return c.populateNetworkProbeStatusInfo(probe, desiredNetProbeState)
	}

	// handle network probe state change from suspend to resume
	if currentNetProbeState.suspended && !desiredNetProbeState.suspended {
		// update the status to resumed state
		currentNetProbeState.resumeProbe()
		return c.populateNetworkProbeStatusInfo(probe, desiredNetProbeState)
	}

	return c.populateNetworkProbeStatusInfo(probe, desiredNetProbeState)
}

func (c *Controller) populateNetworkProbeStatusInfo(probe *networkprobe.NetworkProbe, networkProbeState *NetworkProbeState) (string, ovntypes.OvnK8sStatus, error) {
	eventReason := "Running"
	eventMessage := fmt.Sprintf("Runnning network probe %s/%s on node %s", probe.Namespace, probe.Name, c.nodeName)
	probeStatusReason := networkProbeReadyReason
	var statusUpdateMessage = fmt.Sprintf("Running networkprobe on node %s", c.nodeName)

	if networkProbeState.suspended {
		eventReason = "Suspended"
		eventMessage = fmt.Sprintf("Suspended network probe %s/%s on node %s",
			probe.Namespace, probe.Name, c.nodeName)
		probeStatusReason = networkProbeSuspendedReason
		statusUpdateMessage = fmt.Sprintf("Suspended networkprobe on node %s", c.nodeName)
	}

	// if the network probe is not running on this node,
	// don't have any message for this node in network probe status.
	// send an empty message so that this node will be removed from the network probe status
	if !networkProbeState.isProbeRunningOnThisNode {
		statusUpdateMessage = ""
	} else {
		// send an event only if the probe is running on this node
		c.eventRecorder.Eventf(probe, corev1.EventTypeNormal, eventReason, eventMessage)
	}

	return statusUpdateMessage, ovntypes.OvnK8sStatus(probeStatusReason), nil
}

func (ps *NetworkProbeState) runProbe(probeInterval string, probeFunc func(context.Context)) {
	defer ps.wg.Done()

	sleepDur, err := time.ParseDuration(probeInterval)
	if err != nil {
		klog.Errorf("Error parsing interval for networkprobe %s/%s: %v", ps.name, ps.namespace, err)
		return
	}

	// Apply probe startup delay to avoid all of them starting simultaneously
	// Only for the first probe run, add a random delay between 0 and Interval-0.001s
	if sleepDur > time.Millisecond && config.EnableNetworkProbeDelay {
		// Generate a random value between 0 and interval with up to 3 decimal places
		maxDelayMs := int64(sleepDur.Milliseconds() - 1)

		delayMs := rand.Int63n(maxDelayMs)
		delay := time.Duration(delayMs) * time.Millisecond
		klog.V(5).Infof("Adding initial delay of %v for networkprobe %s/%s", delay, ps.namespace, ps.name)
		time.Sleep(delay)
	}

	for {
		select {
		case <-ps.stopCh:
			return
		default:
			isProbeSuspended := ps.getProbeState()
			if isProbeSuspended {
				time.Sleep(sleepDur)
				continue
			}
			// Create a new context for this probe run
			ctx, cancel := context.WithCancel(context.Background())
			ps.wg.Add(1)
			go func() {
				defer ps.wg.Done()
				probeFunc(ctx)
			}()

			time.Sleep(sleepDur)
			cancel()
		}
	}
}

func (ps *NetworkProbeState) getProbeState() bool {
	ps.probeMutex.Lock()
	defer ps.probeMutex.Unlock()
	return ps.suspended
}

func (ps *NetworkProbeState) stopProbes() {
	close(ps.stopCh)
	// wait for all the go routines to finish.
	ps.wg.Wait()
	klog.Infof("Stopped all the probes for networkprobe %s/%s", ps.namespace, ps.name)
}

func (ps *NetworkProbeState) suspendProbe() {
	ps.probeMutex.Lock()
	defer ps.probeMutex.Unlock()
	ps.suspended = true
}

func (ps *NetworkProbeState) resumeProbe() {
	ps.probeMutex.Lock()
	defer ps.probeMutex.Unlock()
	ps.suspended = false
}

func (c *Controller) NewNetworkProbe(networkProbe *networkprobe.NetworkProbe) (*NetworkProbeState, error) {
	ps := &NetworkProbeState{
		probeMutex: sync.Mutex{},
		wg:         &sync.WaitGroup{},
		stopCh:     make(chan struct{}),
		name:       networkProbe.Name,
		namespace:  networkProbe.Namespace,
		suspended:  networkProbe.Spec.Suspend,
		networkProbeSpecInfo: networkProbeSpecInfo{
			dnsProbes:  make([]*DnsProbe, 0),
			httpProbes: make([]*HttpProbe, 0),
			tcpProbes:  make([]*TcpProbe, 0),
			udpProbes:  make([]*UdpProbe, 0),
			interval:   string(networkProbe.Spec.Interval),
		},
	}

	var err error
	ps.nodeSelector, err = metav1.LabelSelectorAsSelector(&networkProbe.Spec.NodeSelector)
	if err != nil {
		return nil, fmt.Errorf("failed to parse node selector for networkprobe %s/%s", networkProbe.Namespace, networkProbe.Name)
	}

	dnsTargetInfoMap := make(map[string]bool, 0)
	for _, probe := range networkProbe.Spec.DNSProbes {
		dnsProbeObj := &DnsProbe{
			dnsLookupName: probe.LookupName,
			nameServer:    probe.NameServer,
			ipAddress:     probe.IPAddress,
			packetSpec: pktSpec{
				dscp: probe.PacketSpec.DSCP,
			},
		}
		// if dns probe interval is mentioned, then use that value.
		// otherwise, use the default network probe interval value
		dnsProbeInterval := string(probe.Interval)
		if dnsProbeInterval != "" {
			dnsProbeObj.interval = dnsProbeInterval
		} else {
			dnsProbeObj.interval = ps.interval
		}
		// no two DNS probes should have same lookupname and nameserver in same networkprobe spec
		dnsTarget := probe.LookupName + ":" + probe.NameServer
		if ok := dnsTargetInfoMap[dnsTarget]; ok {
			return nil, fmt.Errorf("one or more DNS probes has same dns lookupname and same nameserver within networkprobe spec")
		} else {
			dnsTargetInfoMap[dnsTarget] = true
		}
		ps.dnsProbes = append(ps.dnsProbes, dnsProbeObj)
	}

	httpTargetInfoMap := make(map[string]bool, 0)
	for _, probe := range networkProbe.Spec.HTTPProbes {
		httpProbeObj := &HttpProbe{
			url: probe.URL,
			packetSpec: pktSpec{
				dscp: probe.PacketSpec.DSCP,
			},
			method:  probe.Method,
			headers: probe.Headers,
		}

		// if http probe interval is mentioned, then use that value.
		// otherwise use the default network probe interval value
		httpProbeInterval := string(probe.Interval)
		if httpProbeInterval != "" {
			httpProbeObj.interval = httpProbeInterval
		} else {
			httpProbeObj.interval = ps.interval
		}

		if httpProbeObj.method != ModeGET {
			return nil, fmt.Errorf("probe mode in HTTP probes for URL %s is other than GET method for networkprobe %s/%s", probe.URL, networkProbe.Namespace, networkProbe.Name)
		}

		// no two http probes should have same URL and Method in same networkprobe spec
		httpTarget := probe.URL + ":" + probe.Method
		if ok := httpTargetInfoMap[httpTarget]; ok {
			return nil, fmt.Errorf("one or more http probes has same URL and same method in networkprobe spec")
		} else {
			httpTargetInfoMap[httpTarget] = true
		}

		// Handle TLSConfig
		// by default insecureSkip will be false
		insecureSkip := false
		httpProbeObj.tlsConfig = &TLSConfig{
			InsecureSkipVerify: &insecureSkip,
		}
		if probe.TLSConfig != nil {
			// Handle CA data
			if probe.TLSConfig.CACert != (networkprobe.SecretOrConfigMap{}) {
				switch {
				case probe.TLSConfig.CACert.Secret != nil:
					httpProbeObj.tlsConfig.CASecret = probe.TLSConfig.CACert.Secret
				case probe.TLSConfig.CACert.ConfigMap != nil:
					httpProbeObj.tlsConfig.CAConfigMap = probe.TLSConfig.CACert.ConfigMap
				}
			} else if probe.TLSConfig.InsecureSkipVerify != nil {
				httpProbeObj.tlsConfig.InsecureSkipVerify = probe.TLSConfig.InsecureSkipVerify
			}
		}
		ps.httpProbes = append(ps.httpProbes, httpProbeObj)
	}

	tcpTargetInfoMap := make(map[string]bool, 0)
	for _, probe := range networkProbe.Spec.TCPProbes {
		tcpProbeObj := &TcpProbe{
			host: probe.Host,
			port: probe.Port,
			packetSpec: pktSpec{
				dscp: probe.PacketSpec.DSCP,
			},
		}
		// if tcp probe interval is mentioned, then use that value
		// otherwise use the default network probe interval value
		tcpProbeInterval := string(probe.Interval)
		if tcpProbeInterval != "" {
			tcpProbeObj.interval = tcpProbeInterval
		} else {
			tcpProbeObj.interval = ps.interval
		}
		ps.tcpProbes = append(ps.tcpProbes, tcpProbeObj)
		// no two tcp probes should have same target and port within same networkprobe spec
		tcpTarget := probe.Host + ":" + string(*probe.Port)
		if ok := tcpTargetInfoMap[tcpTarget]; ok {
			return nil, fmt.Errorf("one or more tcp probes has same target and same port in networkprobe spec")
		} else {
			tcpTargetInfoMap[tcpTarget] = true
		}
	}

	udpTargetInfoMap := make(map[string]bool, 0)
	for _, probe := range networkProbe.Spec.UDPStreamProbes {
		udpProbeObj := &UdpProbe{
			host:           probe.Host,
			port:           probe.Port,
			packetCount:    int(*probe.PacketCount),
			packetInterval: string(probe.PacketInterval),
			packetSpec: pktSpec{
				dscp:        probe.PacketSpec.DSCP,
				payloadSize: parsePayloadSize(string(probe.PacketSpec.PayloadSize)),
			},
		}
		// if udp probe interval is mentioned, then use that value
		// otherwise use the default network probe interval value
		udpProbeInterval := string(probe.Interval)
		if udpProbeInterval != "" {
			udpProbeObj.interval = udpProbeInterval
		} else {
			udpProbeObj.interval = ps.interval
		}
		ps.udpProbes = append(ps.udpProbes, udpProbeObj)

		// no two udp probes should have same target and port within same networkprobe spec
		udpTarget := probe.Host + ":" + string(*probe.Port)
		if ok := udpTargetInfoMap[udpTarget]; ok {
			return nil, fmt.Errorf("one or more UDP probes has same target and same port in networkprobe spec")
		} else {
			udpTargetInfoMap[udpTarget] = true
		}
	}
	// check whether this probe should be running on this node
	ps.isProbeRunningOnThisNode, err = c.ShouldRunProbeOnNode(ps)
	if err != nil {
		return nil, err
	}
	return ps, nil
}

func (c *Controller) startProbes(ps *NetworkProbeState) {
	klog.Infof("Starting DNS, HTTP, TCP, UDP probes for networkprobe %s/%s", ps.namespace, ps.name)
	// start dns probes
	for _, dnsProbe := range ps.dnsProbes {
		ps.wg.Add(1)
		go ps.runProbe(dnsProbe.interval, func(ctx context.Context) {
			ps.handleDNSProbe(ctx, dnsProbe)
		})
	}

	// start udp probes
	for _, udpProbe := range ps.udpProbes {
		ps.wg.Add(1)
		go ps.runProbe(udpProbe.interval, func(ctx context.Context) {
			ps.handleUDPProbe(ctx, udpProbe)
		})
	}

	// start tcp probes
	for _, tcpProbe := range ps.tcpProbes {
		ps.wg.Add(1)
		go ps.runProbe(tcpProbe.interval, func(ctx context.Context) {
			ps.handleTCPProbe(ctx, tcpProbe)
		})
	}

	// start http probes
	for _, httpProbe := range ps.httpProbes {
		ps.wg.Add(1)
		go ps.runProbe(httpProbe.interval, func(ctx context.Context) {
			c.handleHttpProbe(ctx, httpProbe, ps.name, ps.namespace)
		})
	}
}

func (c *Controller) deleteNetworkProbe(key string) error {
	klog.Infof("Deleting networkProbe %s", key)

	if probe, ok := c.networkProbeCache[key]; ok {
		probe.stopProbes()
		delete(c.networkProbeCache, key)
	} else {
		// this case can occur when probe creation fails
		// 1. probe spec have more than one tcp probe with same target and port
		// 2. probe spec have more than one udp probe with same target and port
		// 3. probe spec have more than one http probe with same url and method
		// 4. probe spec have more than one DNS probe with same lookupname and nameserver
		klog.Errorf("Networkprobe %s not found in cache, either there must have been error during creation or it is deleted", key)
	}

	return nil
}

func (c *Controller) deleteAllNetworkProbes() {
	klog.Infof("Deleting all networkProbes")

	for probeInfo, probeState := range c.networkProbeCache {
		klog.V(5).Infof("Cancelling and deleting probe %s", probeInfo)
		probeState.stopProbes()
		delete(c.networkProbeCache, probeInfo)
	}
}

// handleDNSProbe sends a dns request to specified address and updates
// the corresponding dns metrics
func (nps *NetworkProbeState) handleDNSProbe(ctx context.Context, probe *DnsProbe) {
	var dnsLookup string
	if probe.dnsLookupName != "" {
		dnsLookup = probe.dnsLookupName
	} else {
		dnsLookup = probe.ipAddress
	}
	klog.V(6).Infof("Starting DNS probe for lookup of %s against nameserver %s in probe %s/%s",
		dnsLookup, probe.nameServer, nps.namespace, nps.name)

	var latency time.Duration
	var err error

	// Create a new context with timeout
	lookupCtx, cancel := context.WithTimeout(ctx, DNSLookupTimeOut)
	defer cancel()

	r := &net.Resolver{
		PreferGo: false,
		Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			d := net.Dialer{}
			conn, err := d.DialContext(ctx, "udp", probe.nameServer+":53")
			if err != nil {
				klog.Errorf("Failed to dial DNS Server %s for lookup of %s in probe %s/%s :(%v)",
					probe.nameServer, dnsLookup, nps.namespace, nps.name, err)
				return nil, err
			}

			if udpConn, ok := conn.(*net.UDPConn); ok {
				rawConn, err := udpConn.SyscallConn()
				if err != nil {
					klog.Errorf("Failed to get raw connection for udp socket for lookup of %s in probe %s/%s :(%v)",
						dnsLookup, nps.namespace, nps.name, err)
					return nil, err
				}

				err = rawConn.Control(func(fd uintptr) {
					sysErr := syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IP, syscall.IP_TOS, probe.packetSpec.dscp<<2)
					if sysErr != nil {
						klog.Errorf("Error setting DSCP for DNS probe for lookup of %s in network probe %s/%s :(%v)",
							dnsLookup, nps.namespace, nps.name, sysErr)
					}
				})
				if err != nil {
					return nil, err
				}
			}
			return conn, nil
		},
	}

	// Increment the attempts counter
	metrics.MetricDNSAttemptsTotal.WithLabelValues(nps.name, nps.namespace, probe.nameServer, dnsLookup).Inc()
	startTime := time.Now()

	if probe.ipAddress != "" {
		_, err = r.LookupAddr(lookupCtx, probe.ipAddress)
	} else {
		_, err = r.LookupHost(lookupCtx, probe.dnsLookupName)
	}
	latency = time.Since(startTime)
	if err != nil {
		klog.Errorf("Error resolving (DNS/Reverse DNS) lookup for %s against nameserver %s in networkprobe %s/%s: (%v)",
			dnsLookup, probe.nameServer, nps.namespace, nps.name, err)
		errorType := categorizeDNSError(err)
		metrics.MetricDNSErrorsTotal.WithLabelValues(nps.name, nps.namespace, probe.nameServer, dnsLookup, errorType).Inc()
		return
	}
	// Increment the completed counter
	metrics.MetricDNSCompletedTotal.WithLabelValues(nps.name, nps.namespace, probe.nameServer, dnsLookup).Inc()
	// Set the DNS latency
	metrics.MetricDNSResponseTime.WithLabelValues(nps.name, nps.namespace, probe.nameServer, dnsLookup).Set(latency.Seconds())
}

// categorizeDNSError categorizes the DNS error into a specific type (NX_DOMAIN, TIMEOUT, etc.)
func categorizeDNSError(err error) string {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return "TIMEOUT"
	}

	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		switch {
		case dnsErr.IsNotFound:
			return "NXDOMAIN"
		case dnsErr.IsTimeout:
			return "TIMEOUT"
		default:
			return "DNS_ERROR"
		}
	}
	return "Others"
}

// handleHttpProbe sends a http request to specified address and updates
// the corresponding http metrics
func (c *Controller) handleHttpProbe(ctx context.Context, probe *HttpProbe,
	networkProbeName, networkProbeNamespace string) {
	klog.V(6).Infof("Starting HTTP probe for url %s in networkprobe %s/%s", probe.url, networkProbeNamespace, networkProbeName)

	dialer := &net.Dialer{
		Control: func(_, _ string, c syscall.RawConn) error {
			return c.Control(func(fd uintptr) {
				// Setting the IP_TOS socket option
				// dscp value is shifted left by 2 bits to fit in the correct position of the TOS byte
				err := syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IP, syscall.IP_TOS, probe.packetSpec.dscp<<2)
				if err != nil {
					klog.Errorf("Error setting DSCP for HTTP probe URL:(%s) in network probe %s/%s :(%v)",
						probe.url, networkProbeNamespace, networkProbeName, err)
					return
				}
			})
		},
	}

	tlsConfig, err := c.configureTLS(probe, networkProbeName, networkProbeNamespace)
	if err != nil {
		klog.Errorf("Failed to configure TLS for http probe (%+v) in network probe %s/%s: (%v)",
			probe, networkProbeNamespace, networkProbeName, err)
		metrics.MetricHttpErrorsTotal.WithLabelValues(networkProbeName, networkProbeNamespace, probe.url, probe.method, "OTHERS").Inc()
		return
	}

	// Configure the HTTP transport
	client := &http.Client{
		Transport: &http.Transport{
			DialContext:     dialer.DialContext,
			TLSClientConfig: tlsConfig,
		},
	}

	// create and configure HTTP/HTTPS request
	req, err := http.NewRequestWithContext(ctx, "GET", probe.url, nil)
	if err != nil {
		klog.Errorf("Error creating a http/https request for url %s in network probe %s/%s: (%v)",
			probe.url, networkProbeNamespace, networkProbeName, err)
		return
	}

	// Add custom headers
	for key, value := range probe.headers {
		req.Header.Set(key, value)
	}

	metrics.MetricHttpAttemptsTotal.WithLabelValues(networkProbeName, networkProbeNamespace, probe.url, probe.method).Inc()
	startTime := time.Now()
	resp, err := client.Do(req)
	latency := time.Since(startTime)

	if err != nil {
		klog.Errorf("Error in HTTP probe for request %s and method %s in networkprobe %s/%s: (%v)",
			probe.url, probe.method, networkProbeNamespace, networkProbeName, err)
		_, errorType := categorizeHTTPError(resp, err)
		// update metrics for errors
		metrics.MetricHttpErrorsTotal.WithLabelValues(networkProbeName, networkProbeNamespace, probe.url, probe.method, errorType).Inc()
		return
	}
	// need to close the body to prevent client from reusing same connection
	defer resp.Body.Close()

	// update the HTTP metrics based on response status code
	if requestSuccess, errorType := categorizeHTTPError(resp, err); requestSuccess {
		metrics.MetricHttpCompletedTotal.WithLabelValues(networkProbeName, networkProbeNamespace, probe.url, probe.method).Inc()
		metrics.MetricHttpResponseTime.WithLabelValues(networkProbeName, networkProbeName, probe.url, probe.method).Set(latency.Seconds())
	} else {
		metrics.MetricHttpErrorsTotal.WithLabelValues(networkProbeName, networkProbeNamespace, probe.url, probe.method, errorType).Inc()
	}
}

// configureTLS sets up the TLS configuration for the HTTP probe.
func (c *Controller) configureTLS(probe *HttpProbe, networkProbeName, networkProbeNamespace string) (*tls.Config, error) {
	if *probe.tlsConfig.InsecureSkipVerify {
		return &tls.Config{InsecureSkipVerify: true}, nil
	}

	// if skipVerify is false and there is not ca configmap or secret, then the http request will be failed
	if !*probe.tlsConfig.InsecureSkipVerify && probe.tlsConfig.CAConfigMap == nil && probe.tlsConfig.CASecret == nil {
		return &tls.Config{InsecureSkipVerify: false}, nil
	}

	var caCertData []byte
	if probe.tlsConfig.CAConfigMap != nil {
		cmName := probe.tlsConfig.CAConfigMap.Name
		cmKey := probe.tlsConfig.CAConfigMap.Key

		cm, err := c.configMapLister.ConfigMaps(networkProbeNamespace).Get(cmName)
		if err != nil {
			return nil, fmt.Errorf("failed to get configmap %s/%s for http probe in networkprobe %s/%s :(%v)",
				networkProbeNamespace, probe.tlsConfig.CAConfigMap.Name, networkProbeNamespace, networkProbeName, err)
		}

		// Get the CA certificate data from the ConfigMap
		caCert, ok := cm.Data[cmKey]
		if !ok {
			return nil, fmt.Errorf("key %s not found in ConfigMap %s/%s", cmKey, networkProbeNamespace, cmName)
		}
		caCertData = []byte(caCert)
	} else {
		secretName := probe.tlsConfig.CASecret.Name
		secretKey := probe.tlsConfig.CASecret.Key

		secret, err := c.secretLister.Secrets(networkProbeNamespace).Get(secretName)
		if err != nil {
			return nil, fmt.Errorf("failed to get secret %s/%s for http probe in networkprobe %s/%s :(%v)",
				networkProbeNamespace, probe.tlsConfig.CASecret.Name, networkProbeNamespace, networkProbeName, err)
		}

		// Get the CA certificate data from the secret
		var ok bool
		if caCertData, ok = secret.Data[secretKey]; !ok {
			return nil, fmt.Errorf("key %s not found in secret %s/%s", secretKey, networkProbeNamespace, secretName)
		}
	}
	// Create a certificate pool and add the CA certificate
	caCertPool := x509.NewCertPool()
	if !caCertPool.AppendCertsFromPEM(caCertData) {
		return nil, fmt.Errorf("failed to append CA certificate for http probe in networkprobe %s/%s", networkProbeNamespace, networkProbeName)
	}

	tlsConfig := &tls.Config{
		RootCAs:            caCertPool,
		InsecureSkipVerify: false,
	}
	return tlsConfig, nil
}

// categorizeHTTPError categorizes the HTTP error into a specific type (TIMEOUT, SERVER_ERROR and CLIENT_ERROR)
// and returns if the HTTP request was a success or a failure based on response status code
func categorizeHTTPError(resp *http.Response, err error) (bool, string) {
	// context deadline errors
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return false, "TIMEOUT"
		}
		return false, "OTHERS"
	}

	// If there is a response, categorize based on status code
	if resp != nil {
		switch {
		case resp.StatusCode >= 200 && resp.StatusCode < 300:
			return true, ""
		case resp.StatusCode >= 500:
			return false, "SERVER_ERROR"
		case resp.StatusCode >= 400:
			return false, "CLIENT_ERROR"
		default:
			return false, "OTHERS"
		}
	}
	// Non-network or other unknown errors
	return false, "Others"
}

// handleTCPProbe initiates a tcp connection to mentioned tcp probe address and port
// and updates the corresponding TCP metrics.
func (nps *NetworkProbeState) handleTCPProbe(ctx context.Context, probe *TcpProbe) {
	dstPort := strconv.Itoa(int(*probe.port))
	addr := probe.host + ":" + dstPort

	klog.V(6).Infof("Starting TCP probe for address %s:%s in networkprobe %s/%s",
		probe.host, dstPort, nps.namespace, nps.name)

	// Create a new context with timeout
	lookupCtx, cancel := context.WithTimeout(ctx, TCPConnectionTimeout)
	defer cancel()

	dialer := &net.Dialer{
		Control: func(_, _ string, c syscall.RawConn) error {
			return c.Control(func(fd uintptr) {
				err := syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IP, syscall.IP_TOS, probe.packetSpec.dscp<<2)
				if err != nil {
					klog.Errorf("Probe %s in namespace %s: error setting DSCP for TCP connection to %s: %v", nps.name, nps.namespace, addr, err)
				}
			})
		},
	}

	metrics.MetricTCPAttemptsTotal.WithLabelValues(nps.name, nps.namespace, probe.host, dstPort).Inc()

	connectionStart := time.Now()
	// if the connection is not established within the TCPConnectionTimeout,
	// this would result in TIMEOUT
	conn, err := dialer.DialContext(lookupCtx, "tcp", addr)
	connectionEnd := time.Now()
	if err != nil {
		klog.Errorf("Error connecting to %s for a tcp probe in network probe %s/%s: %v", addr, nps.name, nps.namespace, err)
		errorType := categorizeTCPError(err)
		metrics.MetricTCPErrorsTotal.WithLabelValues(nps.name, nps.namespace, probe.host, dstPort, errorType).Inc()
		return
	}
	defer conn.Close()

	tcpConnectionTime := connectionEnd.Sub(connectionStart)
	metrics.MetricTCPRTTLatency.WithLabelValues(nps.name, nps.namespace, probe.host, dstPort).Set(tcpConnectionTime.Seconds())
	metrics.MetricTCPCompletedTotal.WithLabelValues(nps.name, nps.namespace, probe.host, dstPort).Inc()
}

// categorizeTCPError categorizes the TCP errors into a specific type
// (TIMEOUT, CONNECTION_REFUSED, and other non network error)
func categorizeTCPError(err error) string {
	// Check if the error is a net.Error and determine if it is a timeout.
	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return "TIMEOUT"
		}
	}

	// Check if the error is a *net.OpError and if it wraps a syscall error.
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		var errno syscall.Errno
		if errors.As(opErr.Err, &errno) && errors.Is(errno, syscall.ECONNREFUSED) {
			return "CONNECTION_REFUSED"
		}
	}
	// Non-network or other unknown errors
	return "OTHERS"
}

// handleUDPProbe tries to connect to mentioned udp probe address and port
// and updates the corresponding UDP metrics.
// It also measures the RTT latency and unidirectional latency for udp packets,
// if the other side is an echo server that time stamps the received packets
func (nps *NetworkProbeState) handleUDPProbe(ctx context.Context, probe *UdpProbe) {
	dstPort := strconv.Itoa(int(*probe.port))
	addr := probe.host + ":" + dstPort
	klog.V(6).Infof("Starting UDP probe for address %s:%s in networkprobe %s/%s",
		probe.host, dstPort, nps.namespace, nps.name)

	udpTargetIP, err := resolveHost(probe.host)
	if err != nil {
		klog.Errorf("Failed to resolve host %s :(%v)", probe.host, err)
		return
	}

	// Using syscall.Socket function instead of net.DialUDP method as we have to set the
	// DSCP value. syscall.Socketsystem call gives you more control over setting the socket options.
	// Also with net.DialUDP, if we set the DSCP parameters, conn.SetReadDeadline is not working
	// and so conn.ReadFromUDP function is blocked indefinitely

	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_DGRAM, 0)
	if err != nil {
		klog.Errorf("Error creating raw socket for UDP probe in network probe %s/%s: (%v)", nps.namespace, nps.name, err)
		return
	}
	defer syscall.Close(fd)

	// Set read timeout
	packetInterval, err := time.ParseDuration(probe.packetInterval)
	if err != nil {
		klog.Errorf("Failed to parse udp packet interval duration in network probe %s/%s: (%v)", nps.namespace, nps.name, err)
		return
	}

	overallTimeout := time.Duration(probe.packetCount)*packetInterval + DefaultReadTimeout
	readTimeoutVal := syscall.Timeval{
		Sec: int64(overallTimeout.Seconds()),
	}

	dscp_val := probe.packetSpec.dscp
	err = syscall.SetsockoptInt(fd, syscall.IPPROTO_IP, syscall.IP_TOS, dscp_val<<2)
	if err != nil {
		klog.Errorf("Error setting DSCP for UDP probe in network probe %s/%s: (%v)", nps.namespace, nps.name, err)
		return
	}

	err = syscall.SetsockoptTimeval(fd, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &readTimeoutVal)
	if err != nil {
		klog.Errorf("Error setting read timeout for UDP probe in network probe %s/%s: %v", nps.namespace, nps.name, err)
		return
	}

	metrics.MetricUDPAttemptsTotal.WithLabelValues(nps.name, nps.namespace, probe.host, dstPort).Inc()
	// currently, assuming it for IPV4. Need to support for IPV6 too
	destAddr := syscall.SockaddrInet4{
		Port: int(*probe.port),
	}
	copy(destAddr.Addr[:], net.ParseIP(udpTargetIP).To4())

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(3)

	sendChan := make(chan Packet, probe.packetCount)
	receiveChan := make(chan Packet, probe.packetCount)
	// currently sendUDPPackets and receiveUDPPackets are the go routines
	// that will send errors on this channel
	errorChan := make(chan error, 2)
	doneChan := make(chan struct{}, 1)

	// Start the goroutines
	go nps.sendUDPPackets(ctx, &wg, fd, &destAddr, probe, sendChan, errorChan)
	go nps.receiveUDPPackets(ctx, &wg, fd, probe, receiveChan, errorChan)
	go nps.processUDPPackets(ctx, &wg, sendChan, receiveChan, probe, doneChan)

	// Wait for completion of processUDPPackets
	// as that is the one which sets the metrics
	select {
	case err := <-errorChan:
		klog.Errorf("Error during UDP probing for target %s in network probe %s/%s: %v", addr, nps.namespace, nps.name, err)
		// Signal all goroutines to stop
		cancel()
	case <-doneChan:
	}
	// wait for completion of all goroutines
	wg.Wait()
}

// resolveHost resolves a given host, which can be either a hostname or an IPv4 address.
// It returns a valid IPv4 address as a string.
func resolveHost(host string) (string, error) {
	// Check if the host is already a valid IP address
	if ip := net.ParseIP(host); ip != nil {
		return ip.String(), nil
	}

	// If it's not an IP, resolve it as a hostname
	// net.LookupIP is using the nameservers in /etc/resolv.conf file
	ips, err := net.LookupIP(host)
	if err != nil {
		return "", fmt.Errorf("failed to resolve hostname: %v", err)
	}

	// Prefer IPv4 over IPv6 if available
	for _, ip := range ips {
		if ip.To4() != nil {
			return ip.String(), nil
		}
	}

	// If no IPv4 found, return error as we are supporting only ipv4 addresses for now
	return "", fmt.Errorf("no valid IPv4 address found for host")
}

func (nps *NetworkProbeState) sendUDPPackets(ctx context.Context, wg *sync.WaitGroup, fd int, addr *syscall.SockaddrInet4, probe *UdpProbe, sendChan chan<- Packet, errorChan chan<- error) {
	defer func() {
		wg.Done()
		close(sendChan)
	}()

	packetInterval, err := time.ParseDuration(probe.packetInterval)
	if err != nil {
		errorChan <- fmt.Errorf("error parsing packet interval: %v", err)
		return
	}

	payloadSize := probe.packetSpec.payloadSize
	dstPort := strconv.Itoa(int(*probe.port))
	ticker := time.NewTicker(packetInterval)

	for i := 0; i < probe.packetCount; i++ {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sendTime := time.Now()
			packet := Packet{
				Sequence:           uint32(i),
				SenderSideSendTime: sendTime.UnixNano(),
				Payload:            make([]byte, min(payloadSize, MaxPayloadSize)),
			}

			packetBytes, err := json.Marshal(packet)
			if err != nil {
				// Moving these messages to Infof from Errorf as we can vary the verbosity level and decide
				// them not to show up in logs.
				klog.Errorf("Error encoding packet for UDP probe in network probe %s/%s: %v", nps.name, nps.namespace, err)
				metrics.MetricUDPErrorsTotal.WithLabelValues(nps.name, nps.namespace, probe.host, dstPort, "ENCODING_ERROR").Inc()
				continue
			}

			err = syscall.Sendto(fd, packetBytes, 0, addr)
			if err != nil {
				errorChan <- fmt.Errorf("error sending packet: %v", err)
				return
			}
			sendChan <- packet
		}
	}
}

func (nps *NetworkProbeState) receiveUDPPackets(ctx context.Context, wg *sync.WaitGroup, fd int, probe *UdpProbe, receiveChan chan<- Packet, errorChan chan<- error) {
	defer func() {
		wg.Done()
		close(receiveChan)
	}()

	dstPort := strconv.Itoa(int(*probe.port))
	receivedCount := 0
	for receivedCount < probe.packetCount {
		select {
		case <-ctx.Done():
			return
		default:
			buffer := make([]byte, UDPBufferSize)
			n, _, err := syscall.Recvfrom(fd, buffer, 0)
			if err != nil {
				if errors.Is(err, syscall.EAGAIN) {
					metrics.MetricUDPErrorsTotal.WithLabelValues(nps.name, nps.namespace, probe.host, dstPort, "TIMEOUT_ERROR").Inc()
				} else {
					metrics.MetricUDPErrorsTotal.WithLabelValues(nps.name, nps.namespace, probe.host, dstPort, "READ_ERROR").Inc()
				}
				errorChan <- fmt.Errorf("timeout receiving packets: %v", err)
				return
			}

			var packet Packet
			err = json.Unmarshal(buffer[:n], &packet)
			if err != nil {
				klog.Errorf("Error decoding response packet for UDP probe in network probe %s/%s: %v", nps.namespace, nps.name, err)
				metrics.MetricUDPErrorsTotal.WithLabelValues(nps.name, nps.namespace, probe.host, "DECODING_ERROR").Inc()
				continue
			}

			packet.SenderSideReceiveTime = time.Now().UnixNano()
			receiveChan <- packet

			receivedCount++
		}
	}
}

func (np *NetworkProbeState) processUDPPackets(ctx context.Context, wg *sync.WaitGroup, sendChan <-chan Packet, receiveChan <-chan Packet, probe *UdpProbe, doneChan chan<- struct{}) {
	defer wg.Done()

	sentPackets := make(map[uint32]Packet)
	defer clear(sentPackets)
	receivedPackets := make(map[uint32]Packet)
	defer clear(receivedPackets)

	expectedPackets := probe.packetCount
	rttList := make([]int64, probe.packetCount)
	packetsLost := 0

	dstPort := strconv.Itoa(int(*probe.port))
	processPacket := func(seq uint32) {
		sent, sentOk := sentPackets[seq]
		received, receivedOk := receivedPackets[seq]

		if sentOk && receivedOk {
			// rttList for jitter calculation
			rttList[seq] = received.SenderSideReceiveTime - sent.SenderSideSendTime
			rtt := time.Duration(received.SenderSideReceiveTime - sent.SenderSideSendTime)
			uniDirectionalTXLatency := time.Duration(received.ReceiverSideReceiveTime - sent.SenderSideSendTime)
			uniDirectionalRXLatency := time.Duration(received.SenderSideReceiveTime - received.ReceiverSideSendTime)

			metrics.MetricUDPRTT.WithLabelValues(np.name, np.namespace, probe.host, dstPort).Set(rtt.Seconds())
			metrics.MetricUDPTXLatency.WithLabelValues(np.name, np.namespace, probe.host, dstPort).Set(uniDirectionalTXLatency.Seconds())
			metrics.MetricUDPRXLatency.WithLabelValues(np.name, np.namespace, probe.host, dstPort).Set(uniDirectionalRXLatency.Seconds())
		}
	}

	for {
		select {
		case <-ctx.Done():
			return
		case sentPacket, ok := <-sendChan:
			if !ok {
				sendChan = nil
			} else {
				sentPackets[sentPacket.Sequence] = sentPacket
			}
		case receivedPacket, ok := <-receiveChan:
			if !ok {
				receiveChan = nil
			} else {
				receivedPackets[receivedPacket.Sequence] = receivedPacket
			}
		}

		// wait till both send and receive channel is closed
		if sendChan == nil && receiveChan == nil {
			for seq := range sentPackets {
				processPacket(seq)
			}
			packetsLost = len(sentPackets) - len(receivedPackets)
			break
		}
	}

	// NOTE: if packets are not received, it might not be an error, it can just be packet loss or packet got timed out
	packetLossTotal := (float64(packetsLost) / float64(expectedPackets)) * 100
	metrics.MetricUDPPacketLossTotal.WithLabelValues(np.name, np.namespace, probe.host, dstPort).Set(packetLossTotal)
	metrics.MetricUDPCompletedTotal.WithLabelValues(np.name, np.namespace, probe.host, dstPort).Inc()
	jitterVal := time.Duration(calculateJitter(rttList))
	metrics.MetricUDPJitter.WithLabelValues(np.name, np.namespace, probe.host, dstPort).Set(jitterVal.Seconds())

	doneChan <- struct{}{}
}

func calculateJitter(rttList []int64) float64 {
	if len(rttList) < 2 {
		return 0
	}

	var totalJitter int64
	for i := 1; i < len(rttList); i++ {
		jitter := rttList[i] - rttList[i-1]
		if jitter < 0 {
			jitter = -jitter
		}
		totalJitter += jitter
	}

	avgJitter := float64(totalJitter) / float64(len(rttList)-1)
	return avgJitter
}
