// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package build

import (
	"os"
	"path/filepath"

	"obs-as-code/pkg/build/dashboard"
	"obs-as-code/pkg/build/dashboard/lib"
	"obs-as-code/pkg/log"
)

// RunDashboardBuild generates dashboard JSON files and optional manifests.
func RunDashboardBuild(jsonDir string, manifestsDir string) error {
	manifests := manifestsDir != ""

	if err := os.MkdirAll(jsonDir, 0o755); err != nil {
		return err
	}
	if manifests {
		if err := os.MkdirAll(manifestsDir, 0o755); err != nil {
			return err
		}
	}

	dashboards := []lib.SdnDashboard{
		dashboard.NewOvnKubeClusterManagerDashboard(),
		dashboard.NewOvnControllerDashboard(),
		dashboard.NewOvnNorthdDashboard(),
		dashboard.NewOvnkubeControllerDashboard(),
		dashboard.NewOvnNorthboundDbDashboard(),
		dashboard.NewOvnSouthboundDbDashboard(),
		dashboard.NewHostOvsDashboard(),
	}

	for _, d := range dashboards {
		raw, err := lib.GenerateJson(d)
		if err != nil {
			return err
		}
		jsonPath := filepath.Join(jsonDir, d.GetFilename()+".json")
		if err := os.WriteFile(jsonPath, raw, 0o644); err != nil {
			return err
		}
		log.Infof("generated dashboard json: %s", jsonPath)

		if !manifests {
			continue
		}

		manifestJSON, err := lib.GenerateManifest(d)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(manifestsDir, d.GetFilename()+".json"), manifestJSON, 0o644); err != nil {
			return err
		}
	}

	return nil
}
