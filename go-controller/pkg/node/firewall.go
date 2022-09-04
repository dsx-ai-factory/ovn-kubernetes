package node

import (
	"fmt"
	"runtime"

	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util"
	kapi "k8s.io/api/core/v1"
)

const (
	ovnFirewallZone      = "ovn"
	ngnAdminFirewallZone = "ngn-admin"
	addInterface         = "add interface"
	addPort              = "add port"
	removePort           = "remove port"
)

//TODO(Hareesh): Temporary workaround till we add firewall support for DPU as well. Otherwise this fails for DPU.
func isARM() bool {
	// This is not the right way to detect platform since this only tests what the binary was compiled for.
	// But this is a good substitute for our needs since full firewall support for DPU is going to be added.
	return runtime.GOARCH == "arm64"
}

func changeFirewallConfig(cmdArgs []string, action string) error {
	if isARM() {
		return nil
	}
	// apply changes to runtime firewall config
	_, stderr, err := util.RunFirewallCmd(cmdArgs...)
	if err != nil {
		return fmt.Errorf("failed to %s in ovn firewall zone "+
			"in runtime firewall config, stderr (%s) :(%v)", action, stderr, err)
	}
	// apply changes to permanent firewall config
	cmdArgs = append(cmdArgs, "--permanent")
	_, stderr, err = util.RunFirewallCmd(cmdArgs...)
	if err != nil {
		return fmt.Errorf("failed to %s in ovn firewall zone "+
			"in permanent firewall config, stderr (%s) :(%v)", action, stderr, err)
	}
	return nil
}

func addInterfaceToFirewallZone(interfaceName, zoneName string) error {
	if isARM() {
		return nil
	}
	cmdArgs := []string{
		fmt.Sprintf("--zone=%s", zoneName),
		fmt.Sprintf("--change-interface=%s", interfaceName),
	}

	err := changeFirewallConfig(cmdArgs, addInterface)
	if err != nil {
		return err
	}

	return nil
}

func firewallPortExists(zoneName string, port int32, protocol string) (bool, error) {
	if isARM() {
		return false, nil
	}
	cmdArgs := []string{
		fmt.Sprintf("--zone=%s", zoneName),
		fmt.Sprintf("--query-port=%d/%s", port, protocol),
	}

	stdout, stderr, err := util.RunFirewallCmd(cmdArgs...)
	if err != nil {
		if stdout == "no" {
			return false, nil
		} else {
			return false, fmt.Errorf("failed to query port %d from %s zone "+
				"stderr:(%s): (%v)", port, zoneName, stderr, err)
		}
	}

	if stdout == "yes" {
		return true, nil
	}
	return false, nil
}

func addPortToFirewallZone(zoneName string, port int32, protocol kapi.Protocol) error {
	if isARM() {
		return nil
	}
	var portType, portArgs string
	if protocol == kapi.ProtocolTCP {
		portArgs = fmt.Sprintf("--add-port=%d/tcp", port)
		portType = "tcp"
	} else if protocol == kapi.ProtocolUDP {
		portArgs = fmt.Sprintf("--add-port=%d/udp", port)
		portType = "udp"
	} else if protocol == kapi.ProtocolSCTP {
		portArgs = fmt.Sprintf("--add-port=%d/sctp", port)
		portType = "sctp"
	} else {
		return fmt.Errorf("not supported protocol type for firewall config")
	}

	exists, err := firewallPortExists(zoneName, port, portType)
	if err != nil {
		return err
	} else if exists {
		return nil
	}

	cmdArgs := []string{
		fmt.Sprintf("--zone=%s", zoneName),
		portArgs,
	}
	err = changeFirewallConfig(cmdArgs, addPort)
	if err != nil {
		return err
	}
	return nil
}

func removePortFromFirewallZone(zoneName string, port int32, protocol kapi.Protocol) error {
	if isARM() {
		return nil
	}
	var portType, portArgs string
	if protocol == kapi.ProtocolTCP {
		portArgs = fmt.Sprintf("--remove-port=%d/tcp", port)
		portType = "tcp"
	} else if protocol == kapi.ProtocolUDP {
		portArgs = fmt.Sprintf("--remove-port=%d/udp", port)
		portType = "udp"
	} else if protocol == kapi.ProtocolSCTP {
		portArgs = fmt.Sprintf("--remove-port=%d/sctp", port)
		portType = "sctp"
	} else {
		return fmt.Errorf("not supported protocol type for firewall config")
	}

	exists, err := firewallPortExists(zoneName, port, portType)
	if err != nil {
		return err
	} else if !exists {
		return nil
	}

	cmdArgs := []string{
		fmt.Sprintf("--zone=%s", zoneName),
		portArgs,
	}
	err = changeFirewallConfig(cmdArgs, removePort)
	if err != nil {
		return err
	}
	return nil
}
