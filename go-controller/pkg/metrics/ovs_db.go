//go:build linux

package metrics

import (
	"os"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"k8s.io/klog/v2"
)

// ovs local db coverage/show metrics
var ovsDbCoverageShowMetricsMap = map[string]*metricDetails{
	"hmap_pathological": {
		help: "Registering how many hash map resize calls has been " +
			"made that resulted in copying buckets with 6+ nodes (collision factor)",
	},
	"hmap_expand": {
		help: "Registering how many hash map resizes so far has been made",
	},
	"lockfile_lock": {
		help: "Registering how many expensive file locking has been made",
	},
	"poll_create_node": {
		help: "How many scheduled events to wake up blocking poller (event loop busy factor)",
	},
	"poll_zero_timeout": {
		help: "How many scheduled events were processed without timeout (event loop effectiveness)",
	},
	"seq_change": {
		help: "Registering intensity of new objects creations",
	},
	"pstream_open": {
		help: "Specifies the number of time passive connections " +
			"were opened for the remote peer to connect.",
	},
	"stream_open": {
		help: "Specifies the number of attempts to connect " +
			"to a remote peer (active connection).",
	},
	"unixctl_received": {
		help: "Another metric that shows how many JSON RPC requests " +
			"actually received in OVSDB server",
	},
	"unixctl_replied": {
		help: "Metric showing to how many of received JSON RPC requests " +
			"OVSDB server actually replied",
	},
	"util_xalloc": {
		help: "Registering intensity of memory allocations in OVSDB server",
	},
}

var metricOvsDbSize = prometheus.NewGauge(prometheus.GaugeOpts{
	Namespace: MetricOvsNamespace,
	Subsystem: MetricOvsSubsystemOvsDB,
	Name:      "db_size",
	Help:      "The size of the database file associated with the OVS DB on each node.",
})

func ovsDbSizeMetricUpdater(metricsScrapeInterval int, stopChan <-chan struct{}) {
	ticker := time.NewTicker(time.Duration(metricsScrapeInterval) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			dbFile := "/etc/openvswitch/conf.db"
			// container case, /host mountPath
			fileInfo, err := os.Stat("/host/" + dbFile)
			if err != nil {
				// host case
				fileInfo, err = os.Stat(dbFile)
			}
			if err != nil {
				klog.Errorf("Failed to get the OVS DB size :(%v)", err)
			} else {
				metricOvsDbSize.Set(float64(fileInfo.Size()))
			}
		case <-stopChan:
			return

		}
	}
}

var registerOvsDBMetricsOnce sync.Once

func RegisterOvsDBMetrics(metricsScrapeInterval int, stopChan <-chan struct{}) {
	registerOvsDBMetricsOnce.Do(func() {
		prometheus.MustRegister(metricOvsDbSize)
		// Register OVSDB coverage/show metrics with prometheus
		componentCoverageShowMetricsMap[ovsDB] = ovsDbCoverageShowMetricsMap
		registerCoverageShowMetrics(ovsDB, MetricOvsNamespace, MetricOvsSubsystemOvsDB)
		// OVSDB coverage/show metrics updater
		go coverageShowMetricsUpdater(ovsDB, metricsScrapeInterval, stopChan)
		// OVSDB size metric uodater
		go ovsDbSizeMetricUpdater(metricsScrapeInterval, stopChan)

	})
}
