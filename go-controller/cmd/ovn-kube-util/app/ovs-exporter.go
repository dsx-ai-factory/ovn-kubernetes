package app

import (
	"k8s.io/klog"
	"net/http"
	"strings"

	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/metrics"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/urfave/cli/v2"
	kexec "k8s.io/utils/exec"
)

var (
	ovsVersion string
)

func getOvsVersionInfo() {
	stdout, _, err := util.RunOVSVsctl("--version")
	if err == nil && strings.HasPrefix(stdout, "ovs-vsctl (Open vSwitch)") {
		ovsVersion = strings.Fields(stdout)[3]
	}
}

var OvsExporterCommand = cli.Command{
	Name:  "ovs-exporter",
	Usage: "",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:  "metrics-bind-address",
			Usage: `The IP address and port for the metrics server to serve on (default ":9310")`,
		},
	},
	Action: func(ctx *cli.Context) error {
		bindAddress := ctx.String("metrics-bind-address")
		if bindAddress == "" {
			bindAddress = "0.0.0.0:9310"
		}

		if err := util.SetExec(kexec.New()); err != nil {
			return err
		}
		mux := http.NewServeMux()
		mux.Handle("/metrics", promhttp.Handler())

		getOvsVersionInfo()
		// register metrics that will be served off of /metrics path
		prometheus.MustRegister(prometheus.NewGaugeFunc(
			prometheus.GaugeOpts{
				Namespace: metrics.MetricOvsNamespace,
				Name:      "build_info",
				Help:      "A metric with a constant '1' value labeled by ovs version.",
				ConstLabels: prometheus.Labels{
					"version": ovsVersion,
				},
			},
			func() float64 { return 1 },
		))

		err := http.ListenAndServe(bindAddress, mux)
		if err != nil {
			klog.Exitf("starting metrics server failed: %v", err)
		}
		return nil
	},
}
