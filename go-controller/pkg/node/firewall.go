package node

import (
	"fmt"

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

func changeFirewallConfig(cmdArgs []string, action string) error {
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
