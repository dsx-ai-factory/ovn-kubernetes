// SPDX-FileCopyrightText: Copyright The OVN-Kubernetes Contributors
// SPDX-License-Identifier: Apache-2.0

package hostport

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/knftables"

	"github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/controller"
	nodenft "github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/node/nftables"
	"github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/types"
	"github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/util"
)

const (
	preroutingChain = "hostports-prerouting"
	outputChain     = "hostports-output"
	mappingChain    = "hostports"
	// Run before service DNAT, which uses priority dstnat (-100).
	dnatPriority        knftables.BaseChainPriority = "-110"
	resyncInterval                                  = 5 * time.Minute
	initialSyncInterval                             = time.Second
	initialSyncTimeout                              = 2 * time.Minute
	nftTimeout                                      = 30 * time.Second
	reconcileKey                                    = "hostports"
	ipv4LoopbackCIDR                                = "127.0.0.0/8"
)

// Controller publishes hostPorts for local default-network pods. The informer is
// the source of truth, so missed deletes and restarts cannot retain stale rules.
// Only one worker replaces all owned chains in a single nftables transaction.
// TCP/SCTP connections keep their conntrack entries across rule replacement.
// The shared gateway management port supplies routing and SNAT, including hairpin.
type Controller struct {
	nodeName   string
	informer   cache.SharedIndexInformer
	nft        knftables.Interface
	reconciler controller.Reconciler
	// Pending conntrack cleanup survives failures, pod changes, and restarts via
	// the owned nftables sets, which are committed together with rule changes.
	applied            map[string]mapping
	pendingUDP         map[udpTuple]struct{}
	udpStateLoaded     bool
	readLegacyUDP      func(context.Context) ([]udpTuple, error)
	localAddresses     func() ([]net.Addr, error)
	deleteUDPConntrack func(context.Context, string, int32) error
}

// NewController expects the node-name pod index installed by the watch factory.
func NewController(nodeName string, informer cache.SharedIndexInformer, nft knftables.Interface) *Controller {
	c := &Controller{nodeName: nodeName, informer: informer, nft: nft}
	c.applied = map[string]mapping{}
	c.pendingUDP = map[udpTuple]struct{}{}
	c.localAddresses = net.InterfaceAddrs
	c.readLegacyUDP = func(ctx context.Context) ([]udpTuple, error) {
		return readLegacyUDPTuples(ctx, nodenft.OVNKubernetesNFTablesName)
	}
	c.deleteUDPConntrack = deleteUDPConntrack
	return c
}

// Start waits for the cache, retries initial reconciliation, and starts the worker.
// Shutdown leaves working rules in place for a daemon restart. Starting with the
// feature disabled calls Cleanup instead; node teardown removes the whole table.
func (c *Controller) Start(stopCh <-chan struct{}, wg *sync.WaitGroup) error {
	ctx := wait.ContextForChannel(stopCh)
	c.reconciler = controller.NewReconciler("hostports", &controller.ReconcilerConfig{
		Reconcile:   func(string) error { return c.sync(ctx) },
		Threadiness: 1,
		MaxAttempts: controller.InfiniteAttempts,
	})
	enqueue := func(obj interface{}) {
		if pod, ok := obj.(*corev1.Pod); ok && pod.Spec.NodeName == c.nodeName && hasHostPorts(pod) {
			c.reconciler.Reconcile(reconcileKey)
		}
	}
	handler, err := c.informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: enqueue,
		UpdateFunc: func(oldObj, newObj interface{}) {
			oldPod, newPod := oldObj.(*corev1.Pod), newObj.(*corev1.Pod)
			if oldPod.Spec.NodeName != newPod.Spec.NodeName ||
				oldPod.Spec.HostNetwork != newPod.Spec.HostNetwork ||
				oldPod.Status.Phase != newPod.Status.Phase ||
				oldPod.Annotations[types.OvnPodAnnotationName] != newPod.Annotations[types.OvnPodAnnotationName] ||
				!reflect.DeepEqual(oldPod.Spec.Containers, newPod.Spec.Containers) {
				enqueue(oldPod)
				enqueue(newPod)
			}
		},
		DeleteFunc: func(obj interface{}) {
			if tombstone, ok := obj.(cache.DeletedFinalStateUnknown); ok {
				obj = tombstone.Obj
			}
			enqueue(obj)
		},
	})
	if err != nil {
		controller.Stop(c.reconciler)
		return fmt.Errorf("register hostPort handler: %w", err)
	}
	removeHandler := func() {
		if err := c.informer.RemoveEventHandler(handler); err != nil {
			klog.Errorf("Remove hostPort handler: %v", err)
		}
	}
	if !util.WaitForInformerCacheSyncWithTimeout("hostports", stopCh, handler.HasSynced) {
		removeHandler()
		controller.Stop(c.reconciler)
		return fmt.Errorf("timed out waiting for hostPort pod cache")
	}
	if err := controller.StartWithInitialSync(func() error { return c.syncWithRetry(ctx) }, c.reconciler); err != nil {
		removeHandler()
		return fmt.Errorf("start hostPort controller: %w", err)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer removeHandler()
		defer controller.Stop(c.reconciler)
		ticker := time.NewTicker(resyncInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stopCh:
				return
			case <-ticker.C:
				// Repair rules removed externally even if no pod has changed.
				c.reconciler.Reconcile(reconcileKey)
			}
		}
	}()
	return nil
}

// Keep startup gated on successful reconciliation, but tolerate transient nftables
// and conntrack failures before the worker can take over retries.
func (c *Controller) syncWithRetry(ctx context.Context) error {
	var lastErr error
	err := wait.PollUntilContextTimeout(ctx, initialSyncInterval, initialSyncTimeout, true, func(ctx context.Context) (bool, error) {
		lastErr = c.sync(ctx)
		if lastErr != nil && ctx.Err() == nil {
			klog.Warningf("Retrying initial hostPort reconciliation: %v", lastErr)
		}
		return lastErr == nil, nil
	})
	if err != nil {
		return fmt.Errorf("initial hostPort reconciliation did not complete: %w", errors.Join(err, lastErr))
	}
	return nil
}

func hasHostPorts(pod *corev1.Pod) bool {
	// Match kubelet's PodSandbox port mappings, which use regular containers.
	for _, container := range pod.Spec.Containers {
		for _, port := range container.Ports {
			if port.HostPort > 0 {
				return true
			}
		}
	}
	return false
}

type mapping struct {
	family        string
	hostIP        string // empty means all local addresses of this family
	protocol      string
	hostPort      int32
	podIP         string
	containerPort int32
	owner         string
}

func (c *Controller) podMappings(pod *corev1.Pod) []mapping {
	if pod.Spec.NodeName != c.nodeName || pod.Spec.HostNetwork || !hasHostPorts(pod) ||
		pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
		return nil
	}
	// Status.PodIPs may refer to a primary UDN. Only publish addresses allocated
	// on the default network when it is the pod's primary network.
	annotation, err := util.UnmarshalPodAnnotation(pod.Annotations, types.DefaultNetworkName)
	if err != nil {
		if !util.IsAnnotationNotSetError(err) {
			klog.Warningf("Skipping hostPorts for pod %s/%s: %v", pod.Namespace, pod.Name, err)
		}
		return nil
	}
	if annotation.Role != "" && annotation.Role != types.NetworkRolePrimary {
		return nil
	}
	var mappings []mapping
	for _, container := range pod.Spec.Containers {
		for _, port := range container.Ports {
			if port.HostPort < 1 || port.HostPort > 65535 || port.ContainerPort < 1 || port.ContainerPort > 65535 {
				continue
			}
			protocol := port.Protocol
			if protocol == "" {
				protocol = corev1.ProtocolTCP
			}
			if protocol != corev1.ProtocolTCP && protocol != corev1.ProtocolUDP && protocol != corev1.ProtocolSCTP {
				continue
			}
			hostIP := net.ParseIP(port.HostIP)
			if port.HostIP != "" && hostIP == nil {
				continue
			}
			if hostIP != nil && hostIP.To4() == nil && hostIP.IsLoopback() {
				klog.Warningf("Skipping IPv6 localhost hostPort for pod %s/%s: routing ::1 to a pod is unsupported", pod.Namespace, pod.Name)
				continue
			}
			for _, podCIDR := range annotation.IPs {
				podIP := podCIDR.IP
				v4 := podIP.To4() != nil
				if hostIP != nil && (hostIP.To4() != nil) != v4 {
					continue
				}
				m := mapping{family: "ip6", protocol: strings.ToLower(string(protocol)), hostPort: port.HostPort,
					podIP: podIP.String(), containerPort: port.ContainerPort, owner: pod.Namespace + "/" + pod.Name}
				if v4 {
					m.family = "ip"
				}
				if hostIP != nil && !hostIP.IsUnspecified() {
					m.hostIP = hostIP.String()
				}
				mappings = append(mappings, m)
			}
		}
	}
	return mappings
}

func (c *Controller) sync(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !c.udpStateLoaded {
		if err := c.restoreUDPState(ctx); err != nil {
			return err
		}
	}
	// The shared informer may contain cluster-wide pods; use its node index
	// so reconciliation cost is bounded by pods on this node.
	objects, err := c.informer.GetIndexer().ByIndex(types.CacheIndexPodByNodeName, c.nodeName)
	if err != nil {
		return fmt.Errorf("list hostPort pods: %w", err)
	}
	pods := make([]*corev1.Pod, 0, len(objects))
	for _, obj := range objects {
		pods = append(pods, obj.(*corev1.Pod))
	}
	// Stable ordering makes conflicting manually bound pods deterministic.
	slices.SortFunc(pods, func(a, b *corev1.Pod) int {
		return strings.Compare(a.Namespace+"/"+a.Name, b.Namespace+"/"+b.Name)
	})
	var mappings []mapping
	for _, pod := range pods {
		mappings = append(mappings, c.podMappings(pod)...)
	}
	// A specific hostIP takes precedence over a wildcard mapping.
	slices.SortStableFunc(mappings, func(a, b mapping) int {
		if (a.hostIP == "") == (b.hostIP == "") {
			return 0
		}
		if a.hostIP == "" {
			return 1
		}
		return -1
	})

	tx := c.nft.NewTransaction()
	tx.Add(&knftables.Chain{Name: preroutingChain, Type: ptr.To(knftables.NATType), Hook: ptr.To(knftables.PreroutingHook), Priority: ptr.To(dnatPriority)})
	tx.Add(&knftables.Chain{Name: outputChain, Type: ptr.To(knftables.NATType), Hook: ptr.To(knftables.OutputHook), Priority: ptr.To(dnatPriority)})
	tx.Add(&knftables.Chain{Name: mappingChain})
	for _, chain := range []string{preroutingChain, outputChain, mappingChain} {
		tx.Flush(&knftables.Chain{Name: chain})
	}
	// Only locally generated traffic may use loopback hostPort mappings.
	tx.Add(&knftables.Rule{Chain: preroutingChain, Rule: "ip daddr 127.0.0.0/8 return"})
	tx.Add(&knftables.Rule{Chain: preroutingChain, Rule: "ip6 daddr ::1 return"})
	for _, chain := range []string{preroutingChain, outputChain} {
		tx.Add(&knftables.Rule{Chain: chain, Rule: "fib daddr type local jump " + mappingChain})
	}
	seen := map[string]string{}
	desired := map[string]mapping{}
	for _, m := range mappings {
		key := fmt.Sprintf("%s/%s/%s/%d", m.family, m.hostIP, m.protocol, m.hostPort)
		if owner, exists := seen[key]; exists {
			if owner != m.owner {
				klog.Warningf("Ignoring conflicting hostPort %s from pod %s; owned by %s", key, m.owner, owner)
			}
			continue
		}
		seen[key] = m.owner
		desired[key] = m
		match := "meta nfproto ipv4"
		if m.family == "ip6" {
			match = "meta nfproto ipv6 ip6 daddr != ::1"
		}
		if m.hostIP != "" {
			match = m.family + " daddr " + m.hostIP
		}
		destination := net.JoinHostPort(m.podIP, strconv.Itoa(int(m.containerPort)))
		tx.Add(&knftables.Rule{Chain: mappingChain,
			Rule: fmt.Sprintf("%s %s dport %d counter dnat %s to %s", match, m.protocol, m.hostPort, m.family, destination),
		})
	}
	pending := maps.Clone(c.pendingUDP)
	for key, old := range c.applied {
		if desired[key] != old && old.protocol == "udp" {
			pending[udpTuple{old.family, old.hostIP, old.hostPort}] = struct{}{}
		}
	}
	for key, current := range desired {
		if c.applied[key] != current && current.protocol == "udp" {
			pending[udpTuple{current.family, current.hostIP, current.hostPort}] = struct{}{}
		}
	}
	addUDPState(tx, desired, pending)
	if err := c.runNFTTransaction(ctx, tx); err != nil {
		return fmt.Errorf("reconcile hostPort nftables rules: %w", err)
	}
	c.applied = desired
	c.pendingUDP = pending
	if len(pending) == 0 {
		return nil
	}
	if err := c.clearUDPConntrack(ctx); err != nil {
		return err
	}
	// Forget removed tuples only after conntrack cleanup has succeeded. Active
	// tuples remain recoverable if their pods disappear while the daemon is down.
	tx = c.nft.NewTransaction()
	addUDPState(tx, desired, nil)
	if err := c.runNFTTransaction(ctx, tx); err != nil {
		return fmt.Errorf("finish UDP hostPort cleanup: %w", err)
	}
	return nil
}

// Each transaction gets its full timeout while remaining bounded by shutdown.
func (c *Controller) runNFTTransaction(ctx context.Context, tx *knftables.Transaction) error {
	ctx, cancel := context.WithTimeout(ctx, nftTimeout)
	defer cancel()
	return c.nft.Run(ctx, tx)
}

type udpTuple struct {
	family string
	hostIP string
	port   int32
}

func (c *Controller) clearUDPConntrack(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(c.pendingUDP) == 0 {
		return nil
	}
	addresses, err := c.localAddresses()
	if err != nil {
		return fmt.Errorf("list local hostPort addresses: %w", err)
	}
	var localIPs []net.IP
	for _, address := range addresses {
		ip, _, err := net.ParseCIDR(address.String())
		if err != nil {
			return fmt.Errorf("parse local hostPort address %q: %w", address, err)
		}
		localIPs = append(localIPs, ip)
	}
	for tuple := range c.pendingUDP {
		if err := ctx.Err(); err != nil {
			return err
		}
		var ips []string
		wildcardV4 := tuple.family == "ip" && tuple.hostIP == ""
		if wildcardV4 {
			// Wildcard OUTPUT rules also match unassigned addresses in 127/8.
			ips = append(ips, ipv4LoopbackCIDR)
		}
		for _, ip := range localIPs {
			if wildcardV4 && ip.IsLoopback() {
				continue
			}
			if (ip.To4() != nil) != (tuple.family == "ip") || (ip.To4() == nil && ip.IsLoopback()) {
				continue
			}
			if tuple.hostIP == "" || ip.Equal(net.ParseIP(tuple.hostIP)) {
				ips = append(ips, ip.String())
			}
		}
		// All of 127/8 is locally routed, including addresses not explicitly
		// assigned to lo. Never flush a non-local explicit destination.
		if ip := net.ParseIP(tuple.hostIP); ip != nil && ip.To4() != nil && ip.IsLoopback() && !slices.Contains(ips, tuple.hostIP) {
			ips = append(ips, tuple.hostIP)
		}
		for _, ip := range ips {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := c.deleteUDPConntrack(ctx, ip, tuple.port); err != nil {
				return fmt.Errorf("clear UDP hostPort conntrack for %s: %w", net.JoinHostPort(ip, strconv.Itoa(int(tuple.port))), err)
			}
		}
		delete(c.pendingUDP, tuple)
	}
	return nil
}

// Cleanup removes only this controller's chains and sets when the feature is disabled.
// Runtime/CNI hostPort rules and other OVN-Kubernetes chains are left intact.
func Cleanup(nft knftables.Interface) error {
	ctx, cancel := context.WithTimeout(context.Background(), nftTimeout)
	defer cancel()
	chains, err := nft.List(ctx, "chain")
	if err != nil {
		if knftables.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("list hostPort chains: %w", err)
	}
	sets, err := nft.List(ctx, "set")
	if err != nil {
		return fmt.Errorf("list hostPort sets: %w", err)
	}
	tx := nft.NewTransaction()
	for _, chain := range []string{preroutingChain, outputChain, mappingChain} {
		if slices.Contains(chains, chain) {
			tx.Flush(&knftables.Chain{Name: chain})
		}
	}
	for _, chain := range []string{preroutingChain, outputChain, mappingChain} {
		if slices.Contains(chains, chain) {
			tx.Delete(&knftables.Chain{Name: chain})
		}
	}
	for _, name := range []string{udpStateV4Set, udpStateV6Set} {
		if slices.Contains(sets, name) {
			tx.Delete(&knftables.Set{Name: name})
		}
	}
	if err := nft.Run(ctx, tx); err != nil {
		return fmt.Errorf("clean up hostPort chains and sets: %w", err)
	}
	return nil
}
