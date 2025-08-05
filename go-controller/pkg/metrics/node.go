package metrics

import (
	"runtime"
	"sync"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/config"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/types"
)

// MetricCNIRequestDuration is a prometheus metric that tracks the duration
// of CNI requests
var MetricCNIRequestDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
	Namespace: types.MetricOvnkubeNamespace,
	Subsystem: types.MetricOvnkubeSubsystemNode,
	Name:      "cni_request_duration_seconds",
	Help:      "The duration of CNI server requests.",
	Buckets:   prometheus.LinearBuckets(1, 1, 15)},
	//labels
	[]string{"command", "err"},
)

var MetricNodeReadyDuration = prometheus.NewGauge(prometheus.GaugeOpts{
	Namespace: types.MetricOvnkubeNamespace,
	Subsystem: types.MetricOvnkubeSubsystemNode,
	Name:      "ready_duration_seconds",
	Help:      "The duration for the node to get to ready state.",
})

var metricOvnNodePortEnabled = prometheus.NewGauge(prometheus.GaugeOpts{
	Namespace: types.MetricOvnkubeNamespace,
	Subsystem: types.MetricOvnkubeSubsystemNode,
	Name:      "nodeport_enabled",
	Help:      "Specifies if the node port is enabled on this node(1) or not(0).",
})

// metric to get the size of ovnkube.log file
var metricOvnKubeNodeLogFileSize = prometheus.NewGaugeVec(prometheus.GaugeOpts{
	Namespace: types.MetricOvnkubeNamespace,
	Subsystem: types.MetricOvnkubeSubsystemNode,
	Name:      "logfile_size",
	Help:      "The size of ovnkube logfile on the node."},
	[]string{
		"logfile_name",
	},
)

// DNS probe metrics
var MetricDNSResponseTime = prometheus.NewGaugeVec(prometheus.GaugeOpts{
	Namespace: types.MetricProbeNamespace,
	Subsystem: types.MetricDNSSubsystem,
	Name:      "response_time",
	Help:      "The response time of the DNS probe.",
},
	[]string{
		"name",
		"namespace",
		"name_server",
		"lookup_name",
	},
)

var MetricDNSAttemptsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
	Namespace: types.MetricProbeNamespace,
	Subsystem: types.MetricDNSSubsystem,
	Name:      "attempts_total",
	Help:      "The total number of DNS lookup attempts.",
},
	[]string{
		"name",
		"namespace",
		"name_server",
		"lookup_name",
	},
)

var MetricDNSCompletedTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
	Namespace: types.MetricProbeNamespace,
	Subsystem: types.MetricDNSSubsystem,
	Name:      "completed_total",
	Help:      "The total number of successful DNS lookups.",
},
	[]string{
		"name",
		"namespace",
		"name_server",
		"lookup_name",
	})

var MetricDNSErrorsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
	Namespace: types.MetricProbeNamespace,
	Subsystem: types.MetricDNSSubsystem,
	Name:      "errors_total",
	Help:      "The total number of DNS lookup errors.",
}, []string{
	"name",
	"namespace",
	"name_server",
	"lookup_name",
	"error_type",
})

// HTTP probe metrics
var MetricHttpResponseTime = prometheus.NewGaugeVec(prometheus.GaugeOpts{
	Namespace: types.MetricProbeNamespace,
	Subsystem: types.MetricHttpSubsystem,
	Name:      "response_time",
	Help:      "The response time of the HTTP probe.",
},
	[]string{
		"name",
		"namespace",
		"http_url",
		"method",
	},
)

var MetricHttpAttemptsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
	Namespace: types.MetricProbeNamespace,
	Subsystem: types.MetricHttpSubsystem,
	Name:      "attempts_total",
	Help:      "The total number of HTTP request attempts.",
}, []string{
	"name",
	"namespace",
	"http_url",
	"method",
},
)

var MetricHttpCompletedTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
	Namespace: types.MetricProbeNamespace,
	Subsystem: types.MetricHttpSubsystem,
	Name:      "completed_total",
	Help:      "The total number of successful HTTP requests.",
}, []string{
	"name",
	"namespace",
	"http_url",
	"method",
})

var MetricHttpErrorsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
	Namespace: types.MetricProbeNamespace,
	Subsystem: types.MetricHttpSubsystem,
	Name:      "errors_total",
	Help:      "The total number of HTTP request errors.",
}, []string{
	"name",
	"namespace",
	"http_url",
	"method",
	"error_type",
})

// TCP probe metrics
var MetricTCPRTTLatency = prometheus.NewGaugeVec(prometheus.GaugeOpts{
	Namespace: types.MetricProbeNamespace,
	Subsystem: types.MetricTCPSubsystem,
	Name:      "rtt_latency",
	Help:      "The response time of the TCP probe.",
},
	[]string{
		"name",
		"namespace",
		"host",
		"port",
	},
)

var MetricTCPAttemptsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
	Namespace: types.MetricProbeNamespace,
	Subsystem: types.MetricTCPSubsystem,
	Name:      "attempts_total",
	Help:      "The total number of TCP connection attempts.",
}, []string{
	"name",
	"namespace",
	"host",
	"port",
})

var MetricTCPCompletedTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
	Namespace: types.MetricProbeNamespace,
	Subsystem: types.MetricTCPSubsystem,
	Name:      "completed_total",
	Help:      "The total number of successful TCP connections.",
}, []string{
	"name",
	"namespace",
	"host",
	"port",
})

var MetricTCPErrorsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
	Namespace: types.MetricProbeNamespace,
	Subsystem: types.MetricTCPSubsystem,
	Name:      "errors_total",
	Help:      "The total number of TCP connection errors.",
}, []string{
	"name",
	"namespace",
	"host",
	"port",
	"error_type",
})

// UDP probe metrics
var MetricUDPTXLatency = prometheus.NewGaugeVec(prometheus.GaugeOpts{
	Namespace: types.MetricProbeNamespace,
	Subsystem: types.MetricUDPSubsystem,
	Name:      "tx_latency",
	Help:      "The latency of the UDP probe from sender to receiver side",
},
	[]string{
		"name",
		"namespace",
		"host",
		"port",
	},
)

var MetricUDPRXLatency = prometheus.NewGaugeVec(prometheus.GaugeOpts{
	Namespace: types.MetricProbeNamespace,
	Subsystem: types.MetricUDPSubsystem,
	Name:      "rx_latency",
	Help:      "The latency of the UDP probe from receiver to sender side",
},
	[]string{
		"name",
		"namespace",
		"host",
		"port",
	},
)

var MetricUDPRTT = prometheus.NewGaugeVec(prometheus.GaugeOpts{
	Namespace: types.MetricProbeNamespace,
	Subsystem: types.MetricUDPSubsystem,
	Name:      "rtt_latency",
	Help:      "The bi-directional latency of the UDP probe.",
},
	[]string{
		"name",
		"namespace",
		"host",
		"port",
	},
)

var MetricUDPJitter = prometheus.NewGaugeVec(prometheus.GaugeOpts{
	Namespace: types.MetricProbeNamespace,
	Subsystem: types.MetricUDPSubsystem,
	Name:      "jitter",
	Help:      "average variation in packet round trip times for UDP probe",
},
	[]string{
		"name",
		"namespace",
		"host",
		"port",
	},
)

var MetricUDPAttemptsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
	Namespace: types.MetricProbeNamespace,
	Subsystem: types.MetricUDPSubsystem,
	Name:      "attempts_total",
	Help:      "Total number of UDP probe attempts.",
}, []string{
	"name",
	"namespace",
	"host",
	"port",
})

var MetricUDPCompletedTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
	Namespace: types.MetricProbeNamespace,
	Subsystem: types.MetricUDPSubsystem,
	Name:      "completed_total",
	Help:      "Total number of completed UDP probes.",
}, []string{
	"name",
	"namespace",
	"host",
	"port",
})

var MetricUDPErrorsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
	Namespace: types.MetricProbeNamespace,
	Subsystem: types.MetricUDPSubsystem,
	Name:      "errors_total",
	Help:      "Total number of UDP probe errors.",
}, []string{
	"name",
	"namespace",
	"host",
	"port",
	"error_type"})

var MetricUDPPacketLossTotal = prometheus.NewGaugeVec(prometheus.GaugeOpts{
	Namespace: types.MetricProbeNamespace,
	Subsystem: types.MetricUDPSubsystem,
	Name:      "packet_loss_total",
	Help:      "Total number of Packet lost for UDP probe in percentage",
}, []string{
	"name",
	"namespace",
	"host",
	"port",
})

var registerNodeMetricsOnce sync.Once

func RegisterNodeMetrics(metricsScrapeInterval int, stopChan <-chan struct{}) {
	registerNodeMetricsOnce.Do(func() {
		// ovnkube-node metrics
		prometheus.MustRegister(MetricCNIRequestDuration)
		prometheus.MustRegister(MetricNodeReadyDuration)
		prometheus.MustRegister(metricOvnNodePortEnabled)
		prometheus.MustRegister(metricOvnKubeNodeLogFileSize)
		prometheus.MustRegister(MetricDNSResponseTime)
		prometheus.MustRegister(MetricDNSAttemptsTotal)
		prometheus.MustRegister(MetricDNSCompletedTotal)
		prometheus.MustRegister(MetricDNSErrorsTotal)
		prometheus.MustRegister(MetricHttpResponseTime)
		prometheus.MustRegister(MetricHttpAttemptsTotal)
		prometheus.MustRegister(MetricHttpCompletedTotal)
		prometheus.MustRegister(MetricHttpErrorsTotal)
		prometheus.MustRegister(MetricTCPRTTLatency)
		prometheus.MustRegister(MetricTCPAttemptsTotal)
		prometheus.MustRegister(MetricTCPCompletedTotal)
		prometheus.MustRegister(MetricTCPErrorsTotal)
		prometheus.MustRegister(MetricUDPTXLatency)
		prometheus.MustRegister(MetricUDPRXLatency)
		prometheus.MustRegister(MetricUDPRTT)
		prometheus.MustRegister(MetricUDPJitter)
		prometheus.MustRegister(MetricUDPAttemptsTotal)
		prometheus.MustRegister(MetricUDPCompletedTotal)
		prometheus.MustRegister(MetricUDPErrorsTotal)
		prometheus.MustRegister(MetricUDPPacketLossTotal)
		prometheus.MustRegister(prometheus.NewGaugeFunc(
			prometheus.GaugeOpts{
				Namespace: types.MetricOvnkubeNamespace,
				Subsystem: types.MetricOvnkubeSubsystemNode,
				Name:      "build_info",
				Help: "A metric with a constant '1' value labeled by version, revision, branch, " +
					"and go version from which ovnkube was built and when and who built it.",
				ConstLabels: prometheus.Labels{
					"version":    "0.0",
					"revision":   config.Commit,
					"branch":     config.Branch,
					"build_user": config.BuildUser,
					"build_date": config.BuildDate,
					"goversion":  runtime.Version(),
				},
			},
			func() float64 { return 1 },
		))
		registerWorkqueueMetrics(types.MetricOvnkubeNamespace, types.MetricOvnkubeSubsystemNode)
		if err := prometheus.Register(MetricResourceRetryFailuresCount); err != nil {
			if _, ok := err.(prometheus.AlreadyRegisteredError); !ok {
				panic(err)
			}
		}
		go ovnKubeLogFileSizeMetricsUpdater(metricOvnKubeNodeLogFileSize, metricsScrapeInterval, stopChan)
	})
}
