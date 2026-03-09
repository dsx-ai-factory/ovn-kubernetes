package metrics

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/pprof"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	utilwait "k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"

	libovsdbclient "github.com/ovn-kubernetes/libovsdb/client"

	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util"
)

// metricsUpdaters holds registered metric update functions that are called
// on each /metrics scrape request. This allows components to register their
// metric update logic without spawning separate goroutines.
var (
	metricsUpdatersMu sync.RWMutex
	metricsUpdaters   []func()
)

// RegisterMetricsUpdater registers a function to be called on each /metrics scrape.
// This is useful for metrics that need to be updated on-demand rather than
// continuously in the background.
func RegisterMetricsUpdater(updater func()) {
	metricsUpdatersMu.Lock()
	defer metricsUpdatersMu.Unlock()
	metricsUpdaters = append(metricsUpdaters, updater)
}

// runRegisteredMetricsUpdaters executes all registered metric update functions.
func runRegisteredMetricsUpdaters() {
	metricsUpdatersMu.RLock()
	defer metricsUpdatersMu.RUnlock()
	for _, updater := range metricsUpdaters {
		updater()
	}
}

// MetricServerOptions defines the configuration options for the new MetricServer
type MetricServerOptions struct {
	// Server configuration
	BindAddress string

	// TLS configuration
	CertFile string
	KeyFile  string

	// Feature flags
	EnableOVSMetrics           bool
	EnableOVNDBMetrics         bool
	EnableOVNControllerMetrics bool
	EnableOVNNorthdMetrics     bool
	EnablePprof                bool

	// Enable OVS native metrics (downstream-specific)
	// When enabled, collects metrics via `ovs-appctl metrics/show`
	EnableOVSNativeMetrics bool
	// OnFatalError is called when an unrecoverable error occurs (e.g., failed to bind to address).
	// If set, it allows the caller to trigger a graceful shutdown.
	OnFatalError func()

	// Prometheus plumbing
	Registerer prometheus.Registerer

	// Kubernetes integration
	K8sClient   kubernetes.Interface
	K8sNodeName string
	OVSDBClient libovsdbclient.Client

	// Node name for OVS version info
	NodeName string

	dbIsClustered  bool
	dbFoundViaPath bool
}

// MetricServer represents the new unified metrics server
type MetricServer struct {
	// Configuration
	opts MetricServerOptions

	ovsDBClient libovsdbclient.Client
	kubeClient  kubernetes.Interface

	// Node name for OVS version info
	nodeName string

	ovsDbProperties []*util.OvsDbProperties

	// HTTP server
	server *http.Server
	mux    *http.ServeMux

	// Prometheus registry
	registerer prometheus.Registerer
}

// NewMetricServer creates a new MetricServer instance
func NewMetricServer(opts MetricServerOptions, ovsDBClient libovsdbclient.Client, kubeClient kubernetes.Interface) *MetricServer {
	registerer := opts.Registerer
	if registerer == nil {
		registerer = prometheus.NewRegistry()
	}

	server := &MetricServer{
		opts:        opts,
		ovsDBClient: ovsDBClient,
		nodeName:    opts.NodeName,
		registerer:  registerer,
		kubeClient:  kubeClient,
	}

	server.mux = http.NewServeMux()
	tg := prometheus.ToTransactionalGatherer(server.registerer.(prometheus.Gatherer))
	handlerOpts := promhttp.HandlerOpts{
		DisableCompression: true,
	}
	metricsHandler := promhttp.HandlerForTransactional(tg, handlerOpts)

	server.mux.Handle("/metrics", promhttp.InstrumentMetricHandler(
		server.registerer,
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Update metrics in the registry before emitting them.
			server.handleMetrics(r)
			metricsHandler.ServeHTTP(w, r)

			// Append OVS native metrics if enabled
			if server.opts.EnableOVSNativeMetrics {
				ovsMetrics, err := collectOvsNativeMetrics()
				if err != nil {
					klog.Errorf("Failed to collect ovs native metrics: %v", err)
					return
				}
				defer ovsMetrics.Close()
				if _, err := io.Copy(w, ovsMetrics); err != nil {
					klog.Errorf("Failed to write ovs native metrics: %v", err)
				}
			}
		}),
	))

	if opts.EnablePprof {
		server.mux.HandleFunc("/debug/pprof/", pprof.Index)
		server.mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
		server.mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
		server.mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
		server.mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

		// Allow changes to log level at runtime
		server.mux.HandleFunc("/debug/flags/v", stringFlagPutHandler(klogSetter))
	}

	return server
}

// registerMetrics registers the metrics to the OVN registry
func (s *MetricServer) registerMetrics() {
	if s.opts.EnableOVSMetrics {
		klog.Infof("MetricServer registers OVS metrics")
		registerOvsMetrics(s.registerer)
		RegisterOvsDBMetrics(s.registerer)
	}
	if s.opts.EnableOVNDBMetrics {
		klog.Infof("MetricServer registers OVN DB metrics")
		s.ovsDbProperties, s.opts.dbIsClustered, s.opts.dbFoundViaPath = RegisterOvnDBMetrics(s.registerer)
	}
	if s.opts.EnableOVNControllerMetrics {
		klog.Infof("MetricServer registers OVN Controller metrics")
		RegisterOvnControllerMetrics(s.ovsDBClient, s.registerer)
	}
	if s.opts.EnableOVNNorthdMetrics {
		klog.Infof("MetricServer registers OVN Northd metrics")
		RegisterOvnNorthdMetrics(s.registerer)
	}
}

func (s *MetricServer) EnableOVNNorthdMetrics() {
	s.opts.EnableOVNNorthdMetrics = true
	klog.Infof("MetricServer registers OVN Northd metrics")
	RegisterOvnNorthdMetrics(s.registerer)
}

func (s *MetricServer) EnableOVNDBMetrics() {
	s.opts.EnableOVNDBMetrics = true
	klog.Infof("MetricServer registers OVN DB metrics")
	s.ovsDbProperties, s.opts.dbIsClustered, s.opts.dbFoundViaPath = RegisterOvnDBMetrics(s.registerer)
}

// updateOvsMetrics updates the OVS metrics.
// When EnableOvsNativeMetrics is true, OVS native metrics are collected via
// `ovs-appctl metrics/show`, so only update the metrics not covered by native metrics.
func (s *MetricServer) updateOvsMetrics() {
	getOvsVersionInfo(s.nodeName, s.ovsDBClient)

	updateOvsDatapathMetrics(util.RunOvsVswitchdAppCtl)

	// Reset and update ovs bridge/interface metrics
	resetOvsBridgeMetrics()
	if err := updateOvsBridgeMetrics(s.ovsDBClient, util.RunOVSOfctl); err != nil {
		klog.Errorf("Updating ovs bridge metrics failed: %s", err.Error())
	}

	if err := setOvsMemoryMetrics(util.RunOvsVswitchdAppCtl); err != nil {
		klog.Errorf("Updating ovs memory metrics failed: %s", err.Error())
	}

	if err := setOvsHwOffloadMetrics(s.ovsDBClient); err != nil {
		klog.Errorf("Updating ovs hardware offload metrics failed: %s", err.Error())
	}

	updateOvsDbSizeMetric()
	coverageShowMetricsUpdate(ovsVswitchd)
}

// updateOvnControllerMetrics updates the OVN Controller metrics
func (s *MetricServer) updateOvnControllerMetrics() {
	if err := setOvnControllerConfigurationMetrics(s.ovsDBClient); err != nil {
		klog.Errorf("Setting ovn controller config metrics failed: %s", err.Error())
	}

	// Update SBDB connection status metric
	updateSBDBConnectionMetric(util.RunOVNControllerAppCtl)

	coverageShowMetricsUpdate(ovnController)
	stopwatchShowMetricsUpdate(ovnController)
}

// updateOvnNorthdMetrics updates the OVN Northd metrics
func (s *MetricServer) updateOvnNorthdMetrics() {
	coverageShowMetricsUpdate(ovnNorthd)
	stopwatchShowMetricsUpdate(ovnNorthd)
}

// updateOvnDBMetrics updates the OVN DB metrics
func (s *MetricServer) updateOvnDBMetrics() {
	if s.opts.dbIsClustered {
		resetOvnDbClusterMetrics()
	}
	if s.opts.dbFoundViaPath {
		resetOvnDbSizeMetric()
	}
	resetOvnDbMemoryMetrics()

	for _, dbProperty := range s.ovsDbProperties {
		if s.opts.dbIsClustered {
			ovnDBClusterStatusMetricsUpdater(dbProperty)
		}
		if s.opts.dbFoundViaPath {
			updateOvnDBSizeMetrics(dbProperty)
		}
		updateOvnDBMemoryMetrics(dbProperty)
	}
}

// handleMetrics handles the /metrics request
func (s *MetricServer) handleMetrics(r *http.Request) {
	klog.V(5).Infof("MetricServer starts to handle metrics request from %s", r.RemoteAddr)

	if s.opts.EnableOVSMetrics {
		s.updateOvsMetrics()
	}
	if s.opts.EnableOVNDBMetrics {
		s.updateOvnDBMetrics()
	}
	if s.opts.EnableOVNControllerMetrics {
		s.updateOvnControllerMetrics()
	}
	if s.opts.EnableOVNNorthdMetrics {
		s.updateOvnNorthdMetrics()
	}

	// Run all registered metric updaters
	runRegisteredMetricsUpdaters()
}

// Run runs the metrics server and blocks until graceful shutdown
func (s *MetricServer) Run(stopChan <-chan struct{}) {
	utilwait.Until(func() {
		s.server = &http.Server{
			Addr:    s.opts.BindAddress,
			Handler: s.mux,
		}
		listenAndServe := func() error { return s.server.ListenAndServe() }
		if s.opts.CertFile != "" && s.opts.KeyFile != "" {
			s.server.TLSConfig = &tls.Config{
				MinVersion: tls.VersionTLS12,
				GetCertificate: func(_ *tls.ClientHelloInfo) (*tls.Certificate, error) {
					cert, err := tls.LoadX509KeyPair(s.opts.CertFile, s.opts.KeyFile)
					if err != nil {
						return nil, fmt.Errorf("error generating x509 certs for metrics TLS endpoint: %v", err)
					}
					return &cert, nil
				},
			}
			listenAndServe = func() error { return s.server.ListenAndServeTLS("", "") }
		}

		errCh := make(chan error)
		go func() {
			klog.Infof("Metric Server starts to listen on %s", s.opts.BindAddress)
			errCh <- listenAndServe()
		}()

		select {
		case err := <-errCh:
			if !errors.Is(err, http.ErrServerClosed) {
				utilruntime.HandleError(fmt.Errorf("failed while running metrics server at address %q: %w", s.opts.BindAddress, err))
				if s.opts.OnFatalError != nil {
					s.opts.OnFatalError()
				}
			}
		case <-stopChan:
			klog.Infof("Stopping metrics server at address %q", s.opts.BindAddress)
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := s.server.Shutdown(shutdownCtx); err != nil {
				klog.Errorf("Error stopping metrics server at address %q: %v", s.opts.BindAddress, err)
			}
		}
	}, 5*time.Second, stopChan)
}
