package ovn

import (
	"fmt"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/types"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util"

	"k8s.io/klog/v2"
	utilnet "k8s.io/utils/net"
)

// SetupLocalnetMaster creates localnet switch for the network
func (oc *Controller) SetupLocalnetMaster() error {
	switchName := oc.nadInfo.Prefix + types.OVNLocalnetSwitch
	// Create a single common switch for the cluster.
	lsArgs := []string{"--", "--may-exist", "ls-add", switchName,
		"--", "set", "logical_switch", switchName, "external_ids:network_name=" + oc.nadInfo.NetName}

	for _, subnet := range oc.clusterSubnets {
		hostSubnet := subnet.CIDR
		if utilnet.IsIPv6CIDR(hostSubnet) {
			lsArgs = append(lsArgs,
				"other-config:ipv6_prefix="+hostSubnet.IP.String(),
			)
		} else {
			//mgmtIfAddr := util.GetNodeManagementIfAddr(hostSubnet)
			//excludeIPs := mgmtIfAddr.IP.String()
			lsArgs = append(lsArgs,
				"other-config:subnet="+hostSubnet.String(),
			)
		}
	}

	// TBD other-config:mcast_snoop, other-config:mcast_querier etc. see
	stdout, stderr, err := util.RunOVNNbctl(lsArgs...)
	if err != nil {
		klog.Errorf("Failed to create a common localnet switch for network %s, "+
			"stdout: %q, stderr: %q, error: %v", oc.nadInfo.NetName, stdout, stderr, err)
		return err
	}

	// Add external interface as a logical port to external_switch.
	// This is a learning switch port with "unknown" address. The external
	// world is accessed via this port.
	portName := oc.nadInfo.Prefix + types.OVNLocalnetPort
	cmdArgs := []string{
		"--", "--may-exist", "lsp-add", switchName, portName,
		"--", "lsp-set-addresses", portName, "unknown",
		"--", "lsp-set-type", portName, "localnet",
		"--", "lsp-set-options", portName, "network_name=" + oc.nadInfo.Prefix + types.LocalNetBridgeName}

	if oc.nadInfo.VlanId != 0 {
		lspArgs := []string{
			"--", "set", "logical_switch_port", portName,
			fmt.Sprintf("tag_request=%d", oc.nadInfo.VlanId),
		}
		cmdArgs = append(cmdArgs, lspArgs...)
	}

	stdout, stderr, err = util.RunOVNNbctl(cmdArgs...)
	if err != nil {
		return fmt.Errorf("failed to add logical port %s to switch %s, stdout: %q, "+
			"stderr: %q, error: %v", portName, switchName, stdout, stderr, err)
	}

	return nil
}

// deleteLocalnetMaster delete localnet switch for the network
func (oc *Controller) deleteLocalnetMaster() {
	switchName := oc.nadInfo.Prefix + types.OVNLocalnetSwitch
	lsArgs := []string{"--if-exist", "ls-del", switchName}
	stdout, stderr, err := util.RunOVNNbctl(lsArgs...)
	if err != nil {
		klog.Errorf("Failed to delete logical switch %s, stdout: %q, "+
			"stderr: %q, error: %v", switchName, stdout, stderr, err)
	}
}
