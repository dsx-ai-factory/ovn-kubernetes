package ovn

import (
	"fmt"
	"net"

	"github.com/ovn-org/libovsdb/ovsdb"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/libovsdbops"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/nbdb"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/types"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util"

	"k8s.io/klog/v2"
	utilnet "k8s.io/utils/net"
)

// setupLayer2Switch creates the logical switch for the layer2/localnet network
func (oc *Controller) setupLayer2Switch(switchName string) error {
	logicalSwitch := nbdb.LogicalSwitch{
		Name: switchName,
	}
	if oc.nadInfo.IsSecondary {
		logicalSwitch.ExternalIDs = map[string]string{"network_name": oc.nadInfo.NetName}
	}

	var hostSubnets []*net.IPNet
	for _, subnet := range oc.clusterSubnets {
		hostSubnet := subnet.CIDR
		hostSubnets = append(hostSubnets, hostSubnet)
		if utilnet.IsIPv6CIDR(hostSubnet) {
			logicalSwitch.OtherConfig = map[string]string{"ipv6_prefix": hostSubnet.IP.String()}
		} else {
			logicalSwitch.OtherConfig = map[string]string{"subnet": hostSubnet.String()}
		}
	}

	err := libovsdbops.CreateOrUpdateLogicalSwitch(oc.mc.nbClient, &logicalSwitch, &logicalSwitch.ExternalIDs, &logicalSwitch.OtherConfig)
	if err != nil {
		klog.Errorf("Failed to create logical switch %s for network %s: %v", switchName, oc.nadInfo.NetName, err)
		return err
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

// SetupLocalnetMaster creates localnet switch for the network
func (oc *Controller) setupLocalnetMaster() error {
	switchName := util.GetClusterScopedName(oc.nadInfo.Prefix + types.OVNLocalnetSwitch)
	if err := oc.setupLayer2Switch(switchName); err != nil {
		return err
	}

	// Add external interface as a logical port to external_switch.
	// This is a learning switch port with "unknown" address. The external
	// world is accessed via this port.
	logicalSwitchPort := nbdb.LogicalSwitchPort{
		Addresses: []string{"unknown"},
		Type:      "localnet",
		Options: map[string]string{
			"network_name": oc.nadInfo.Prefix + types.LocalNetBridgeName,
		},
		Name: util.GetClusterScopedName(oc.nadInfo.Prefix + types.OVNLocalnetPort),
	}
	if oc.nadInfo.VlanId != 0 {
		intVlanID := int(oc.nadInfo.VlanId)
		logicalSwitchPort.TagRequest = &intVlanID
	}

	logicalSwitch := nbdb.LogicalSwitch{Name: switchName}
	err := libovsdbops.CreateOrUpdateLogicalSwitchPortsOnSwitch(oc.mc.nbClient, &logicalSwitch, &logicalSwitchPort)
	if err != nil {
		return fmt.Errorf("failed to add logical port %s to switch %s: %v", logicalSwitchPort.Name, switchName, err)
	}

	return nil
}

// deleteLocalnetMaster delete localnet switch for the network
func (oc *Controller) deleteLocalnetMaster() {
	switchName := util.GetClusterScopedName(oc.nadInfo.Prefix + types.OVNLocalnetSwitch)
	if err := libovsdbops.DeleteLogicalSwitch(oc.mc.nbClient, switchName); err != nil {
		klog.Errorf("Failed to delete logical switch %s: %v", switchName, err)
	}
}

// connectToLogicalRouter connect layer2 switch to the specified logicalRouter, oc is the layer2 controller
func (oc *Controller) connectToLogicalRouter(logicalRouterName string) error {
	switchName := oc.nadInfo.Prefix + types.OvnLayer2Switch

	var nodeLRPMAC net.HardwareAddr
	logicalRouterPortNetwork := []string{}
	for _, subnet := range oc.clusterSubnets {
		gwIfAddr := util.GetNodeGatewayIfAddr(subnet.CIDR)
		logicalRouterPortNetwork = append(logicalRouterPortNetwork, gwIfAddr.String())
		if !utilnet.IsIPv6CIDR(subnet.CIDR) {
			nodeLRPMAC = util.IPAddrToHWAddr(gwIfAddr.IP)
			break
		}
	}

	logicalRouterPortName := types.RouterToSwitchPrefix + switchName
	logicalRouterPort := nbdb.LogicalRouterPort{
		Name:     logicalRouterPortName,
		MAC:      nodeLRPMAC.String(),
		Networks: logicalRouterPortNetwork,
	}
	logicalRouter := nbdb.LogicalRouter{Name: logicalRouterName}

	err := libovsdbops.CreateOrUpdateLogicalRouterPort(oc.mc.nbClient, &logicalRouter,
		&logicalRouterPort, nil, &logicalRouterPort.MAC, &logicalRouterPort.Networks)
	if err != nil {
		return fmt.Errorf("failed to add logical router port %v to router %s: %v", logicalRouterPortName, logicalRouterName, err)
	}

	logicalSwitchPortName := types.SwitchToRouterPrefix + switchName
	logicalSwitchPort := nbdb.LogicalSwitchPort{
		Name:      logicalSwitchPortName,
		Type:      "router",
		Options:   map[string]string{"router-port": logicalRouterPortName},
		Addresses: []string{"router"},
	}
	sw := nbdb.LogicalSwitch{Name: switchName}
	err = libovsdbops.CreateOrUpdateLogicalSwitchPortsOnSwitch(oc.mc.nbClient, &sw, &logicalSwitchPort)
	if err != nil {
		return fmt.Errorf("failed to add logical port %v to switch %s: %v", logicalSwitchPortName, switchName, err)
	}
	return nil
}

// disconnectFromLogicalRouter disconnects layer2 switch from the specified logicalRouter, oc is the layer2 controller
func (oc *Controller) disconnectFromLogicalRouterOps(logicalRouterName string) ([]ovsdb.Operation, error) {
	switchName := oc.nadInfo.Prefix + types.OvnLayer2Switch
	logicalRouterPortName := types.RouterToSwitchPrefix + switchName
	logicalRouter := nbdb.LogicalRouter{Name: logicalRouterName}
	nodeLogicalRouterPort := nbdb.LogicalRouterPort{
		Name: logicalRouterPortName,
	}
	return libovsdbops.DeleteLogicalRouterPortsOps(oc.mc.nbClient, nil, &logicalRouter, &nodeLogicalRouterPort)
}

// Note that all connect_to_nad related operastion is result of net-attach-def watch handler function:
//  1. Add/Delete of the layer2 nad which requests to connect to layer3 nad.
//     This could fail if layer3 nad has not been applied yet
//  2. Add/Delete of layer3 nad. As a result, all layer2 nads associated needs to connect/disconnect to it.
//
// All above operations are serialized by net-attach-def watch handlers.

// setupLayer2Master creates layer2 switch and connect to ovn_cluster_router of the specified nad if requested
func (oc *Controller) setupLayer2Master() error {
	switchName := oc.nadInfo.Prefix + types.OvnLayer2Switch
	err := oc.setupLayer2Switch(switchName)
	if err != nil {
		return err
	}

	if oc.nadInfo.ConnectToNad == "" {
		return nil
	}

	// Find the controller and its logical cluster router's name associated with the given OvnK8sConnectToNad
	logicalRouterName := ""
	found := false
	oc.mc.allOvnControllers.Range(func(koc, voc interface{}) bool {
		l3Controller := voc.(*Controller)
		// the layer 2 network can only connect to layer 3 network
		if l3Controller.nadInfo.TopoType != types.Layer3AttachDefTopoType {
			return true
		}
		l3Controller.nadInfo.NetAttachDefs.Range(func(nadName, v interface{}) bool {
			if nadName.(string) == oc.nadInfo.ConnectToNad {
				l3Controller.startMutex.Lock()
				// check if the specific logical router is created
				if l3Controller.isStarted {
					logicalRouterName = l3Controller.nadInfo.Prefix + types.OVNClusterRouter
				}
				l3Controller.startMutex.Unlock()
				found = true
				return false
			}
			return true
		})
		return !found
	})

	// it may be the nad is not found yet, or the cluster router failed to be created, do not return failure
	// when the nad or the logical router finally shows up, the l3 controller will try to connect back.
	if logicalRouterName == "" {
		if !found {
			klog.Warningf("Specified %s %s is either not found or not belongs to a layer3 controler", types.OvnK8sConnectToNad, oc.nadInfo.ConnectToNad)
		} else {
			klog.Warningf("Logical cluster router for specified %s %s failed to be created", types.OvnK8sConnectToNad, oc.nadInfo.ConnectToNad)
		}
	} else {
		err = oc.connectToLogicalRouter(logicalRouterName)
		if err != nil {
			err = fmt.Errorf("failed to connect network %s to nad %s, error: %v", oc.nadInfo.NetName, oc.nadInfo.ConnectToNad, err)
			logicalRouterName = ""
		}
	}

	// add the nad into mc.nadConnInfoMap. If failed to connect to the given nad, set logicalRouterName to be empty.
	connInfo, ok := oc.mc.nadConnInfoMap[oc.nadInfo.ConnectToNad]
	if !ok {
		connInfo = map[*Controller]string{}
		oc.mc.nadConnInfoMap[oc.nadInfo.ConnectToNad] = connInfo
	}
	connInfo[oc] = logicalRouterName
	return err
}

// deleteLayer2Master creates layer2 switch and connect to ovn_cluster_router of the specified nad if requested
func (oc *Controller) deleteLayer2Master() {
	var err error
	switchName := oc.nadInfo.Prefix + types.OvnLayer2Switch
	ops := []ovsdb.Operation{}
	if oc.nadInfo.ConnectToNad != "" {
		if connInfo, ok := oc.mc.nadConnInfoMap[oc.nadInfo.ConnectToNad]; ok {
			if logicalRouterName, ok := connInfo[oc]; ok {
				if logicalRouterName != "" {
					disConnectOps, err := oc.disconnectFromLogicalRouterOps(logicalRouterName)
					if err != nil {
						klog.Errorf("Failed to get txn ops to disconnect layer2 network %s from nad %s: %v", oc.nadInfo.NetName, oc.nadInfo.ConnectToNad, err)
					} else {
						ops = append(ops, disConnectOps...)
					}
				}
			}
			delete(connInfo, oc)
			if len(connInfo) == 0 {
				delete(oc.mc.nadConnInfoMap, oc.nadInfo.ConnectToNad)
			}
		}
	}
	ops, err = libovsdbops.DeleteLogicalSwitchOps(oc.mc.nbClient, ops, switchName)
	if err != nil {
		klog.Errorf("Failed to get txn ops to delete logical switch %s: %v", switchName, err)
		return
	}
	_, err = libovsdbops.TransactAndCheck(oc.mc.nbClient, ops)
	if err != nil {
		klog.Errorf("Failed to execute txn to disconnect and delete layer 2 network %s: %v", oc.nadInfo.NetName, err)
	}
}

// nad with the given nadKeyName is applied and its OVN cluster route is created.
// connect it to all the layer 2 network requested;
func (l3Controller *Controller) connectToLayer2Network(nadKeyName string) {
	if l3Controller.nadInfo.TopoType != types.Layer3AttachDefTopoType {
		return
	}

	if connInfo, ok := l3Controller.mc.nadConnInfoMap[nadKeyName]; ok {
		for oc, logicalRouterName := range connInfo {
			if logicalRouterName != "" {
				klog.Errorf("Unexpected: layer2 network %s already connected to %s, but %s is just created",
					oc.nadInfo.NetName, logicalRouterName, l3Controller.nadInfo.Prefix+types.OVNClusterRouter)
				continue
			}
			if err := oc.connectToLogicalRouter(l3Controller.nadInfo.Prefix + types.OVNClusterRouter); err != nil {
				klog.Errorf("Failed to connect layer2 network %s to %s",
					oc.nadInfo.NetName, l3Controller.nadInfo.Prefix+types.OVNClusterRouter)
				continue
			}
			connInfo[oc] = l3Controller.nadInfo.Prefix + types.OVNClusterRouter
		}
	}
}

// nad with the given nadKeyName is deleted. Disconnect it from all the layer 2 network requested
func (l3Controller *Controller) disconnectFromLayer2Network(nadKeyName string) {
	if l3Controller.nadInfo.TopoType != types.Layer3AttachDefTopoType {
		return
	}

	allops := []ovsdb.Operation{}
	if connInfo, ok := l3Controller.mc.nadConnInfoMap[nadKeyName]; ok {
		for oc, logicalRouterName := range connInfo {
			if logicalRouterName == "" {
				continue
			}
			ops, err := oc.disconnectFromLogicalRouterOps(l3Controller.nadInfo.Prefix + types.OVNClusterRouter)
			if err != nil {
				klog.Errorf("Failed to get txn ops to disconnect layer2 network %s from nad %s: %v", oc.nadInfo.NetName, nadKeyName, err)
			}
			allops = append(allops, ops...)
			connInfo[oc] = ""
		}
	}
	_, err := libovsdbops.TransactAndCheck(l3Controller.mc.nbClient, allops)
	if err != nil {
		klog.Errorf("Failed to disconnect all connected layer2 networks requested to connect to NAD %s: %v", nadKeyName, err)
	}
}
