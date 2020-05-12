package metrics

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util"
	"github.com/prometheus/client_golang/prometheus"
	"k8s.io/klog"
)

const (
	ovsVswitchd = "ovs-vswitchd"
)

var (
	ovsVersion string
)

// ovs Data Path Metrics
var metricOvsDpTotal = prometheus.NewGauge(prometheus.GaugeOpts{
	Namespace: MetricOvsNamespace,
	Subsystem: MetricOvsSubsystemVswitchd,
	Name:      "dp_total",
	Help:      "Represents total number of datapaths on the system.",
})

var metricOvsDp = prometheus.NewGaugeVec(prometheus.GaugeOpts{
	Namespace: MetricOvsNamespace,
	Subsystem: MetricOvsSubsystemVswitchd,
	Name:      "dp",
	Help: "A metric with a constant '1' value labeled by datapath " +
		"name present on the instance."},
	[]string{
		"datapath",
		"type",
	},
)

var metricOvsDpIfTotal = prometheus.NewGaugeVec(prometheus.GaugeOpts{
	Namespace: MetricOvsNamespace,
	Subsystem: MetricOvsSubsystemVswitchd,
	Name:      "dp_if_total",
	Help:      "Represents the number of ports connected to the datapath."},
	[]string{
		"datapath",
	},
)

var metricOvsDpIf = prometheus.NewGaugeVec(prometheus.GaugeOpts{
	Namespace: MetricOvsNamespace,
	Subsystem: MetricOvsSubsystemVswitchd,
	Name:      "dp_if",
	Help: "A metric with a constant '1' value labeled by " +
		"datapath name, port name, port type and datapath port number."},
	[]string{
		"datapath",
		"port",
		"type",
		"ofPort",
	},
)

var metricOvsDpFlowsTotal = prometheus.NewGaugeVec(prometheus.GaugeOpts{
	Namespace: MetricOvsNamespace,
	Subsystem: MetricOvsSubsystemVswitchd,
	Name:      "dp_flows_total",
	Help:      "Represents the number of flows in datapath."},
	[]string{
		"datapath",
	},
)

var metricOvsDpFlowsLookupHit = prometheus.NewGaugeVec(prometheus.GaugeOpts{
	Namespace: MetricOvsNamespace,
	Subsystem: MetricOvsSubsystemVswitchd,
	Name:      "dp_flows_lookup_hit",
	Help: "Represents number of packets matching the existing flows " +
		"while processing incoming packets in the datapath."},
	[]string{
		"datapath",
	},
)

var metricOvsDpFlowsLookupMissed = prometheus.NewGaugeVec(prometheus.GaugeOpts{
	Namespace: MetricOvsNamespace,
	Subsystem: MetricOvsSubsystemVswitchd,
	Name:      "dp_flows_lookup_missed",
	Help: "Represents the number of packets not matching any existing " +
		"flow  and require  user space processing."},
	[]string{
		"datapath",
	},
)

var metricOvsDpFlowsLookupLost = prometheus.NewGaugeVec(prometheus.GaugeOpts{
	Namespace: MetricOvsNamespace,
	Subsystem: MetricOvsSubsystemVswitchd,
	Name:      "dp_flows_lookup_lost",
	Help: "number of packets destined for user space process but " +
		"subsequently dropped before  reaching  userspace."},
	[]string{
		"datapath",
	},
)

var metricOvsDpPacketsTotal = prometheus.NewGaugeVec(prometheus.GaugeOpts{
	Namespace: MetricOvsNamespace,
	Subsystem: MetricOvsSubsystemVswitchd,
	Name:      "dp_packets_total",
	Help: "Represents the total number of packets datapath processed " +
		"which is the sum of hit and missed."},
	[]string{
		"datapath",
	},
)

var metricOvsdpMasksHit = prometheus.NewGaugeVec(prometheus.GaugeOpts{
	Namespace: MetricOvsNamespace,
	Subsystem: MetricOvsSubsystemVswitchd,
	Name:      "dp_masks_hit",
	Help:      "Represents the total number of masks visited for matching incoming packets.",
},
	[]string{
		"datapath",
	},
)

var metricOvsDpMasksTotal = prometheus.NewGaugeVec(prometheus.GaugeOpts{
	Namespace: MetricOvsNamespace,
	Subsystem: MetricOvsSubsystemVswitchd,
	Name:      "dp_masks_total",
	Help:      "Represents the number of masks in a datapath."},
	[]string{
		"datapath",
	},
)

var metricOvsDpMasksHitRatio = prometheus.NewGaugeVec(prometheus.GaugeOpts{
	Namespace: MetricOvsNamespace,
	Subsystem: MetricOvsSubsystemVswitchd,
	Name:      "dp_masks_hit_ratio",
	Help: "Represents the average number of masks visited per packet " +
		"the  ratio between hit and total number of packets processed by the datapath."},
	[]string{
		"datapath",
	},
)

func getOvsVersionInfo() {
	stdout, _, err := util.RunOVSVsctl("--version")
	if err == nil && strings.HasPrefix(stdout, "ovs-vsctl (Open vSwitch)") {
		ovsVersion = strings.Fields(stdout)[3]
	}
}

func parseMetricToFloat(name, value string) float64 {
	f64Value, err := strconv.ParseFloat(value, 64)
	if err != nil {
		klog.Errorf("Failed to parse %s value into float for metric %s :(%v)",
			value, name, err)
		return 0
	}
	return f64Value
}

// ovsDataPathLookupsMetrics obtains the ovs datapath
// (lookups: hit, missed, lost) metrics and updates them.
func ovsDataPathLookupsMetrics(output string, dataPath string) {
	var dataPathPacketsTotal float64
	for _, field := range strings.Fields(output) {
		elem := strings.Split(field, ":")
		if len(elem) != 2 {
			continue
		}
		switch elem[0] {
		case "hit":
			value := parseMetricToFloat("dp_flows_lookup_hit", elem[1])
			dataPathPacketsTotal = dataPathPacketsTotal + value
			metricOvsDpFlowsLookupHit.WithLabelValues(dataPath).Set(value)
		case "missed":
			value := parseMetricToFloat("dp_flows_lookup_missed", elem[1])
			dataPathPacketsTotal = dataPathPacketsTotal + value
			metricOvsDpFlowsLookupMissed.WithLabelValues(dataPath).Set(value)
		case "lost":
			value := parseMetricToFloat("dp_flows_lookup_lost", elem[1])
			metricOvsDpFlowsLookupLost.WithLabelValues(dataPath).Set(value)
		}
	}
	metricOvsDpPacketsTotal.WithLabelValues(dataPath).Set(dataPathPacketsTotal)
}

// ovsDataPathMasksMetrics obatins ovs datapath masks metrics
// (masks :hit, total, hit/pkt) and updates them.
func ovsDataPathMasksMetrics(output string, datapath string) {
	for _, field := range strings.Fields(output) {
		elem := strings.Split(field, ":")
		if len(elem) != 2 {
			continue
		}
		switch elem[0] {
		case "hit":
			value := parseMetricToFloat("dp_masks_hit", elem[1])
			metricOvsdpMasksHit.WithLabelValues(datapath).Set(value)
		case "total":
			value := parseMetricToFloat("dp_masks_total", elem[1])
			metricOvsDpMasksTotal.WithLabelValues(datapath).Set(value)
		case "hit/pkt":
			value := parseMetricToFloat("dp_masks_hit_ratio", elem[1])
			metricOvsDpMasksHitRatio.WithLabelValues(datapath).Set(value)
		}
	}
}

// ovsDataPathPortMetrics obtains the ovs datapath port metrics
// from ovs-appctl dpctl/show(portname, porttype, portnumber) and updates them.
func ovsDataPathPortMetrics(output string, datapath string) {
	portFields := strings.Fields(output)
	portType := "system"
	if len(portFields) > 3 {
		portType = strings.Trim(portFields[3], "():")
	}

	portName := strings.TrimSpace(portFields[2])
	portNumber := strings.Trim(portFields[1], ":")
	metricOvsDpIf.WithLabelValues(datapath, portName, portType, portNumber).Set(1)
}

// getOvsDatapaths gives list of datapaths
// and updates the corresponding dataPath metrics
func getOvsDataPaths() ([]string, error) {
	stdout, stderr, err := util.RunOVSDpctl("dump-dps")
	if err != nil {
		return nil, fmt.Errorf("failed to get output of ovs-dpctl dump-dps "+
			"stderr(%s) :(%v)", stderr, err)
	}
	var dataPathsList []string
	for _, kvPair := range strings.Split(stdout, "\n") {
		output := strings.TrimSpace(kvPair)
		dataPath := strings.Split(output, "@")
		dataPathType, dataPathName := dataPath[0], dataPath[1]
		metricOvsDp.WithLabelValues(dataPathName, dataPathType).Set(1)
		dataPathsList = append(dataPathsList, dataPathName)
	}
	metricOvsDpTotal.Set(float64(len(dataPathsList)))
	return dataPathsList, nil
}

// ovsDataPathMetricsUpdate updates the ovs datapath metrics for every 30 sec
func ovsDataPathMetricsUpdate() {
	for {
		dataPaths, err := getOvsDataPaths()
		if err != nil {
			klog.Errorf("%s", err.Error())
			time.Sleep(30 * time.Second)
			continue
		}
		if len(dataPaths) == 0 {
			klog.Infof("Currently, no datapath is present ")
		}

		for _, dataPathName := range dataPaths {
			stdout, stderr, err := util.RunOVSDpctl("show", dataPathName)
			if err != nil {
				klog.Errorf("Failed to get datapath stats for %s "+
					"stderr(%s) :(%v)", dataPathName, stderr, err)
				time.Sleep(30 * time.Second)
				continue
			}
			var dataPathPortCount float64
			for i, kvPair := range strings.Split(stdout, "\n") {
				if i <= 0 {
					continue
				}
				output := strings.TrimSpace(kvPair)
				if strings.HasPrefix(output, "lookups:") {
					ovsDataPathLookupsMetrics(output, dataPathName)
				} else if strings.HasPrefix(output, "masks:") {
					ovsDataPathMasksMetrics(output, dataPathName)
				} else if strings.HasPrefix(output, "port ") {
					ovsDataPathPortMetrics(output, dataPathName)
					dataPathPortCount++
				} else if strings.HasPrefix(output, "flows:") {
					flowFields := strings.Fields(output)
					value := parseMetricToFloat("dp_flows_total", flowFields[1])
					metricOvsDpFlowsTotal.WithLabelValues(dataPathName).Set(value)
				}
			}
			metricOvsDpIfTotal.WithLabelValues(dataPathName).Set(dataPathPortCount)
		}
		time.Sleep(30 * time.Second)
	}
}

var ovsVswitchdCoverageShowCountersMap = map[string]*metricDetails{
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
	"pstream_open": {
		help: "Specifies the number of time passive connections " +
			"were opened for the remote peer to connect.",
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
	"dpif_port_add": {
		help: "Number of times a netdev was added as a port to the dpif.",
	},
	"dpif_port_del": {
		help: "Number of times a netdev was removed from the dpif.",
	},
	"dpif_flow_flush": {
		help: "Number of times flows were flushed from the datapath " +
			"(Linux kernel datapath module).",
	},
	"dpif_flow_get": {
		help: "Number of times flows were retrieved from the " +
			"datapath (Linux kernel datapath module).",
	},
	"dpif_flow_put": {
		help: "Number of times flows were added to the datapath " +
			"(Linux kernel datapath module).",
	},
	"dpif_flow_del": {
		help: "Number of times flows were deleted from the " +
			"datapath (Linux kernel datapath module).",
	},
	"bridge_reconfigure": {
		help: "Number of times OVS bridges were reconfigured.",
	},
	"xlate_actions": {
		help: "Number of times an OpenFlow actions were translated " +
			"into datapath actions.",
	},
	"xlate_actions_oversize": {
		help: "Number of times the translated OpenFlow actions into " +
			"a datapath actions were too big for a netlink attribute.",
	},
	"xlate_actions_too_many_output": {
		help: "Number of times the number of datapath actions " +
			"were more than what the kernel can handle reliably.",
	},
}
var registerOvsMetricsOnce sync.Once

func RegisterOvsMetrics() {
	registerOvsMetricsOnce.Do(func() {
		getOvsVersionInfo()
		prometheus.MustRegister(prometheus.NewGaugeFunc(
			prometheus.GaugeOpts{
				Namespace: MetricOvsNamespace,
				Name:      "build_info",
				Help:      "A metric with a constant '1' value labeled by ovs version.",
				ConstLabels: prometheus.Labels{
					"version": ovsVersion,
				},
			},
			func() float64 { return 1 },
		))

		// Register OVS datapath metrics.
		prometheus.MustRegister(metricOvsDpTotal)
		prometheus.MustRegister(metricOvsDp)
		prometheus.MustRegister(metricOvsDpIfTotal)
		prometheus.MustRegister(metricOvsDpIf)
		prometheus.MustRegister(metricOvsDpFlowsTotal)
		prometheus.MustRegister(metricOvsDpFlowsLookupHit)
		prometheus.MustRegister(metricOvsDpFlowsLookupMissed)
		prometheus.MustRegister(metricOvsDpFlowsLookupLost)
		prometheus.MustRegister(metricOvsDpPacketsTotal)
		prometheus.MustRegister(metricOvsdpMasksHit)
		prometheus.MustRegister(metricOvsDpMasksTotal)
		prometheus.MustRegister(metricOvsDpMasksHitRatio)

		// ovs datapath metrics updater
		go ovsDataPathMetricsUpdate()
		// Register the ovs-vswitchd coverage/show counters metrics with prometheus
		registerCoverageShowCounters(ovsVswitchd, MetricOvsNamespace, MetricOvsSubsystemVswitchd)
		go coverageShowCountersMetricsUpdater(ovsVswitchd)
	})
}
