package node

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	mlxdevm "github.com/Mellanox/mlxdevm-go"
	utilfs "github.com/Mellanox/sriovnet/pkg/utils/filesystem"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"
)

const (
	PcidevPrefix       = "device"
	NetSysDir          = "/sys/class/net"
	AuxDevDir          = "/sys/bus/auxiliary/devices"
	NetdevPhysPortName = "phys_port_name"
	// Needs to be in sync with the limit in the API spec
	MaxSFNum = 5
)

// Regex that matches on VF representor port name
var vfPortRepRegex = regexp.MustCompile(`^(?:c\d+)?pf(\d+)vf(\d+)$`)

type sfDetails struct {
	portIndex       uint32
	uplinkPhysPort  string
	sfRepName       string
	mirrorCount     int
	sfNetDeviceName string
	// portMirrorInUse is map of portmirror names
	// that use this SF
	portMirrorInUse map[string]bool
	sfNum           uint32
	// mirroredToPod is sinkPod name (podNamespace/podName)
	// which has created the SF.
	mirroredToPod string
}

// getNextAvailableSFPortNum returns next available sfportNum and mark it as used.
// if all the allocated SF's for portmirror are used up, it returns MaxSFNum
func (bnnc *BaseNodeNetworkController) getNextAvailableSFPortNum(startSFNum uint32, reqSFNum int) uint32 {
	bnnc.sfPortNumberMutex.Lock()
	defer bnnc.sfPortNumberMutex.Unlock()

	if reqSFNum >= 0 {
		if _, ok := bnnc.usedSFPortNumMap[uint32(reqSFNum)]; !ok {
			bnnc.usedSFPortNumMap[uint32(reqSFNum)] = true
			return uint32(reqSFNum)
		}
		return uint32(MaxSFNum)
	}

	var i uint32
	for i = startSFNum; i < uint32(MaxSFNum); i++ {
		if _, ok := bnnc.usedSFPortNumMap[i]; !ok {
			bnnc.usedSFPortNumMap[i] = true
			return i
		}
	}
	return i
}

func (bnnc *BaseNodeNetworkController) releaseSFPortNum(sfNum uint32) {
	bnnc.sfPortNumberMutex.Lock()
	defer bnnc.sfPortNumberMutex.Unlock()

	delete(bnnc.usedSFPortNumMap, sfNum)
}

func (bnnc *BaseNodeNetworkController) setSFPortNum(sfNum uint32) {
	bnnc.sfPortNumberMutex.Lock()
	defer bnnc.sfPortNumberMutex.Unlock()

	bnnc.usedSFPortNumMap[sfNum] = true
}

func setSFState(pciAddress string, portIndex uint32, setUnset uint8) error {
	var portFn mlxdevm.DevlinkPortFn
	var fnAttrs mlxdevm.DevlinkPortFnSetAttrs

	portFn.State = setUnset

	fnAttrs.StateValid = true
	fnAttrs.FnAttrs = portFn
	err := mlxdevm.DevlinkPortFnSet("devlink", "pci", pciAddress, portIndex, fnAttrs)
	if err != nil {
		return fmt.Errorf("error setting state to %d on %s/%d", setUnset, pciAddress, portIndex)
	}
	return nil
}

// these needs to be invoked via mlxdevm-go mellanox lib; for now exec is used
func (bnnc *BaseNodeNetworkController) createSF(pciAddress string, pfNum uint16, sfNum uint32) (*mlxdevm.DevlinkPort, error) {
	var portAttr mlxdevm.DevlinkPortAddAttrs

	portAttr.PfNumber = pfNum
	portAttr.SfNumber = sfNum
	portAttr.SfNumberValid = true

	// To use upstream devlink interface
	dl_port, err := mlxdevm.DevlinkPortAdd("devlink", "pci", pciAddress, mlxdevm.DEVLINK_PORT_FLAVOUR_PCI_SF, portAttr)
	if err != nil {
		return nil, fmt.Errorf("error creating an SF for %s/%d/%d: (%v)", pciAddress, pfNum, sfNum, err)
	}
	sfIndex := fmt.Sprintf("pci/%s/%d", pciAddress, dl_port.PortIndex)
	// Set trust to on : need to come from config; This is needed
	// for some application such as FI, but maybe not all
	err = setSFState(pciAddress, dl_port.PortIndex, 0)
	if err != nil {
		return nil, fmt.Errorf("error setting SF %s to inactive: (%v)", sfIndex, err)
	}

	cmd := exec.Command("/opt/mellanox/iproute2/sbin/mlxdevm", "port", "function", "set", sfIndex, "trust", "on")
	err = cmd.Run()
	if err != nil {
		return nil, fmt.Errorf("error setting trust mode for SF %s: (%v)", sfIndex, err)
	}

	err = setSFState(pciAddress, dl_port.PortIndex, 1)
	if err != nil {
		return nil, fmt.Errorf("error setting SF %s to active: (%v)", sfIndex, err)
	}

	return dl_port, nil
}

// Unbind from SF and bind to mlx
func bindAndUnbindSF(auxDev string) error {
	cmdStr := fmt.Sprintf("echo %s > /sys/bus/auxiliary/drivers/mlx5_core.sf_cfg/unbind", auxDev)
	cmd := exec.Command("bash", "-c", cmdStr)
	err := cmd.Run()
	if err != nil {
		klog.Errorf("Failed to unbind for auxdev %s: %v", auxDev, err)
		return err
	}

	cmdStr = fmt.Sprintf("echo %s > /sys/bus/auxiliary/drivers/mlx5_core.sf/bind", auxDev)
	cmd = exec.Command("bash", "-c", cmdStr)
	err = cmd.Run()
	if err != nil {
		klog.Errorf("Failed to bind for auxdev %s: %v", auxDev, err)
		return err
	}
	return nil
}

func (bnnc *BaseNodeNetworkController) deleteSF(sfUplinkPort string, portIndex, sfNum uint32) error {
	uplinkPCIAddress, err := getPCIFromDeviceName(sfUplinkPort)
	if err != nil {
		return fmt.Errorf("failed to get PCI address for SF's uplink port %s: (%v)",
			sfUplinkPort, err)
	}
	err = mlxdevm.DevlinkPortDel("devlink", "pci", uplinkPCIAddress, portIndex)
	if err != nil {
		return fmt.Errorf("failed to delete SF %d", portIndex)
	}

	bnnc.releaseSFPortNum(sfNum)
	return nil
}

// Hack
// sys/class/net/p0/subsystem/en3f0pf0sf0/phys_port_name
// sys/class/net/p0/subsystem/<rep name>/phys_port_name == pf<physport no.>sf<sf num>
// PF0SFx (e.g. pf0sf0)
func getSFRepPort(pfPort uint16, sfNum int32) (string, error) {
	physPortName := fmt.Sprintf("pf%dsf%d", pfPort, sfNum)
	upLink := fmt.Sprintf("p%d", pfPort)

	deviceListPath := filepath.Join(NetSysDir, upLink, "subsystem")
	devices, err := utilfs.Fs.ReadDir(deviceListPath)
	if err != nil {
		return "", err
	}
	for _, device := range devices {
		physPortFile := filepath.Join(NetSysDir, upLink, "subsystem", device.Name(), "phys_port_name")
		phyName, err := utilfs.Fs.ReadFile(physPortFile)
		if err != nil {
			continue
		}
		if strings.TrimSuffix(string(phyName), "\n") == physPortName {
			return device.Name(), err
		}
	}
	return "", fmt.Errorf("error getting rep for %d/%d", pfPort, sfNum)
}

func getSFAuxDev(sfnum uint32) (string, error) {
	devices, err := utilfs.Fs.ReadDir(AuxDevDir)
	if err != nil {
		return "", err
	}
	for _, device := range devices {
		sfNumFile := filepath.Join(AuxDevDir, device.Name(), "sfnum")
		devSFNum, err := utilfs.Fs.ReadFile(sfNumFile)
		if err != nil {
			continue
		}
		if strings.TrimSuffix(string(devSFNum), "\n") == strconv.FormatUint(uint64(sfnum), 10) {
			return device.Name(), nil
		}
	}
	return "", fmt.Errorf("error getting aux dev for SF %d", sfnum)
}

func getSFNetDeviceName(pfPort uint16, sfnum uint32) (string, error) {
	upLink := fmt.Sprintf("p%d", pfPort)

	deviceListPath := filepath.Join(NetSysDir, upLink, "subsystem")
	devices, err := utilfs.Fs.ReadDir(deviceListPath)
	if err != nil {
		return "", err
	}
	for _, device := range devices {
		sfNumFile := filepath.Join(deviceListPath, device.Name(), "device", "sfnum")
		devSFNum, err := utilfs.Fs.ReadFile(sfNumFile)
		if err != nil {
			continue
		}
		if strings.TrimSuffix(string(devSFNum), "\n") == strconv.FormatUint(uint64(sfnum), 10) {
			return device.Name(), nil
		}
	}
	return "", fmt.Errorf("error getting net dev for SF %d", sfnum)
}

// From sriovnet, ideally should export from the lib and use it here.
func readPCIsymbolicLink(symbolicLink string) (string, error) {
	pciDevDir, err := os.Readlink(symbolicLink)
	//nolint:gomnd
	if len(pciDevDir) <= 3 {
		return "", fmt.Errorf("could not find PCI Address")
	}

	return pciDevDir[9:], err
}

func getPCIFromDeviceName(netdevName string) (string, error) {
	symbolicLink := filepath.Join(NetSysDir, netdevName, PcidevPrefix)
	pciAddress, err := readPCIsymbolicLink(symbolicLink)
	if err != nil {
		err = fmt.Errorf("%v for netdevice %s", err, netdevName)
	}
	return pciAddress, err
}

func getNetDevPhysPortName(netDev string) (string, error) {
	devicePortNameFile := filepath.Join(NetSysDir, netDev, NetdevPhysPortName)
	physPortName, err := utilfs.Fs.ReadFile(devicePortNameFile)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(physPortName)), nil
}

func getUplinkPhysPortInfo(netdev string) (string, uint16, error) {
	portName, err := getNetDevPhysPortName(netdev)
	if err != nil {
		return "", 0, err
	}
	// Extract port num
	portNum := vfPortRepRegex.FindStringSubmatch(portName)
	if len(portNum) < 2 {
		return "", 0, fmt.Errorf("failed to extract physical port number from port name %s of netdev %s",
			portName, netdev)
	}
	portNumInt, err := strconv.ParseInt(portNum[1], 10, 64)
	if err != nil {
		return "", 0, fmt.Errorf("failed to get phys port from %s: (%v)", portNum[1], err)
	}
	uplinkPhysPortName := "p" + portNum[1]
	return uplinkPhysPortName, uint16(portNumInt), nil
}

// createSFWithRetry retries creation of SF with next sf number
// if there is already one with current sf num.
func (bnnc *BaseNodeNetworkController) createSFWithRetry(uplinkPCIAddress string, sfUplinkPortNum uint16, reqSFNum int) (*mlxdevm.DevlinkPort, uint32, error) {
	var startSFNum uint32 = 0
	for {
		// get the next available SF port number
		mirrorSFNum := bnnc.getNextAvailableSFPortNum(startSFNum, reqSFNum)
		if mirrorSFNum == uint32(MaxSFNum) {
			var err error
			if reqSFNum >= 0 {
				err = fmt.Errorf("requested SF num %d is not available", reqSFNum)
			} else {
				err = fmt.Errorf("all available SF's for portmirror are used up")
			}
			return nil, uint32(MaxSFNum), err
		}

		dlPort, err := bnnc.createSF(uplinkPCIAddress, sfUplinkPortNum, mirrorSFNum)
		if err != nil {
			// check if sf already exists(through file exists error), if sf already exists retry sf creation
			// with next number. Else, return an error

			if strings.Contains(err.Error(), "file exists") {
				// if requested SF num already exists, just return error.
				if reqSFNum >= 0 {
					bnnc.releaseSFPortNum(mirrorSFNum)
					return nil, uint32(MaxSFNum), fmt.Errorf("requested SF num %d already exists", reqSFNum)
				}

				// update startSFNum
				startSFNum = mirrorSFNum + 1
				bnnc.releaseSFPortNum(mirrorSFNum)
				klog.Infof("SF with PCIaddress %s and PF num %d and sf number %d already exists",
					uplinkPCIAddress, sfUplinkPortNum, mirrorSFNum)
				continue
			} else {
				bnnc.releaseSFPortNum(mirrorSFNum)
				err := fmt.Errorf("failed to create SF with PCIaddress %s and PF num %d and sf number %d :(%v)",
					uplinkPCIAddress, sfUplinkPortNum, mirrorSFNum, err)
				return nil, uint32(MaxSFNum), err
			}
		}
		return dlPort, mirrorSFNum, nil
	}
}

// getSFInfo creates a SF and does the binding and unbinding stuff and
// populates the sfInfo struct with corresponding sfdetails
func (bnnc *BaseNodeNetworkController) getSFInfo(sfUplinkPort string, sfUplinkPortNum uint16, reqSFNum int) (*sfDetails, error) {
	uplinkPCIAddress, err := getPCIFromDeviceName(sfUplinkPort)
	if err != nil {
		return nil, fmt.Errorf("failed to get PCI address for SF's uplink port %s: (%v)",
			sfUplinkPort, err)
	}

	dlPort, mirrorSFNum, err := bnnc.createSFWithRetry(uplinkPCIAddress, sfUplinkPortNum, reqSFNum)
	if err != nil {
		return nil, err
	}

	var auxDevice string
	// wait for some time, if needed
	start := time.Now()
	if err := wait.PollUntilContextTimeout(context.Background(), 500*time.Millisecond, 5*time.Second, true, func(ctx context.Context) (bool, error) {
		var errRet error
		auxDevice, errRet = getSFAuxDev(mirrorSFNum)
		if errRet != nil {
			klog.Infof("Error at iteration %f seconds for SF %d while getting aux device: %v",
				time.Since(start).Seconds(), mirrorSFNum, errRet)
			return false, nil
		}
		return true, nil
	}); err != nil {
		_ = bnnc.deleteSF(sfUplinkPort, dlPort.PortIndex, mirrorSFNum)
		return nil, fmt.Errorf("failed to get aux device details for sf %d: (%v)", mirrorSFNum, err)
	}

	err = bindAndUnbindSF(auxDevice)
	if err != nil {
		_ = bnnc.deleteSF(sfUplinkPort, dlPort.PortIndex, mirrorSFNum)
		return nil, fmt.Errorf("failed to bind and unbind auxDevice %s: (%v)", auxDevice, err)
	}
	start = time.Now()
	var repName string
	// wait for some time, if needed to get the netdev name and SFrep name
	if err := wait.PollUntilContextTimeout(context.Background(), 500*time.Millisecond, 5*time.Second, true, func(ctx context.Context) (bool, error) {
		var errRet error
		repName, errRet = getSFRepPort(sfUplinkPortNum, int32(mirrorSFNum))
		if errRet != nil {
			klog.Infof("Interation at %fs; failed to get SF representor name for SF with pfnumber %d and sfnumber %d: %v",
				sfUplinkPortNum, mirrorSFNum, errRet)
			return false, nil
		}
		return true, nil
	}); err != nil {
		_ = bnnc.deleteSF(sfUplinkPort, dlPort.PortIndex, mirrorSFNum)
		return nil, fmt.Errorf("failed to get SF representor name for SF with pfnumber %d and sfnumber %d :(%v)",
			sfUplinkPortNum, mirrorSFNum, err)
	}

	// get sf netdeviceName
	netDevName, err := getSFNetDeviceName(sfUplinkPortNum, mirrorSFNum)
	if err != nil {
		_ = bnnc.deleteSF(sfUplinkPort, dlPort.PortIndex, mirrorSFNum)
		return nil, fmt.Errorf("failed to get netdevice name for SF with pfnumber %d and sfnumber %d: (%v)",
			sfUplinkPortNum, mirrorSFNum, err)
	}

	sfInfo := &sfDetails{
		portIndex:       dlPort.PortIndex,
		sfRepName:       repName,
		sfNetDeviceName: netDevName,
		portMirrorInUse: make(map[string]bool),
		sfNum:           mirrorSFNum,
	}
	return sfInfo, nil
}
