// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package patch

import (
	"fmt"
	"os"
	"path/filepath"

	"obs-as-code/pkg/log"
)

func RunDashboardCmd(dashboardFile, dashboardDir, outDir string) error {
	processOne := func(path string) error {
		log.Infof("processing dashboard file %s, out_dir %s", path, outDir)
		return ProcessDashboardTemplateFile(path, outDir)
	}

	if dashboardFile != "" {
		return processOne(dashboardFile)
	}

	entries, err := os.ReadDir(dashboardDir)
	if err != nil {
		return fmt.Errorf("fatal error reading --dashboard-dir %s: %w", dashboardDir, err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		if err := processOne(filepath.Join(dashboardDir, entry.Name())); err != nil {
			return fmt.Errorf("fatal error processing %s: %w", filepath.Join(dashboardDir, entry.Name()), err)
		}
	}

	return nil
}
