//go:build linux
// +build linux

package util

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/k8snetworkplumbingwg/govdpa/pkg/kvdpa"
	"github.com/k8snetworkplumbingwg/sriovnet"
	utilfs "github.com/k8snetworkplumbingwg/sriovnet/pkg/utils/filesystem"
)

// Copied from vendor/github.com/Mellanox/sriovnet/sriovnet_switchdev.go
// can be removed once SetRepresentorVFMissPktRate moves to the same location.
const (
	NetSysDir = "/sys/class/net"
	PciSysDir = "/sys/bus/pci/devices"
)

type SriovnetOps interface {
	GetNetDevicesFromPci(pciAddress string) ([]string, error)
	GetNetDevicesFromAux(auxDev string) ([]string, error)
	GetUplinkRepresentor(vfPciAddress string) (string, error)
	GetUplinkRepresentorFromAux(auxDev string) (string, error)
	GetVfIndexByPciAddress(vfPciAddress string) (int, error)
	GetPfIndexByVfPciAddress(vfPciAddress string) (int, error)
	GetSfIndexByAuxDev(auxDev string) (int, error)
	GetVfRepresentor(uplink string, vfIndex int) (string, error)
	GetSfRepresentor(uplink string, sfIndex int) (string, error)
	GetPfPciFromVfPci(vfPciAddress string) (string, error)
	GetPfPciFromAux(auxDev string) (string, error)
	GetVfRepresentorDPU(pfID, vfIndex string) (string, error)
	GetRepresentorPeerMacAddress(netdev string) (net.HardwareAddr, error)
	SetRepresentorPeerMacAddress(netdev string, mac net.HardwareAddr) error
	GetRepresentorPortFlavour(netdev string) (sriovnet.PortFlavour, error)
	IsVfPciVfioBound(pciAddr string) bool
	SetRepresentorVFMissPktRate(netdev string, maxPPS, maxBurst uint) error
	GetRepresentorVFMissPktRate(netdev string) (uint64, uint64, error)
	GetRepresentorVFMissPktDrops(netdev string) (uint64, error)
}

type defaultSriovnetOps struct {
}

var sriovnetOps SriovnetOps = &defaultSriovnetOps{}

// SetSriovnetOpsInst method would be used by unit tests in other packages
func SetSriovnetOpsInst(mockInst SriovnetOps) {
	sriovnetOps = mockInst
}

// GetSriovnetOps will be invoked by functions in other packages that would need access to the sriovnet library methods.
func GetSriovnetOps() SriovnetOps {
	return sriovnetOps
}

func (defaultSriovnetOps) GetNetDevicesFromPci(pciAddress string) ([]string, error) {
	return sriovnet.GetNetDevicesFromPci(pciAddress)
}

func (defaultSriovnetOps) GetNetDevicesFromAux(auxDev string) ([]string, error) {
	return sriovnet.GetNetDevicesFromAux(auxDev)
}

func (defaultSriovnetOps) GetUplinkRepresentor(vfPciAddress string) (string, error) {
	return sriovnet.GetUplinkRepresentor(vfPciAddress)
}

func (defaultSriovnetOps) GetUplinkRepresentorFromAux(auxDev string) (string, error) {
	return sriovnet.GetUplinkRepresentorFromAux(auxDev)
}

func (defaultSriovnetOps) GetVfIndexByPciAddress(vfPciAddress string) (int, error) {
	return sriovnet.GetVfIndexByPciAddress(vfPciAddress)
}

func (defaultSriovnetOps) GetPfIndexByVfPciAddress(vfPciAddress string) (int, error) {
	return sriovnet.GetPfIndexByVfPciAddress(vfPciAddress)
}

func (defaultSriovnetOps) GetSfIndexByAuxDev(auxDev string) (int, error) {
	return sriovnet.GetSfIndexByAuxDev(auxDev)
}

func (defaultSriovnetOps) GetVfRepresentor(uplink string, vfIndex int) (string, error) {
	return sriovnet.GetVfRepresentor(uplink, vfIndex)
}

func (defaultSriovnetOps) GetSfRepresentor(uplink string, sfIndex int) (string, error) {
	return sriovnet.GetSfRepresentor(uplink, sfIndex)
}

func (defaultSriovnetOps) GetPfPciFromVfPci(vfPciAddress string) (string, error) {
	return sriovnet.GetPfPciFromVfPci(vfPciAddress)
}

func (defaultSriovnetOps) GetPfPciFromAux(auxDev string) (string, error) {
	return sriovnet.GetPfPciFromAux(auxDev)
}

func (defaultSriovnetOps) GetVfRepresentorDPU(pfID, vfIndex string) (string, error) {
	return sriovnet.GetVfRepresentorDPU(pfID, vfIndex)
}

func (defaultSriovnetOps) GetRepresentorPeerMacAddress(netdev string) (net.HardwareAddr, error) {
	return sriovnet.GetRepresentorPeerMacAddress(netdev)
}

func (defaultSriovnetOps) SetRepresentorPeerMacAddress(netdev string, mac net.HardwareAddr) error {
	return sriovnet.SetRepresentorPeerMacAddress(netdev, mac)
}

func (defaultSriovnetOps) GetRepresentorPortFlavour(netdev string) (sriovnet.PortFlavour, error) {
	return sriovnet.GetRepresentorPortFlavour(netdev)
}

// GetFunctionRepresentorName returns representor name for passed device ID. Supported devices are Virtual Function
// or Scalable Function
func GetFunctionRepresentorName(deviceID string) (string, error) {
	var rep, uplink string
	var err error
	var index int

	if IsPCIDeviceName(deviceID) { // PCI device
		uplink, err = GetSriovnetOps().GetUplinkRepresentor(deviceID)
		if err != nil {
			return "", err
		}
		index, err = GetSriovnetOps().GetVfIndexByPciAddress(deviceID)
		if err != nil {
			return "", err
		}
		rep, err = GetSriovnetOps().GetVfRepresentor(uplink, index)
	} else if IsAuxDeviceName(deviceID) { // Auxiliary device
		uplink, err = GetSriovnetOps().GetUplinkRepresentorFromAux(deviceID)
		if err != nil {
			return "", err
		}
		index, err = GetSriovnetOps().GetSfIndexByAuxDev(deviceID)
		if err != nil {
			return "", err
		}
		rep, err = GetSriovnetOps().GetSfRepresentor(uplink, index)
	} else {
		return "", fmt.Errorf("cannot determine device type for id '%s'", deviceID)
	}
	if err != nil {
		return "", err
	}
	return rep, nil
}

// GetNetdevsNameFromDeviceId returns the all netdevice names from the passed device ID.
func GetNetdevsNameFromDeviceId(deviceId string) ([]string, error) {
	if IsPCIDeviceName(deviceId) {
		// If a vDPA device exists, it takes preference over the vendor device, steering-wize
		vdpaDevice, err := GetVdpaOps().GetVdpaDeviceByPci(deviceId)
		if err == nil && vdpaDevice.Driver() == kvdpa.VirtioVdpaDriver {
			return []string{vdpaDevice.VirtioNet().NetDev()}, nil
		}

		return GetSriovnetOps().GetNetDevicesFromPci(deviceId)
	} else { // Auxiliary network device
		return GetSriovnetOps().GetNetDevicesFromAux(deviceId)
	}
}

// GetNetdevNameFromDeviceId returns the netdevice name from the passed device ID.
func GetNetdevNameFromDeviceId(deviceId string) (string, error) {
	netdevices, err := GetNetdevsNameFromDeviceId(deviceId)
	if err != nil {
		return "", err
	}

	// Make sure we have 1 netdevice per pci address
	numNetDevices := len(netdevices)
	if numNetDevices != 1 {
		return "", fmt.Errorf("failed to get one netdevice interface (count %d) per Device ID %s", numNetDevices, deviceId)
	}
	return netdevices[0], nil
}

func (defaultSriovnetOps) IsVfPciVfioBound(pciAddr string) bool {
	return sriovnet.IsVfPciVfioBound(pciAddr)
}

// Temporary location - will be moved to
// vendor/github.com/Mellanox/sriovnet/sriovnet_switchdev.go
// Populate the Representator's miss packet rate info.
// The usage is echo "max_pps max_pkt_burst" > /sys/class/net/<rep>/rep_config/miss_rl_cfg
// both max_pps and max_pkt_burst are needed
func (defaultSriovnetOps) SetRepresentorVFMissPktRate(netdev string, maxPPS, maxBurst uint) error {
	_, err := sriovnet.GetRepresentorPortFlavour(netdev)
	if err != nil {
		return fmt.Errorf("unknown port flavour for netdev %s. %v", netdev, err)
	}
	missPktRateInfo := fmt.Sprintf("%d %d", maxPPS, maxBurst)
	sysfsVfMissPktRateFile := filepath.Join(NetSysDir, netdev, "rep_config", "miss_rl_cfg")
	_, err = utilfs.Fs.Stat(sysfsVfMissPktRateFile)
	if err != nil {
		return fmt.Errorf("couldn't stat VF representor's sysfs file %s: %v", sysfsVfMissPktRateFile, err)
	}
	err = utilfs.Fs.WriteFile(sysfsVfMissPktRateFile, []byte(missPktRateInfo), 0)
	if err != nil {
		return fmt.Errorf("failed to write the miss packet rate %s to VF representator %s",
			missPktRateInfo, sysfsVfMissPktRateFile)
	}
	return nil
}

// Get the configured rate and Burst
func (defaultSriovnetOps) GetRepresentorVFMissPktRate(netdev string) (uint64, uint64, error) {
	sysfsVfMissPktRateFile := filepath.Join(NetSysDir, netdev, "rep_config", "miss_rl_cfg")
	_, err := utilfs.Fs.Stat(sysfsVfMissPktRateFile)
	if err != nil {
		if os.IsNotExist(err) {
			// connection rate-limiting is supported only on PF and VF representors.
			return 0, 0, nil
		}
		return 0, 0, fmt.Errorf("couldn't stat VF representor's sysfs file %s: %v", sysfsVfMissPktRateFile, err)
	}
	missPktRateInfo, err := utilfs.Fs.ReadFile(sysfsVfMissPktRateFile)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to read the miss packet rate info from VF representator %s",
			sysfsVfMissPktRateFile)
	}
	// The format is static
	// rate: 100[packes/s] burst: 250[packets]
	pktRateInfo := strings.Split(string(missPktRateInfo), " ")
	rateStr := strings.Split(pktRateInfo[1], "[")
	burstStr := strings.Split(pktRateInfo[3], "[")

	maxPPS, err := strconv.ParseUint(rateStr[0], 10, 32)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to convert rate from string %s to uint: %v",
			rateStr, err)
	}
	maxBurst, err := strconv.ParseUint(burstStr[0], 10, 32)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to convert rate from string %s to uint: %v",
			burstStr, err)
	}

	return maxPPS, maxBurst, nil
}

// Get the packets dropped due to exceeding the configured limit.
func (defaultSriovnetOps) GetRepresentorVFMissPktDrops(netdev string) (uint64, error) {
	sysfsVfMissPktDropFile := filepath.Join(NetSysDir, netdev, "rep_config", "miss_rl_dropped_packets")
	_, err := utilfs.Fs.Stat(sysfsVfMissPktDropFile)
	if err != nil {
		if os.IsNotExist(err) {
			// connection rate-limiting is supported only on PF and VF representors.
			return 0, nil
		}
		return 0, fmt.Errorf("couldn't stat VF representor's sysfs file %s: %v", sysfsVfMissPktDropFile, err)
	}
	missPktDropCount, err := utilfs.Fs.ReadFile(sysfsVfMissPktDropFile)
	if err != nil {
		return 0, fmt.Errorf("failed to read the miss packet drop info from VF representator %s",
			sysfsVfMissPktDropFile)
	}
	packetsDropped, err := strconv.ParseUint(strings.TrimSuffix(string(missPktDropCount), "\n"), 10, 32)
	if err != nil {
		return 0, fmt.Errorf("failed to convert drop count from string %s to uint: %v",
			missPktDropCount, err)
	}

	return packetsDropped, nil
}

// SetVFHardwreAddress sets mac address for a VF interface
func SetVFHardwreAddress(deviceID string, mac net.HardwareAddr) error {
	// get uplink netdevice name and its netlink object
	uplink, err := GetSriovnetOps().GetUplinkRepresentor(deviceID)
	if err != nil {
		return err
	}
	uplinkObj, err := GetNetLinkOps().LinkByName(uplink)
	if err != nil {
		return err
	}
	// get VF index from PCI
	vfIndex, err := GetSriovnetOps().GetVfIndexByPciAddress(deviceID)
	if err != nil {
		return err
	}
	// set MAC address through VF representor
	if err := GetNetLinkOps().LinkSetVfHardwareAddr(uplinkObj, vfIndex, mac); err != nil {
		return err
	}
	return nil
}
