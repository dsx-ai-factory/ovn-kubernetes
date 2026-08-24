// SPDX-FileCopyrightText: Copyright The OVN-Kubernetes Contributors
// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package metrics

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/pprof"
	"runtime/debug"
	"strconv"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	utilwait "k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"

	libovsdbclient "github.com/ovn-kubernetes/libovsdb/client"

	"github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/config"
	ovntls "github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/tls"
	"github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/util"
)

const (
	// metricsScrapeBudgetFallback bounds metric collection when Prometheus does not
	// advertise its scrape timeout. Kept below the typical 10s scrape_timeout so the
	// handler still returns in time to serve the last known values (up stays 1).
	metricsScrapeBudgetFallback = 8 * time.Second

	// metricsScrapeBudgetSlack is subtracted from the advertised scrape timeout to
	// leave promhttp room to serialize and write the response before Prometheus'
	// deadline; without it a cached write can lose the race and flap up to 0.
	metricsScrapeBudgetSlack = 500 * time.Millisecond
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

// scrapeBudget returns how long metric collection may run before the handler gives
// up and serves the last known values. It honors Prometheus'
// X-Prometheus-Scrape-Timeout-Seconds header (minus slack) when present, otherwise
// falls back to metricsScrapeBudgetFallback.
func scrapeBudget(r *http.Request) time.Duration {
	if v := r.Header.Get("X-Prometheus-Scrape-Timeout-Seconds"); v != "" {
		if secs, err := strconv.ParseFloat(v, 64); err == nil && secs > 0 {
			// Honor the advertised deadline minus slack, never the larger fallback.
			// If that leaves no room, use the full advertised value rather than skip
			// collection entirely.
			advertised := time.Duration(secs * float64(time.Second))
			if budget := advertised - metricsScrapeBudgetSlack; budget > 0 {
				return budget
			}
			return advertised
		}
	}
	// No header, or an unparseable/non-positive one: fall back.
	return metricsScrapeBudgetFallback
}

// MetricServerOptions defines the configuration options for the new MetricServer
type MetricServerOptions struct {
	// Server configuration
	BindAddress string

	// TLS configuration
	CertFile        string
	KeyFile         string
	ApplyTLSOptions ovntls.ApplyConfigOptions

	// Feature flags
	EnableOVSMetrics           bool
	EnableOVNDBMetrics         bool
	EnableOVNControllerMetrics bool
	EnableOVNNorthdMetrics     bool
	EnablePprof                bool

	// PprofPort is the loopback port for the pprof/debug listener. When pprof is
	// enabled it is a validated, explicit port (1-65535) supplied via
	// --metrics-pprof-port; see config.buildMetricsConfig.
	PprofPort int

	// Enable OVS native metrics (downstream-specific)
	// When enabled, collects metrics via `ovs-appctl metrics/show`
	EnableOVSNativeMetrics bool

	OVSDBClient libovsdbclient.Client

	// OnFatalError is called when an unrecoverable error occurs (e.g., failed to bind to address).
	// If set, it allows the caller to trigger a graceful shutdown.
	OnFatalError func()

	// Prometheus plumbing
	Registerer prometheus.Registerer

	// Node name for OVS version info
	NodeName string

	dbFoundViaPath bool
}

// MetricServer represents the new unified metrics server
type MetricServer struct {
	// Configuration
	opts MetricServerOptions

	ovsDbProperties []*util.OvsDbProperties

	// HTTP server
	server *http.Server
	mux    *http.ServeMux

	// pprofMux serves pprof and /debug/flags/v on a dedicated loopback-only
	// listener (see Run); it is nil unless pprof is enabled.
	pprofMux *http.ServeMux

	// Prometheus registry
	registerer prometheus.Registerer
}

// NewMetricServer creates a new MetricServer instance
func NewMetricServer(opts MetricServerOptions) *MetricServer {
	registerer := opts.Registerer
	if registerer == nil {
		registerer = prometheus.NewRegistry()
	}

	server := &MetricServer{
		opts:       opts,
		registerer: registerer,
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
		// pprof (heap/goroutine dumps, CPU profiling) and /debug/flags/v (runtime
		// klog verbosity changes) are unauthenticated and powerful. Keep them off
		// the routable metrics mux and serve them on a dedicated loopback listener
		// instead (see Run/startPprofServers).
		server.pprofMux = http.NewServeMux()
		server.pprofMux.HandleFunc("/debug/pprof/", pprof.Index)
		server.pprofMux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
		server.pprofMux.HandleFunc("/debug/pprof/profile", pprof.Profile)
		server.pprofMux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
		server.pprofMux.HandleFunc("/debug/pprof/trace", pprof.Trace)

		// Allow changes to log level at runtime
		server.pprofMux.HandleFunc("/debug/flags/v", stringFlagPutHandler(klogSetter))
	}

	return server
}

// startPprofServers starts loopback-only HTTP servers for the pprof and
// /debug/flags/v endpoints when pprof is enabled. Binding to loopback means the
// endpoints are reachable locally (kubectl exec/port-forward) but never from the
// routable metrics address. A listener is started per enabled cluster IP family
// (config.IPv4Mode -> 127.0.0.1, config.IPv6Mode -> ::1), so we don't perpetually
// retry binding a loopback family the cluster doesn't use (e.g. ::1 on a
// single-stack IPv4 cluster). Each listener is independent; the per-address
// goroutines are tracked in wg so Run can wait for their shutdown to complete
// before returning.
func (s *MetricServer) startPprofServers(stopChan <-chan struct{}, wg *sync.WaitGroup) {
	if !s.opts.EnablePprof {
		return
	}
	port := strconv.Itoa(s.opts.PprofPort)
	var addrs []string
	if config.IPv4Mode {
		addrs = append(addrs, net.JoinHostPort("127.0.0.1", port))
	}
	if config.IPv6Mode {
		addrs = append(addrs, net.JoinHostPort("::1", port))
	}
	if len(addrs) == 0 {
		// No IP family resolved from config (should not happen once config is
		// completed); fall back to IPv4 loopback so pprof is not silently dropped.
		klog.Warningf("No cluster IP family enabled; defaulting pprof listener to 127.0.0.1")
		addrs = append(addrs, net.JoinHostPort("127.0.0.1", port))
	}
	for _, addr := range addrs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.servePprof(addr, stopChan)
		}()
	}
}

// servePprof runs the pprof HTTP listener on addr, retrying every 5s until
// stopChan is closed, mirroring the retry loop the main metrics server uses in
// Run. This keeps pprof reachable even if the loopback port is momentarily
// unavailable (e.g. a stale holder across a restart). Each loopback address has
// its own independent loop, so one family stopping does not affect the other.
func (s *MetricServer) servePprof(addr string, stopChan <-chan struct{}) {
	utilwait.Until(func() {
		srv := &http.Server{
			Addr:              addr,
			Handler:           s.pprofMux,
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       10 * time.Second,
			IdleTimeout:       120 * time.Second,
			// WriteTimeout is intentionally left unset: /debug/pprof/profile and
			// /debug/pprof/trace stream for a client-specified duration, so a write
			// deadline would truncate those captures. ReadHeaderTimeout already
			// bounds slow-client (Slowloris) connection holding.
		}
		// Buffered so the ListenAndServe goroutine can always send its result and
		// exit, even when the select below has already taken the stopChan branch.
		errCh := make(chan error, 1)
		go func() {
			klog.Infof("Starting pprof server on %s", addr)
			errCh <- srv.ListenAndServe()
		}()
		select {
		case err := <-errCh:
			if !errors.Is(err, http.ErrServerClosed) {
				klog.Warningf("Pprof server on %s stopped, will retry: %v", addr, err)
			}
		case <-stopChan:
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := srv.Shutdown(shutdownCtx); err != nil {
				klog.Errorf("Error stopping pprof server on %s: %v", addr, err)
			}
		}
	}, 5*time.Second, stopChan)
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
		s.ovsDbProperties, s.opts.dbFoundViaPath = RegisterOvnDBMetrics(s.registerer)
	}
	if s.opts.EnableOVNControllerMetrics {
		klog.Infof("MetricServer registers OVN Controller metrics")
		RegisterOvnControllerMetrics(s.opts.OVSDBClient, s.registerer)
	}
	if s.opts.EnableOVNNorthdMetrics {
		klog.Infof("MetricServer registers OVN Northd metrics")
		RegisterOvnNorthdMetrics(s.registerer)
	}
}

// updateOvsMetrics updates the OVS metrics.
// When EnableOvsNativeMetrics is true, OVS native metrics are collected via
// `ovs-appctl metrics/show`, so only update the metrics not covered by native metrics.
func (s *MetricServer) updateOvsMetrics() {
	getOvsVersionInfo(s.opts.NodeName, s.opts.OVSDBClient)

	ovsVswitchdAppctl := func(args ...string) (string, string, error) {
		return util.RunOvsVswitchdAppCtlWithTimeout(metricsAppctlTimeout, args...)
	}
	updateOvsDatapathMetrics(ovsVswitchdAppctl)

	// Reset and update ovs bridge/interface metrics
	resetOvsBridgeMetrics()
	if err := updateOvsBridgeMetrics(s.opts.OVSDBClient, util.RunOVSOfctl); err != nil {
		klog.Errorf("Updating ovs bridge metrics failed: %s", err.Error())
	}
	if err := setOvsMemoryMetrics(ovsVswitchdAppctl); err != nil {
		klog.Errorf("Updating ovs memory metrics failed: %s", err.Error())
	}

	if err := setOvsHwOffloadMetrics(s.opts.OVSDBClient); err != nil {
		klog.Errorf("Updating ovs hardware offload metrics failed: %s", err.Error())
	}

	updateOvsDbSizeMetric()
	coverageShowMetricsUpdate(ovsVswitchd)
}

// updateOvnControllerMetrics updates the OVN Controller metrics
func (s *MetricServer) updateOvnControllerMetrics() {
	if err := setOvnControllerConfigurationMetrics(s.opts.OVSDBClient); err != nil {
		klog.Errorf("Setting ovn controller config metrics failed: %s", err.Error())
	}

	coverageShowMetricsUpdate(ovnController)
	stopwatchShowMetricsUpdate(ovnController)

	// Update SBDB connection status metric
	updateSBDBConnectionMetric(func(args ...string) (string, string, error) {
		return util.RunOVNControllerAppCtlWithTimeout(metricsAppctlTimeout, args...)
	})
}

// updateOvnNorthdMetrics updates the OVN Northd metrics
func (s *MetricServer) updateOvnNorthdMetrics() {
	coverageShowMetricsUpdate(ovnNorthd)
	stopwatchShowMetricsUpdate(ovnNorthd)
}

// updateOvnDBMetrics updates the OVN DB metrics
func (s *MetricServer) updateOvnDBMetrics() {
	if s.opts.dbFoundViaPath {
		resetOvnDbSizeMetric()
	}
	resetOvnDbMemoryMetrics()

	for _, dbProperty := range s.ovsDbProperties {
		if s.opts.dbFoundViaPath {
			updateOvnDBSizeMetrics(dbProperty)
		}
		updateOvnDBMemoryMetrics(dbProperty)
	}
}

// handleMetrics refreshes the OVS/OVN metrics before they are emitted. Collection
// execs ovs-appctl/ovn-appctl subprocesses (each bounded by metricsAppctlTimeout)
// in a separate goroutine under a scrape budget: if the daemons are slow and it
// overruns, the handler returns and promhttp serves the last known values, keeping
// the scrape under Prometheus' scrape_timeout (up stays 1). The goroutine keeps
// running to refresh the registry for the next scrape.
func (s *MetricServer) handleMetrics(r *http.Request) {
	klog.V(5).Infof("MetricServer starts to handle metrics request from %s", r.RemoteAddr)

	ctx, cancel := context.WithTimeout(r.Context(), scrapeBudget(r))
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		// Nothing recovers this detached goroutine, so recover here: a panic in a
		// metric path must only fail this scrape, not crash ovnkube-node.
		defer func() {
			if r := recover(); r != nil {
				klog.Errorf("MetricServer recovered from panic during metric collection: %v\n%s", r, debug.Stack())
			}
		}()

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

		// Run all registered metric updaters within the same scrape budget.
		runRegisteredMetricsUpdaters()
	}()

	select {
	case <-done:
	case <-ctx.Done():
		klog.Errorf("MetricServer metric collection exceeded the scrape budget for request from %s; "+
			"serving last known values (%v)", r.RemoteAddr, ctx.Err())
	}
}

// Run runs the metrics server and blocks until graceful shutdown
func (s *MetricServer) Run(stopChan <-chan struct{}) {
	var pprofWg sync.WaitGroup
	s.startPprofServers(stopChan, &pprofWg)
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

			if s.opts.ApplyTLSOptions != nil {
				s.opts.ApplyTLSOptions(s.server.TLSConfig)
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

	// stopChan is now closed; wait for the pprof listeners to finish shutting
	// down so a caller's WaitGroup doesn't complete before their Shutdown
	// returns.
	pprofWg.Wait()
}
