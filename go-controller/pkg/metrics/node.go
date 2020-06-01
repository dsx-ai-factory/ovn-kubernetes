package metrics

import (
	"fmt"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util"
	"github.com/prometheus/client_golang/prometheus"
	"k8s.io/klog"
)

const (
	ovnController = "ovn-controller"
)

// MetricCNIRequestDuration is a prometheus metric that tracks the duration
// of CNI requests
var MetricCNIRequestDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
	Namespace: MetricOvnkubeNamespace,
	Subsystem: MetricOvnkubeSubsystemNode,
	Name:      "cni_request_duration_seconds",
	Help:      "The duration of CNI server requests.",
	Buckets:   prometheus.ExponentialBuckets(.1, 2, 15)},
	//labels
	[]string{"command", "err"},
)

var MetricNodeReadyDuration = prometheus.NewGauge(prometheus.GaugeOpts{
	Namespace: MetricOvnkubeNamespace,
	Subsystem: MetricOvnkubeSubsystemNode,
	Name:      "ready_duration_seconds",
	Help:      "The duration for the node to get to ready state.",
})

var registerNodeMetricsOnce sync.Once

type metricDetails struct {
	help   string
	metric prometheus.Gauge
}

var ovnControllerCoverageShowCountersMap = map[string]*metricDetails{
	"lflow_run": {
		help: "Number of times ovn-controller has translated " +
			"the Logical_Flow table in the OVN " +
			"SB database into OpenFlow flows.",
	},
	"rconn_sent": {
		help: "Specifies the number of messages " +
			"that have been sent to the underlying virtual " +
			"connection (unix, tcp, or ssl) to OpenFlow devices.",
	},
	"rconn_queued": {
		help: "Specifies the number of messages that have been " +
			"queued because it couldn’t be sent using the " +
			"underlying virtual connection to OpenFlow devices.",
	},
	"rconn_discarded": {
		help: "Specifies the number of messages that " +
			"have been dropped because the send queue " +
			"had to be flushed because of reconnection.",
	},
	"rconn_overflow": {
		help: "Specifies the number of messages that have " +
			"been dropped because of the queue overflow.",
	},
	"vconn_open": {
		help: "Specifies the number of attempts to connect " +
			"to an OpenFlow Device.",
	},
	"vconn_sent": {
		help: "Specifies the number of messages sent " +
			"to the OpenFlow Device.",
	},
	"vconn_received": {
		help: "Specifies the number of messages received " +
			"from the OpenFlow Device.",
	},
	"stream_open": {
		help: "Specifies the number of attempts to connect " +
			"to a remote peer (active connection).",
	},
	"txn_success": {
		help: "Specifies the number of times the OVSDB " +
			"transaction has successfully completed.",
	},
	"txn_error": {
		help: "Specifies the number of times the OVSDB " +
			"transaction has errored out.",
	},
	"txn_uncommitted": {
		help: "Specifies the number of times the OVSDB " +
			"transaction were uncommitted.",
	},
	"txn_unchanged": {
		help: "Specifies the number of times the OVSDB transaction " +
			"resulted in no change to the database.",
	},
	"txn_incomplete": {
		help: "Specifies the number of times the OVSDB transaction " +
			"did not complete and the client had to re-try.",
	},
	"txn_aborted": {
		help: "Specifies the number of times the OVSDB " +
			" transaction has been aborted.",
	},
	"txn_try_again": {
		help: "Specifies the number of times the OVSDB " +
			"transaction failed and the client had to re-try.",
	},
	"netlink_sent": {
		help: "Number of netlink message sent to the kernel.",
	},
	"netlink_received": {
		help: "Number of netlink messages received by the kernel.",
	},
	"netlink_recv_jumbo": {
		help: "Number of netlink messages that were received from" +
			"the kernel were more than the allocated buffer.",
	},
	"netlink_overflow": {
		help: "Netlink messages dropped by the daemon due " +
			"to buffer overflow.",
	},
	"packet_in": {
		help: "Specifies the number of times ovn-controller has " +
			"handled the packet-ins from ovs-vswitchd.",
	},
}

var targetMetricMap = map[string]map[string]*metricDetails{
	ovnController: ovnControllerCoverageShowCountersMap,
	ovnNorthd:     ovnNorthdCoverageShowCountersMap,
	ovsVswitchd:   ovsVswitchdCoverageShowCountersMap,
}

// registerCoverageShowCounters registers coverage/show counter metrics for
// various targets(ovn-northd, ovn-controller) with prometheus
func registerCoverageShowCounters(target string, metricNamespace string, metricSubsystem string) {
	coverageCountersMetricsMap := targetMetricMap[target]
	for counterName, counterMetricInfo := range coverageCountersMetricsMap {
		counterMetricInfo.metric = prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: metricNamespace,
			Subsystem: metricSubsystem,
			Name:      counterName,
			Help:      counterMetricInfo.help,
		})
		prometheus.MustRegister(counterMetricInfo.metric)
	}
}

// CoverageShowCounters obtains the coverage counter
// values of various events for the specified target .
func coverageShowCounters(target string) (coverageCounters map[string]string, err error) {
	var stdout, stderr string

	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("recovering from a panic while parsing the coverage/show output "+
				"for %s: %v", target, r)
		}
	}()

	if target == ovnController {
		stdout, stderr, err = util.RunOVNControllerAppCtl("coverage/show")
	} else if target == ovnNorthd {
		stdout, stderr, err = util.RunOVNNorthAppCtl("coverage/show")
	} else if target == ovsVswitchd {
		stdout, stderr, err = util.RunOvsVswitchdAppCtl("coverage/show")
	} else {
		err = fmt.Errorf("target is not ovn-controller or " +
			"ovn-northd or ovs-vswitchd")
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get coverage/show output for %s "+
			"stderr(%s): (%v)", target, stderr, err)
	}

	coverageCounters = make(map[string]string)
	output := strings.Split(stdout, "\n")
	for _, kvPair := range output {
		if strings.Contains(kvPair, "total:") {
			fields := strings.Fields(kvPair)
			coverageCounters[fields[0]] = fields[len(fields)-1]
		}
	}
	return coverageCounters, nil
}

func RegisterNodeMetrics() {
	registerNodeMetricsOnce.Do(func() {
		prometheus.MustRegister(MetricCNIRequestDuration)
		prometheus.MustRegister(MetricNodeReadyDuration)
		prometheus.MustRegister(prometheus.NewCounterFunc(
			prometheus.CounterOpts{
				Namespace: MetricOvnkubeNamespace,
				Subsystem: MetricOvnkubeSubsystemNode,
				Name:      "integration_bridge_openflow_total",
				Help:      "The total number of OpenFlow flows in the integration bridge.",
			}, func() float64 {
				stdout, stderr, err := util.RunOVSOfctl("-t", "5", "dump-aggregate", "br-int")
				if err != nil {
					klog.Errorf("Failed to get flow count for br-int, stderr(%s): (%v)",
						stderr, err)
					return 0
				}
				for _, kvPair := range strings.Fields(stdout) {
					if strings.HasPrefix(kvPair, "flow_count=") {
						value := strings.Split(kvPair, "=")[1]
						return parseMetricToFloat("integration_bridge_openflow_total", value)
					}
				}
				return 0
			}))
		prometheus.MustRegister(prometheus.NewGaugeFunc(
			prometheus.GaugeOpts{
				Namespace: MetricOvnkubeNamespace,
				Subsystem: MetricOvnkubeSubsystemNode,
				Name:      "build_info",
				Help: "A metric with a constant '1' value labeled by version, revision, branch, " +
					"and go version from which ovnkube was built and when and who built it.",
				ConstLabels: prometheus.Labels{
					"version":    "0.0",
					"revision":   Commit,
					"branch":     Branch,
					"build_user": BuildUser,
					"build_date": BuildDate,
					"goversion":  runtime.Version(),
				},
			},
			func() float64 { return 1 },
		))
		prometheus.MustRegister(prometheus.NewGaugeFunc(
			prometheus.GaugeOpts{
				Namespace: MetricOvnNamespace,
				Subsystem: MetricOvnSubsystemController,
				Name:      "remote_probe_interval",
				Help: "The maximum number of milliseconds of idle time on connection to " +
					"the OVN SB DB before sending an inactivity probe message.",
			}, func() float64 {
				stdout, stderr, err := util.RunOVSVsctl("get", "Open_vSwitch", ".",
					"external_ids:ovn-remote-probe-interval")
				if err != nil {
					klog.Errorf("Failed to get ovn-remote-probe-interval stderr(%s): (%v)",
						stderr, err)
					return 0
				}
				return parseMetricToFloat("ovn-remote-probe-interval", stdout)
			}))
		prometheus.MustRegister(prometheus.NewGaugeFunc(
			prometheus.GaugeOpts{
				Namespace: MetricOvnNamespace,
				Subsystem: MetricOvnSubsystemController,
				Name:      "openflow_probe_interval",
				Help: "The maximum number of milliseconds of idle time on OpenFlow connection " +
					"to the OVS bridge before sending an inactivity probe message.",
			}, func() float64 {
				stdout, stderr, err := util.RunOVSVsctl("get", "Open_vSwitch", ".",
					"external_ids:ovn-openflow-probe-interval")
				if err != nil {
					klog.Errorf("Failed to get ovn-openflow-probe-interval stderr(%s): (%v)",
						stderr, err)
					return 0
				}
				return parseMetricToFloat("ovn-openflow-probe-interval", stdout)
			}))
		prometheus.MustRegister(prometheus.NewGaugeFunc(
			prometheus.GaugeOpts{
				Namespace: MetricOvnNamespace,
				Subsystem: MetricOvnSubsystemController,
				Name:      "monitor_all",
				Help: "Specifies if ovn-controller should monitor all records of tables in OVN SB DB." +
					"If set to false, it will conditionally monitor the records that " +
					"is needed in the current chassis. Values are false(0), true(1) ",
			}, func() float64 {
				stdout, stderr, err := util.RunOVSVsctl("get", "Open_vSwitch", ".",
					"external_ids:ovn-monitor-all")
				if err != nil {
					klog.Errorf("Failed to get ovn-monitor-all value stderr(%s): (%v)",
						stderr, err)
					return -1
				}
				var ovnMonitorValue float64
				if stdout == "true" {
					ovnMonitorValue = 1
				}
				return ovnMonitorValue
			}))
		prometheus.MustRegister(prometheus.NewGaugeFunc(
			prometheus.GaugeOpts{
				Namespace: MetricOvnNamespace,
				Subsystem: MetricOvnSubsystemController,
				Name:      "packet_in_drop",
				Help: "Specifies the number of times the ovn-controller has dropped the " +
					"packet-ins from ovs-vswitchd due to resource constraints",
			}, func() float64 {
				counters, err := coverageShowCounters(ovnController)
				if err != nil {
					klog.Errorf("%s", err.Error())
					return 0
				}

				packetInDropCounters := []string{
					"pinctrl_drop_put_mac_binding",
					"pinctrl_drop_buffered_packets_map",
					"pinctrl_drop_controller_event",
					"pinctrl_drop_put_vport_binding",
				}
				var packetInDropMetricValue float64
				for _, counterName := range packetInDropCounters {
					if value, ok := counters[counterName]; ok {
						packetInDropMetricValue += parseMetricToFloat(counterName, value)
					}
				}
				return packetInDropMetricValue
			}))

		registerCoverageShowCounters(ovnController, MetricOvnNamespace, MetricOvnSubsystemController)
		go coverageShowCountersMetricsUpdater(ovnController)
	})
}

// coverageShowCountersMetricsUpdater updates the counter metric
// by obtaing values from coverageShowCounters for specified target.
func coverageShowCountersMetricsUpdater(target string) {
	for {
		counters, err := coverageShowCounters(target)
		if err != nil {
			klog.Errorf("%s", err.Error())
			time.Sleep(30 * time.Second)
			continue
		}
		coverageCountersMetricsMap := targetMetricMap[target]
		for counterMetricName, counterMetricInfo := range coverageCountersMetricsMap {
			counterName := counterMetricName
			if counterMetricName == "packet_in" {
				counterName = "flow_extract"
			}
			if value, ok := counters[counterName]; ok {
				metricName := target + "_" + counterName
				count := parseMetricToFloat(metricName, value)
				counterMetricInfo.metric.Set(count)
			} else {
				counterMetricInfo.metric.Set(0)
			}
		}
		time.Sleep(30 * time.Second)
	}
}
