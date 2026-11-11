// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package node

import (
	"context"
	"errors"
	"testing"

	"github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/config"
	ovntest "github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/testing"
	"github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/types"
	"github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/util"
)

func setupLocalnetLifecycleTest(t *testing.T, mode string) (*UserDefinedLocalnetNodeNetworkController, *ovntest.FakeExec) {
	t.Helper()
	if err := config.PrepareTestConfig(); err != nil {
		t.Fatalf("prepare test config: %v", err)
	}
	t.Cleanup(func() {
		util.ResetRunner()
		if err := config.PrepareTestConfig(); err != nil {
			t.Errorf("restore test config: %v", err)
		}
	})
	config.OvnKubeNode.Mode = mode
	fexec := ovntest.NewFakeExec()
	if err := util.SetExec(fexec); err != nil {
		t.Fatalf("set fake exec: %v", err)
	}
	netInfo, err := util.ParseNADInfo(ovntest.GenerateNAD("blue", "blue", "default",
		types.LocalnetTopology, "", types.NetworkRoleSecondary))
	if err != nil {
		t.Fatalf("parse localnet NAD info: %v", err)
	}
	nc := NewUserDefinedLocalnetNodeNetworkController(
		&CommonNodeNetworkControllerInfo{name: "worker1"}, netInfo, nil, nil)
	return nc, fexec
}

func TestLocalnetStopPreservesBridgeMapping(t *testing.T) {
	for _, mode := range []string{types.NodeModeFull, types.NodeModeDPU, types.NodeModeDPUHost} {
		t.Run(mode, func(t *testing.T) {
			nc, fexec := setupLocalnetLifecycleTest(t, mode)
			nc.bridgeName = "br-blue"
			// A graceful stop must not issue any OVS commands, including when
			// another stop follows during shutdown after a partial startup.
			nc.Stop()
			nc.Stop()
			if !fexec.CalledMatchesExpected() {
				t.Fatalf("unexpected OVS commands during stop: %s", fexec.ErrorDesc())
			}
		})
	}
}

func TestLocalnetRestartPreservesBridgeMapping(t *testing.T) {
	nc, fexec := setupLocalnetLifecycleTest(t, types.NodeModeFull)
	for range 2 {
		fexec.AddFakeCmd(&ovntest.ExpectedCmd{
			Cmd:    "ovs-vsctl --timeout=15 --if-exists get Open_vSwitch . external_ids:ngn-localnet-bridge-mappings",
			Output: "blue:br-blue",
		})
		fexec.AddFakeCmd(&ovntest.ExpectedCmd{Cmd: "ovs-vsctl --timeout=15 br-exists br-blue"})
		fexec.AddFakeCmd(&ovntest.ExpectedCmd{
			Cmd:    "ovs-vsctl --timeout=15 --if-exists get Open_vSwitch . external_ids:ovn-bridge-mappings",
			Output: "physnet:breth0,blue_br-localnet:br-blue",
		})
	}
	if err := nc.Start(context.Background()); err != nil {
		t.Fatalf("start localnet controller: %v", err)
	}
	nc.Stop()
	// A new process must be able to reuse the mapping without rewriting it.
	restarted := NewUserDefinedLocalnetNodeNetworkController(
		&CommonNodeNetworkControllerInfo{name: "worker1"}, nc.GetNetInfo(), nil, nil)
	if err := restarted.Start(context.Background()); err != nil {
		t.Fatalf("restart localnet controller: %v", err)
	}
	restarted.Stop()
	if !fexec.CalledMatchesExpected() {
		t.Fatalf("unexpected OVS commands during restart: %s", fexec.ErrorDesc())
	}
}

func TestLocalnetCleanupBridgeMapping(t *testing.T) {
	for _, mode := range []string{types.NodeModeFull, types.NodeModeDPU} {
		t.Run(mode, func(t *testing.T) {
			nc, fexec := setupLocalnetLifecycleTest(t, mode)
			nc.bridgeName = "br-blue"
			const (
				mappings  = "physnet:breth0,blue_br-localnet:br-blue"
				readCmd   = "ovs-vsctl --timeout=15 --if-exists get Open_vSwitch . external_ids:ovn-bridge-mappings"
				deleteCmd = "ovs-vsctl --timeout=15 set Open_vSwitch . external_ids:ovn-bridge-mappings=physnet:breth0"
			)
			fexec.AddFakeCmd(&ovntest.ExpectedCmd{Cmd: readCmd, Output: mappings})
			fexec.AddFakeCmd(&ovntest.ExpectedCmd{Cmd: deleteCmd, Err: errors.New("OVS transaction failed")})
			fexec.AddFakeCmd(&ovntest.ExpectedCmd{Cmd: readCmd, Output: mappings})
			fexec.AddFakeCmd(&ovntest.ExpectedCmd{Cmd: deleteCmd})
			fexec.AddFakeCmd(&ovntest.ExpectedCmd{Cmd: readCmd, Output: "physnet:breth0"})

			nc.Stop()
			if err := nc.Cleanup(); err == nil {
				t.Fatal("expected cleanup to report the failed OVS transaction")
			}
			if err := nc.Cleanup(); err != nil {
				t.Fatalf("cleanup retry failed: %v", err)
			}
			if err := nc.Cleanup(); err != nil {
				t.Fatalf("repeated cleanup failed: %v", err)
			}
			if !fexec.CalledMatchesExpected() {
				t.Fatalf("unexpected OVS commands during cleanup retry: %s", fexec.ErrorDesc())
			}
		})
	}
}

func TestLocalnetCleanupSoleBridgeMapping(t *testing.T) {
	for _, mode := range []string{types.NodeModeFull, types.NodeModeDPU} {
		t.Run(mode, func(t *testing.T) {
			nc, fexec := setupLocalnetLifecycleTest(t, mode)
			nc.bridgeName = "br-blue"
			const (
				readCmd   = "ovs-vsctl --timeout=15 --if-exists get Open_vSwitch . external_ids:ovn-bridge-mappings"
				removeCmd = "ovs-vsctl --timeout=15 remove Open_vSwitch . external_ids ovn-bridge-mappings"
			)
			// The localnet mapping is the only entry, so there is no map left to
			// set and the key itself has to be removed.
			fexec.AddFakeCmd(&ovntest.ExpectedCmd{Cmd: readCmd, Output: "blue_br-localnet:br-blue"})
			fexec.AddFakeCmd(&ovntest.ExpectedCmd{Cmd: removeCmd})
			fexec.AddFakeCmd(&ovntest.ExpectedCmd{Cmd: readCmd})

			nc.Stop()
			if err := nc.Cleanup(); err != nil {
				t.Fatalf("cleanup sole localnet mapping: %v", err)
			}
			if err := nc.Cleanup(); err != nil {
				t.Fatalf("repeated cleanup failed: %v", err)
			}
			if !fexec.CalledMatchesExpected() {
				t.Fatalf("unexpected OVS commands during cleanup: %s", fexec.ErrorDesc())
			}
		})
	}
}

func TestLocalnetCleanupSkipsUnmanagedBridgeMapping(t *testing.T) {
	for _, mode := range []string{types.NodeModeFull, types.NodeModeDPU, types.NodeModeDPUHost} {
		t.Run(mode, func(t *testing.T) {
			nc, fexec := setupLocalnetLifecycleTest(t, mode)
			if mode == types.NodeModeDPUHost {
				nc.bridgeName = "br-blue"
			}
			nc.Stop()
			if err := nc.Cleanup(); err != nil {
				t.Fatalf("cleanup unmanaged localnet mapping: %v", err)
			}
			if !fexec.CalledMatchesExpected() {
				t.Fatalf("unexpected OVS commands for unmanaged mapping: %s", fexec.ErrorDesc())
			}
		})
	}
}
