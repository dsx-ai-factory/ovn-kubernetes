package app

import (
	"fmt"

	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/metrics"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util"
	"github.com/urfave/cli/v2"
	kexec "k8s.io/utils/exec"
)

var metricsScrapeInterval int
var OvsExporterCommand = cli.Command{
	Name:  "ovs-exporter",
	Usage: "",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:  "metrics-bind-address",
			Usage: `The IP address and port for the metrics server to serve on (default ":9310")`,
		},
		&cli.IntFlag{
			Name:        "metrics-interval",
			Usage:       "The Interval at which ovs metrics are collected",
			Value:       30,
			Destination: &metricsScrapeInterval,
		},
		&cli.StringFlag{
			Name:  "tls-cert-file",
			Usage: "The certificate to use for TLS",
		},
		&cli.StringFlag{
			Name:  "tls-key-file",
			Usage: "The key to use for TLS",
		},
	},
	Action: func(ctx *cli.Context) error {
		bindAddress := ctx.String("metrics-bind-address")
		if bindAddress == "" {
			bindAddress = "0.0.0.0:9310"
		}

		tlsCertFile := ctx.String("tls-cert-file")
		tlsKeyFile := ctx.String("tls-key-file")

		if err := util.SetSpecificExec(kexec.New(), "ovs-vsctl", "ovs-dpctl",
			"ovs-ofctl", "ovs-appctl", "ovsdb-client"); err != nil {
			return err
		}

		stopChan := make(chan struct{})

		// start the ovsdb client for ovs metrics monitoring
		ovsDBClient, err := metrics.SetupOvsDBClient()
		if err != nil {
			return fmt.Errorf("error when trying to initialize ovsdb client: %v", err)
		}
		// register ovs metrics that will be served off of /metrics path
		metrics.RegisterOvsMetrics(ovsDBClient, metricsScrapeInterval, stopChan)
		// start the prometheus server to serve OVS Metrics (default port: 9310)
		// use TLS if cert and key file were provided at the command line
		metrics.StartMetricsServer(bindAddress, false, tlsCertFile, tlsKeyFile)

		// run until cancelled
		<-ctx.Context.Done()
		close(stopChan)
		return nil
	},
}
