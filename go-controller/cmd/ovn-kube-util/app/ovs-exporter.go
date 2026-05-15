// SPDX-FileCopyrightText: Copyright The OVN-Kubernetes Contributors
// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/urfave/cli/v2"

	"k8s.io/klog/v2"
	kexec "k8s.io/utils/exec"

	"github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/libovsdb"
	"github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/metrics"
	"github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/util"
)

var OvsExporterCommand = cli.Command{
	Name:  "ovs-exporter",
	Usage: "",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:  "metrics-bind-address",
			Usage: `The IP address and port for the metrics server to serve on (default ":9310")`,
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
		innerCtx, cancel := context.WithCancel(ctx.Context)
		defer cancel()

		bindAddress := ctx.String("metrics-bind-address")
		if bindAddress == "" {
			bindAddress = "0.0.0.0:9310"
		}

		certFile := ctx.String("tls-cert-file")
		keyFile := ctx.String("tls-key-file")

		if err := util.SetSpecificExec(kexec.New(), "ovs-vsctl", "ovs-dpctl",
			"ovs-ofctl", "ovs-appctl", "ovsdb-client"); err != nil {
			return err
		}

		wg := &sync.WaitGroup{}
		// start the ovsdb client for ovs metrics monitoring
		ovsClient, err := libovsdb.NewOVSClient(innerCtx.Done())
		if err != nil {
			klog.Errorf("Error initializing ovs client: %v", err)
			return err
		}
		hostName, err := os.Hostname()
		if err != nil {
			return fmt.Errorf("cannot get hostname: %v", err)
		}

		opts := metrics.MetricServerOptions{
			BindAddress:      bindAddress,
			CertFile:         certFile,
			KeyFile:          keyFile,
			EnableOVSMetrics: true,
			OnFatalError:     cancel,
			NodeName:         hostName,
		}

		metrics.StartOVNMetricsServer(opts, ovsClient, nil, innerCtx.Done(), wg)

		// run until cancelled (by OS signal or fatal error)
		<-innerCtx.Done()
		klog.Info("Shutdown signal received, stopping metrics server...")

		// Wait for all goroutines to finish with a timeout
		done := make(chan struct{})
		go func() {
			wg.Wait()
			close(done)
		}()

		select {
		case <-done:
			klog.Info("Metrics server stopped gracefully")
		case <-time.After(10 * time.Second):
			klog.Warning("Timeout waiting for metrics server to stop")
		}

		return nil
	},
}
