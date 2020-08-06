package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/ovn-org/ovn-kubernetes/go-controller/cmd/ovn-kube-util/app"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/config"
	"github.com/urfave/cli/v2"
	"k8s.io/klog"
)

func main() {
	c := cli.NewApp()
	c.Name = "ovn-kube-util"
	c.Usage = "Utils for kubernetes ovn"
	c.Version = config.Version
	c.Flags = []cli.Flag{
		&cli.IntFlag{
			Name: "loglevel",
			Usage: "klog verbosity level (default: 4). Info, warn, fatal, error are always printed. " +
				"For debug messages, use 5. ",
			Value: 0,
		},
	}
	c.Commands = []*cli.Command{
		&app.NicsToBridgeCommand,
		&app.BridgesToNicCommand,
		&app.ReadinessProbeCommand,
		&app.OvsExporterCommand,
	}

	c.Before = func(ctx *cli.Context) error {
		var level klog.Level

		klog.SetOutput(os.Stderr)
		if err := level.Set(strconv.Itoa(ctx.Int("loglevel"))); err != nil {
			return fmt.Errorf("failed to set klog log level %v", err)
		}
		return nil
	}

	ctx := context.Background()

	// trap SIGHUP, SIGINT, SIGTERM, SIGQUIT and
	// cancel the context
	ctx, cancel := context.WithCancel(ctx)
	exitCh := make(chan os.Signal, 1)
	signal.Notify(exitCh,
		syscall.SIGHUP,
		syscall.SIGINT,
		syscall.SIGTERM,
		syscall.SIGQUIT)
	defer func() {
		signal.Stop(exitCh)
		cancel()
	}()
	go func() {
		select {
		case s := <-exitCh:
			klog.Infof("Received signal %s. Shutting down", s)
			cancel()
		case <-ctx.Done():
		}
	}()

	if err := c.RunContext(ctx, os.Args); err != nil {
		klog.Exit(err)
	}
}
