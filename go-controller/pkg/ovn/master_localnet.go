package ovn

import (
	"fmt"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/libovsdbops"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/nbdb"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/types"
	"net"

	"k8s.io/klog/v2"
	utilnet "k8s.io/utils/net"
)

// SetupLocalnetMaster creates localnet switch for the network
func (oc *Controller) SetupLocalnetMaster() error {
	switchName := oc.nadInfo.Prefix + types.OVNLocalnetSwitch

	// Add external interface as a logical port to external_switch.
	// This is a learning switch port with "unknown" address. The external
	// world is accessed via this port.
	logicalSwitchPort := nbdb.LogicalSwitchPort{
		Addresses: []string{"unknown"},
		Type:      "localnet",
		Options: map[string]string{
			"network_name": oc.nadInfo.Prefix + types.LocalNetBridgeName,
		},
		Name: oc.nadInfo.Prefix + types.OVNLocalnetPort,
	}
	if oc.nadInfo.VlanId != 0 {
		intVlanID := int(oc.nadInfo.VlanId)
		logicalSwitchPort.TagRequest = &intVlanID
	}

	// Create a single common switch for the cluster.
	logicalSwitch := nbdb.LogicalSwitch{Name: switchName}
	if oc.nadInfo.IsSecondary {
		logicalSwitch.ExternalIDs = map[string]string{"network_name": oc.nadInfo.NetName}
	}

	for _, subnet := range oc.clusterSubnets {
		hostSubnet := subnet.CIDR
		if utilnet.IsIPv6CIDR(hostSubnet) {
			logicalSwitch.OtherConfig = map[string]string{"ipv6_prefix": hostSubnet.IP.String()}
		} else {
			//mgmtIfAddr := util.GetNodeManagementIfAddr(hostSubnet)
			//excludeIPs := mgmtIfAddr.IP.String()
			logicalSwitch.OtherConfig = map[string]string{"subnet": hostSubnet.String()}
		}
	}

	// TBD other-config:mcast_snoop, other-config:mcast_querier etc.
	err := libovsdbops.CreateOrUpdateLogicalSwitchPortsAndSwitch(oc.mc.nbClient, &logicalSwitch, &logicalSwitchPort)
	if err != nil {
		return fmt.Errorf("failed to create logical switch ports %+v and switch %s: %v",
			logicalSwitchPort.Name, logicalSwitch.Name, err)
	}

	var hostSubnets []*net.IPNet
	for _, subnet := range oc.clusterSubnets {
		hostSubnet := subnet.CIDR
		hostSubnets = append(hostSubnets, hostSubnet)
	}
	err = oc.lsManager.AddNode(switchName, logicalSwitch.UUID, hostSubnets)
	if err != nil {
		return fmt.Errorf("failed to initialize localnet switch IP manager for network %s: %v", oc.nadInfo.NetName, err)
	}
	for _, excludeIP := range oc.nadInfo.ExcludeIPs {
		var ipMask net.IPMask
		if excludeIP.To4() != nil {
			ipMask = net.CIDRMask(32, 32)
		} else {
			ipMask = net.CIDRMask(128, 128)
		}

		_ = oc.lsManager.AllocateIPs(switchName, []*net.IPNet{{IP: excludeIP, Mask: ipMask}})
	}
	return nil
}

// deleteLocalnetMaster delete localnet switch for the network
func (oc *Controller) deleteLocalnetMaster() {
	switchName := oc.nadInfo.Prefix + types.OVNLocalnetSwitch
	if err := libovsdbops.DeleteLogicalSwitch(oc.mc.nbClient, switchName); err != nil {
		klog.Errorf("Failed to delete logical switch %s: %v", switchName, err)
	}
}
