// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lib

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"

	"github.com/grafana/grafana-foundation-sdk/go/dashboard"
	"github.com/grafana/grafana-foundation-sdk/go/resource"
)

type DashboardInput struct {
	Name        string `json:"name"`
	Label       string `json:"label"`
	Description string `json:"description"`
	Type        string `json:"type"`
	PluginId    string `json:"pluginId"`
	PluginName  string `json:"pluginName"`
}

type DashboardAux struct {
	Inputs []DashboardInput `json:"__inputs,omitempty"`
	dashboard.Dashboard
}

type BuildMode string

const (
	GrafanaDashboard BuildMode = "GrafanaDashboard"
	Dashboard        BuildMode = "Dashboard"
)

type SdnDashboard interface {
	GetFilename() string
	Build() (dashboard.Dashboard, error)
	GenerateJson() ([]byte, error)
	GenerateManifest() ([]byte, error)
	SetMode(mode BuildMode)
	GetBaseSdnDashboard() *BaseSdnDashboard

	DataSourceRef() dashboard.DataSourceRef
}

type BaseSdnDashboard struct {

	// filename of the dashboard
	Filename string

	Title string

	Description string
	Uid         string
	Tags        []string

	Builder *dashboard.DashboardBuilder

	// define mode as a typed string with values: GrafanaDashboard, Dashboard,
	BuildMode BuildMode

	// sdk doesn't generate __inputs, so add it manually
	Inputs []DashboardInput

	DataSourceUid string
}

// GenerateJson is a helper function that works with the interface to avoid method dispatch issues
func GenerateJson(d SdnDashboard) ([]byte, error) {
	dashboard, err := d.Build()
	if err != nil {
		return nil, err
	}

	// Get the base dashboard to access the Inputs field
	baseDashboard := getBaseDashboard(d)
	aux := DashboardAux{
		Dashboard: dashboard,
		Inputs:    baseDashboard.Inputs,
	}
	return json.MarshalIndent(aux, "", "  ")
}

// Helper to get the base dashboard from any concrete type
func getBaseDashboard(d SdnDashboard) *BaseSdnDashboard {
	// Use type assertion to get the underlying BaseSdnDashboard
	// This is a temporary solution - in a better design, we'd have a method on the interface
	switch v := d.(type) {
	case interface{ GetBaseSdnDashboard() *BaseSdnDashboard }:
		return v.GetBaseSdnDashboard()
	default:
		panic("dashboard type doesn't implement GetBaseSdnDashboard() method")
	}
}

func (d *BaseSdnDashboard) GenerateJson() ([]byte, error) {
	// This method exists for interface compatibility but shouldn't be called directly
	// Use the standalone GenerateJson function instead
	panic("GenerateJson method called on base type - use lib.GenerateJson(dashboard) instead")
}

// GenerateManifest is a helper function that works with the interface
func GenerateManifest(d SdnDashboard) ([]byte, error) {
	d.SetMode(Dashboard)

	dashboard, err := d.Build()
	if err != nil {
		return nil, err
	}

	manifest := DashboardManifest("eevq404t7ec5ca", d.GetFilename(), dashboard)
	return json.MarshalIndent(manifest, "", "  ")
}

func (d *BaseSdnDashboard) GenerateManifest() ([]byte, error) {
	// This method exists for interface compatibility but shouldn't be called directly
	// Use the standalone GenerateManifest function instead
	panic("GenerateManifest method called on base type - use lib.GenerateManifest(dashboard) instead")
}

// create a dummy BaseSdnDashboard object to verify it implements the interface
var _ SdnDashboard = &BaseSdnDashboard{}

func (d *BaseSdnDashboard) GetFilename() string {
	return d.Filename
}

func (d *BaseSdnDashboard) GetBaseSdnDashboard() *BaseSdnDashboard {
	return d
}

func (d *BaseSdnDashboard) Build() (dashboard.Dashboard, error) {
	panic("not implemented")
}

func (d *BaseSdnDashboard) SetMode(mode BuildMode) {
	d.BuildMode = mode
}

func (d *BaseSdnDashboard) DataSourceRef() dashboard.DataSourceRef {
	if d.BuildMode == GrafanaDashboard {
		return PrometheusDataSourceRef()
	} else if d.BuildMode == Dashboard {
		// TODO: make datasource uid configurable
		d.DataSourceUid = "prometheus"
		return dashboard.DataSourceRef{Type: &PrometheusDataSourceType, Uid: &d.DataSourceUid}
	}
	return PrometheusDataSourceRef()
}

func NewSdnDashboard(filename string, title string, description string) BaseSdnDashboard {
	uid := md5.Sum([]byte(filename))
	return BaseSdnDashboard{
		Filename:    filename,
		Title:       title,
		Description: description,
		Uid:         hex.EncodeToString(uid[:]),
		Tags:        []string{"SDN"},
		Inputs: []DashboardInput{
			{
				Name:        "DS_PROMETHEUS",
				Label:       "prometheus",
				Description: "",
				Type:        "datasource",
				PluginId:    "prometheus",
				PluginName:  "Prometheus",
			},
		},
	}
}

func DashboardManifest(folderUid string, name string, dash any) resource.Manifest {
	return resource.Manifest{
		ApiVersion: "dashboard.grafana.app/v1beta1",
		Kind:       "Dashboard",
		Metadata: resource.Metadata{
			Annotations: map[string]string{
				"grafana.app/folder": folderUid,
			},
			Name: name,
		},
		Spec: dash,
	}
}
