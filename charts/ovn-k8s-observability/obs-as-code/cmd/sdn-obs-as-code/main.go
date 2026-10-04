// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"obs-as-code/pkg/build"
	"obs-as-code/pkg/log"
	"obs-as-code/pkg/patch"

	"github.com/spf13/cobra"
)

func setupLogger(verbose bool) {
	log.Setup(verbose)
}

func newRootCmd() *cobra.Command {
	var verbose bool

	rootCmd := &cobra.Command{
		Use:   "sdn-obs-as-code",
		Short: "Build dashboards and apply PromQL rewrites",
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			setupLogger(verbose)
		},
	}
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "enable debug logs")

	modifyCmd := &cobra.Command{
		Use:   "modify",
		Short: "Modify resources by type",
	}

	var dashboardFile string
	var dashboardDir string
	var outDir string
	dashboardCmd := &cobra.Command{
		Use:   "dashboard",
		Short: "Patch dashboard PromQL expressions and template variables",
		Run: func(cmd *cobra.Command, args []string) {
			if dashboardFile == "" && dashboardDir == "" {
				log.Fatalf("fatal error: one of --dashboard-file or --dashboard-dir is required\n")
			}
			if dashboardFile != "" && dashboardDir != "" {
				log.Fatalf("fatal error: --dashboard-file and --dashboard-dir are mutually exclusive\n")
			}
			if err := patch.RunDashboardCmd(dashboardFile, dashboardDir, outDir); err != nil {
				log.Fatalf("fatal error: %v\n", err)
			}
		},
	}
	dashboardCmd.Flags().StringVar(&dashboardFile, "dashboard-file", "", "Grafana dashboard JSON file")
	dashboardCmd.Flags().StringVar(&dashboardDir, "dashboard-dir", "", "Directory containing Grafana dashboard JSON files")
	dashboardCmd.Flags().StringVar(&outDir, "out-dir", "", "Output directory for modified dashboard JSON (preserves source file)")

	modifyCmd.AddCommand(dashboardCmd)
	rootCmd.AddCommand(modifyCmd)

	buildCmd := &cobra.Command{
		Use:   "build",
		Short: "Build observability resources",
	}

	var buildDashboardJSONDir string
	var buildDashboardManifestsDir string
	buildDashboardCmd := &cobra.Command{
		Use:   "dashboard",
		Short: "Build dashboard JSON and optional manifests",
		Run: func(cmd *cobra.Command, args []string) {
			if err := build.RunDashboardBuild(buildDashboardJSONDir, buildDashboardManifestsDir); err != nil {
				log.Fatalf("fatal error: %v\n", err)
			}
		},
	}
	buildDashboardCmd.Flags().StringVar(&buildDashboardJSONDir, "json-dir", "./json", "Directory in which dashboard JSON files are generated")
	buildDashboardCmd.Flags().StringVar(&buildDashboardManifestsDir, "manifests-dir", "", "Directory in which dashboard manifests are generated (when set, manifests are generated)")

	buildCmd.AddCommand(buildDashboardCmd)
	rootCmd.AddCommand(buildCmd)

	return rootCmd
}

func main() {
	if err := newRootCmd().Execute(); err != nil {
		log.Fatalf("fatal error: %v\n", err)
	}
}
