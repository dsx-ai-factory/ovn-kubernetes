// SPDX-FileCopyrightText: Copyright The OVN-Kubernetes Contributors
// SPDX-License-Identifier: Apache-2.0

//go:build linux
// +build linux

package util

import (
	"crypto/sha256"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"

	"github.com/k8snetworkplumbingwg/sriovnet"
	"github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/config"
	"k8s.io/klog/v2"
)

// DPU operations abstraction.
//
// DPUOps is the central interface that hides DPU operational details
// (SR-IOV, switchdev, sysfs) behind a uniform API. All DPU and DPU Host
// mode code should go through GetDPUOps() rather than calling SriovnetOps
// directly.
//
// Two concrete implementations exist today:
//   - SwitchdevDPUOps - SR-IOV / switchdev hardware (NVIDIA BlueField, etc.)
//   - SimulatedDPUOps - simulated DPU environments (Kind, VMs with virtio)
//
// The singleton is selected by the --simulate-dpu configuration flag.
// When the flag is absent (the default), SwitchdevDPUOps is used.
type DPUOps interface {
	// GetDPUHostInterface returns the host representor interface attached to bridge
	// On switchdev hardware this discovers the VF/SF representor via sriovnet.
	// On simulated platforms this is either a veth peer or virtio interface.
	GetDPUHostInterface(bridgeName string) (string, error)

	// GetHostGatewayMACAddress returns the MAC address of the host-side
	// interface that corresponds to the DPU-side Host representor.
	// nodeName is the K8s node name of the host this DPU operates behalf of.
	GetHostGatewayMACAddress(bridgeName, nodeName string) (net.HardwareAddr, error)

	// ResolveDeviceDetails probes a netdev (e.g. VF) interface and returns device
	// details including PF and VF indices. In simulation this follows the
	// pattern <prefix><pfId>-<funcId> (e.g. "eth0-1" → PfId=0, FuncId=1).
	ResolveDeviceDetails(netdevName string) (*NetworkDeviceDetails, error)

	// GetPortRepresentor finds the DPU-side representor (VF representor in the case of switchdev hardware)
	// for the given PF and function indices. On simulation this follows the
	// pattern rep<pfId>-<funcId> (e.g. "rep0-1").
	GetPortRepresentor(pfId, funcId string) (string, error)

	// GetDeviceAddress returns an opaque, platform-specific identifier for
	// a representor interface. On switchdev hardware this is a PCI address
	// (e.g. "0000:01:00.2"); on simulated platforms it is the netdev name
	// itself.
	GetDeviceAddress(repName string) string
}

// ---------------------------------------------------------------------------
// DPUOps singleton
// ---------------------------------------------------------------------------

var dpuOps DPUOps

// GetDPUOps returns the current DPUOps singleton. If the singleton has not
// been initialised, it defaults to SwitchdevDPUOps (SR-IOV / switchdev hardware).
func GetDPUOps() DPUOps {
	if dpuOps == nil {
		if config.IsModeDPU() || config.IsModeDPUHost() {
			if config.OvnKubeNode.SimulateDPU {
				dpuOps = &SimulatedDPUOps{}
				klog.Infof("DPUOps initialised: simulated DPU environment")
				return dpuOps
			}
		}
		dpuOps = &SwitchdevDPUOps{}
	}
	return dpuOps
}

// ---------------------------------------------------------------------------
// SwitchdevDPUOps - SR-IOV / switchdev hardware (NVIDIA BlueField, etc.)
// ---------------------------------------------------------------------------

type SwitchdevDPUOps struct{}

func (n *SwitchdevDPUOps) GetDPUHostInterface(bridgeName string) (string, error) {
	portsToInterfaces, err := getBridgePortsInterfaces(bridgeName)
	if err != nil {
		return "", err
	}

	for _, ifaces := range portsToInterfaces {
		for _, iface := range ifaces {
			stdout, stderr, err := RunOVSVsctl("get", "Interface", strings.TrimSpace(iface), "Name")
			if err != nil {
				return "", fmt.Errorf("failed to get Interface %q Name on bridge %q:, stderr: %q, error: %v",
					iface, bridgeName, stderr, err)

			}
			flavor, err := GetSriovnetOps().GetRepresentorPortFlavour(stdout)
			if err == nil && flavor == sriovnet.PORT_FLAVOUR_PCI_PF {
				// host representor interface found
				return stdout, nil
			}
			continue
		}
	}
	// No host interface found in provided bridge
	return "", fmt.Errorf("dpu host interface was not found for bridge %q", bridgeName)
}

func (n *SwitchdevDPUOps) GetHostGatewayMACAddress(bridgeName, _ string) (net.HardwareAddr, error) {
	hostRep, err := n.GetDPUHostInterface(bridgeName)
	if err != nil {
		return nil, err
	}
	return GetSriovnetOps().GetRepresentorPeerMacAddress(hostRep)
}

func (n *SwitchdevDPUOps) ResolveDeviceDetails(netdevName string) (*NetworkDeviceDetails, error) {
	deviceID, err := GetDeviceIDFromNetdevice(netdevName)
	if err != nil {
		return nil, fmt.Errorf("failed to read sysfs device link for %s: %v", netdevName, err)
	}
	return GetNetworkDeviceDetails(deviceID)
}

func (n *SwitchdevDPUOps) GetPortRepresentor(pfId, funcId string) (string, error) {
	return GetSriovnetOps().GetVfRepresentorDPU(pfId, funcId)
}

func (n *SwitchdevDPUOps) GetRepresentorForPod(pfId, vfId string) (string, error) {
	return GetSriovnetOps().GetVfRepresentorDPU(pfId, vfId)
}

func (n *SwitchdevDPUOps) GetDeviceAddress(repName string) string {
	addr, err := GetSriovnetOps().GetPCIFromDeviceName(repName)
	if err != nil {
		klog.Warningf("Failed to get PCI address for %s: %v, using device name", repName, err)
		return repName
	}
	return addr
}

// ---------------------------------------------------------------------------
// SimulatedDPUOps - simulated DPU environments (Kind containers, VMs)
//
// Uses interface naming conventions instead of sysfs / switchdev:
//   - Host interfaces: <prefix><pfId>-<funcId>  (e.g. eth0-1)
//   - DPU representors: rep<pfId>-<funcId>      (e.g. rep0-1)
// ---------------------------------------------------------------------------

type SimulatedDPUOps struct{}

// Update when simulation code uses different constants
const (
	SimulationHostGatewayInterface      = "eth0-0"
	SimulationHostGatewayInterfaceIndex = 0
	SimulationHostGatewayPeerInterface  = "rep0-0"
)

var reSimulationNetdevFunc = regexp.MustCompile(`(\d+)-(\d+)$`)

// generateMACForHostToDpu returns a deterministic MAC for a host-to-DPU data
// interface. The hash is over nodeName + role("host" or "dpu"); the index is
// encoded in the last octet so each channel in a pair has a unique MAC.
// OUI 52:54:00 is commonly used for QEMU/virtio and marks the address as
// locally administered.
func (s *SimulatedDPUOps) generateMACForHostToDpu(nodeName, role string, index int) string {
	h := sha256.Sum256([]byte(nodeName + "\x00" + role))
	return fmt.Sprintf("52:54:00:%02x:%02x:%02x", h[0], h[1], index&0xff)
}

// getDPURepresentor builds rep<pfId>-<funcId> and verifies the link exists.
func (s *SimulatedDPUOps) getDPURepresentor(pfId, funcId string) (string, error) {
	rep := fmt.Sprintf("rep%s-%s", pfId, funcId)
	if _, err := GetNetLinkOps().LinkByName(rep); err != nil {
		return "", fmt.Errorf("simulated representor %s not found: %v", rep, err)
	}
	return rep, nil
}

func (s *SimulatedDPUOps) GetDPUHostInterface(bridgeName string) (string, error) {
	return SimulationHostGatewayPeerInterface, nil
}

func (s *SimulatedDPUOps) GetHostGatewayMACAddress(_, nodeName string) (net.HardwareAddr, error) {
	if nodeName == "" {
		return nil, fmt.Errorf("nodeName must be provided for simulated GetHostGatewayMACAddress")
	}

	// TODO: This identifies a need to have an API to get reliable information from the host (requested by the DPU)
	macStr := s.generateMACForHostToDpu(nodeName, "host", SimulationHostGatewayInterfaceIndex)
	mac, err := net.ParseMAC(macStr)
	if err != nil {
		return nil, fmt.Errorf("failed to parse generated MAC %s: %v", macStr, err)
	}

	klog.Infof("Derived host gateway peer MAC %s for node %s", mac, nodeName)
	return mac, nil
}

func (s *SimulatedDPUOps) ResolveDeviceDetails(netdevName string) (*NetworkDeviceDetails, error) {
	matches := reSimulationNetdevFunc.FindStringSubmatch(netdevName)
	if len(matches) != 3 {
		return nil, fmt.Errorf("interface %s does not match simulated naming pattern *<pfId>-<funcId>", netdevName)
	}
	pfId, err := strconv.Atoi(matches[1])
	if err != nil {
		return nil, fmt.Errorf("failed to parse PF index from %q: %v", netdevName, err)
	}
	funcId, err := strconv.Atoi(matches[2])
	if err != nil {
		return nil, fmt.Errorf("failed to parse Function index from %q: %v", netdevName, err)
	}
	klog.Infof("Interface %s resolved as simulated netdev: PfId=%d, FuncId=%d", netdevName, pfId, funcId)
	return &NetworkDeviceDetails{
		DeviceId: netdevName,
		PfId:     pfId,
		FuncId:   funcId,
	}, nil
}

func (s *SimulatedDPUOps) GetPortRepresentor(pfId, funcId string) (string, error) {
	return s.getDPURepresentor(pfId, funcId)
}

func (s *SimulatedDPUOps) GetDeviceAddress(repName string) string {
	return repName
}
