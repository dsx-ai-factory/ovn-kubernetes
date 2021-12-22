package ovn

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/nbdb"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/ovn/libovsdbops"
	ovnlb "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/ovn/loadbalancer"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/sbdb"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/types"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util"

	kapi "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
)

// gatewayCleanup removes all the NB DB objects created for a node's gateway
func (oc *Controller) gatewayCleanup(nodeName string) error {
	gatewayRouter := types.GWRouterPrefix + nodeName

	// Get the gateway router port's IP address (connected to join switch)
	var nextHops []net.IP

	gwIPAddrs, err := util.GetLRPAddrs(oc.mc.nbClient, types.GWRouterToJoinSwitchPrefix+gatewayRouter)
	if err != nil {
		return err
	}

	for _, gwIPAddr := range gwIPAddrs {
		nextHops = append(nextHops, gwIPAddr.IP)
	}
	oc.staticRouteCleanup(nextHops)

	// Remove the patch port that connects join switch to gateway router
	logicalSwitch := nbdb.LogicalSwitch{}
	logicalSwitchPort := nbdb.LogicalSwitchPort{
		Name: types.JoinSwitchToGWRouterPrefix + gatewayRouter,
	}
	opModels := []libovsdbops.OperationModel{
		{
			Model: &logicalSwitchPort,
			DoAfter: func() {
				if logicalSwitchPort.UUID != "" {
					logicalSwitch.Ports = []string{logicalSwitchPort.UUID}
				}
			},
		},
		{
			Model:          &logicalSwitch,
			ModelPredicate: func(ls *nbdb.LogicalSwitch) bool { return ls.Name == types.OVNJoinSwitch },
			OnModelMutations: []interface{}{
				&logicalSwitch.Ports,
			},
		},
	}
	if err := oc.mc.modelClient.Delete(opModels...); err != nil {
		return fmt.Errorf("failed to delete logical switch port %s%s:, error: %v", types.JoinSwitchToGWRouterPrefix, gatewayRouter, err)
	}

	// Remove router to lb associations from the LBCache before removing the router
	lbCache, err := ovnlb.GetLBCache(oc.mc.nbClient)
	if err != nil {
		return fmt.Errorf("failed to get load_balancer cache for router %s: %v", gatewayRouter, err)
	}
	lbCache.RemoveRouter(gatewayRouter)

	// Remove the gateway router associated with nodeName
	opModel := libovsdbops.OperationModel{
		ModelPredicate: func(lr *nbdb.LogicalRouter) bool { return lr.Name == gatewayRouter },
		ExistingResult: &[]nbdb.LogicalRouter{},
	}
	if err := oc.mc.modelClient.Delete(opModel); err != nil {
		return fmt.Errorf("failed to delete gateway router %s, error: %v", gatewayRouter, err)
	}

	// Remove external switch
	opModel = libovsdbops.OperationModel{
		ModelPredicate: func(ls *nbdb.LogicalSwitch) bool { return ls.Name == types.ExternalSwitchPrefix+nodeName },
		ExistingResult: &[]nbdb.LogicalSwitch{},
	}
	if err := oc.mc.modelClient.Delete(opModel); err != nil {
		return fmt.Errorf("failed to delete external switch %s, error: %v", types.ExternalSwitchPrefix+nodeName, err)
	}

	exGWexternalSwitch := types.ExternalSwitchPrefix + types.ExternalSwitchPrefix + nodeName
	opModel = libovsdbops.OperationModel{
		ModelPredicate: func(ls *nbdb.LogicalSwitch) bool { return ls.Name == exGWexternalSwitch },
		ExistingResult: &[]nbdb.LogicalSwitch{},
	}
	if err := oc.mc.modelClient.Delete(opModel); err != nil {
		return fmt.Errorf("failed to delete external switch %s, error: %v", exGWexternalSwitch, err)
	}

	// This will cleanup the NodeSubnetPolicy in local and shared gateway modes. It will be a no-op for any other mode.
	oc.delPbrAndNatRules(nodeName, nil)
	return nil
}

func (oc *Controller) delPbrAndNatRules(nodeName string, lrpTypes []string) {
	// delete the dnat_and_snat entry that we added for the management port IP
	// Note: we don't need to delete any MAC bindings that are dynamically learned from OVN SB DB
	// because there will be none since this NAT is only for outbound traffic and not for inbound
	mgmtPortName := types.K8sPrefix + nodeName
	nat := libovsdbops.BuildRouterDNATAndSNAT(nil, nil, mgmtPortName, "", nil)
	err := libovsdbops.DeleteNatsFromRouter(oc.mc.nbClient, types.OVNClusterRouter, nat)
	if err != nil {
		klog.Errorf("Failed to delete the dnat_and_snat associated with the management "+
			"port %s, error: %v", mgmtPortName, err)
	}

	// delete all logical router policies on ovn_cluster_router
	oc.removeLRPolicies(nodeName, lrpTypes)
}

func (oc *Controller) staticRouteCleanup(nextHops []net.IP) {
	for _, nextHop := range nextHops {
		logicalRouter := nbdb.LogicalRouter{}
		logicalRouterStaticRouteRes := []nbdb.LogicalRouterStaticRoute{}
		opModels := []libovsdbops.OperationModel{
			{
				Model: &nbdb.LogicalRouterStaticRoute{},
				ModelPredicate: func(lrsr *nbdb.LogicalRouterStaticRoute) bool {
					return lrsr.Nexthop == nextHop.String()
				},
				ExistingResult: &logicalRouterStaticRouteRes,
				DoAfter: func() {
					logicalRouter.StaticRoutes = libovsdbops.ExtractUUIDsFromModels(&logicalRouterStaticRouteRes)
				},
				BulkOp: true,
			},
			{
				Model:          &logicalRouter,
				ModelPredicate: func(lr *nbdb.LogicalRouter) bool { return lr.Name == types.OVNClusterRouter },
				OnModelMutations: []interface{}{
					&logicalRouter.StaticRoutes,
				},
			},
		}
		if err := oc.mc.modelClient.Delete(opModels...); err != nil {
			klog.Errorf("Failed to delete static route for nexthop: %s, err: %v", nextHop.String(), err)
		}
	}
}

// remove Logical Router Policy on ovn_cluster_router for a specific node.
// Specify priorities to only delete specific types
func (oc *Controller) removeLRPolicies(nodeName string, priorities []string) {
	if len(priorities) == 0 {
		priorities = []string{types.NodeSubnetPolicyPriority}
	}
	for _, priority := range priorities {
		intPriority, _ := strconv.Atoi(priority)

		logicalRouter := nbdb.LogicalRouter{}
		result := []nbdb.LogicalRouterPolicy{}
		opModels := []libovsdbops.OperationModel{
			{
				ModelPredicate: func(lrp *nbdb.LogicalRouterPolicy) bool {
					return strings.Contains(lrp.Match, fmt.Sprintf("%s ", nodeName)) && lrp.Priority == intPriority
				},
				ExistingResult: &result,
				DoAfter: func() {
					logicalRouter.Policies = libovsdbops.ExtractUUIDsFromModels(&result)
				},
				BulkOp: true,
			},
			{
				Model:          &logicalRouter,
				ModelPredicate: func(lr *nbdb.LogicalRouter) bool { return lr.Name == types.OVNClusterRouter },
				OnModelMutations: []interface{}{
					&logicalRouter.Policies,
				},
			},
		}
		if err := oc.mc.modelClient.Delete(opModels...); err != nil {
			klog.Errorf("Failed to remove the policy routes %s associated with the node %s, error: %v", priority, nodeName, err)
		}
	}
}

// removes DGP, snat_and_dnat entries, and LRPs
func (oc *Controller) cleanupDGP(nodes *kapi.NodeList) error {
	klog.Infof("Removing DGP %v", nodes)
	// remove dnat_snat entries as well as LRPs
	for _, node := range nodes.Items {
		oc.delPbrAndNatRules(node.Name, []string{types.InterNodePolicyPriority, types.MGMTPortPolicyPriority})
	}
	// remove SBDB MAC bindings for DGP
	for _, ip := range []string{types.V4NodeLocalNATSubnetNextHop, types.V6NodeLocalNATSubnetNextHop} {
		opModels := []libovsdbops.OperationModel{
			{
				Model: &sbdb.MACBinding{
					IP: ip,
				},
			},
		}
		if err := oc.mc.modelClient.WithClient(oc.mc.sbClient).Delete(opModels...); err != nil {
			return fmt.Errorf("unable to remove mac_binding for DGP, err: %v", err)
		}
	}
	// remove node local switch
	opModels := []libovsdbops.OperationModel{
		{
			ModelPredicate: func(ls *nbdb.LogicalSwitch) bool { return ls.Name == types.NodeLocalSwitch },
			ExistingResult: &[]nbdb.LogicalSwitch{},
		},
	}
	if err := oc.mc.modelClient.Delete(opModels...); err != nil {
		return fmt.Errorf("unable to remove node local switch, err: %v", err)
	}

	// remove lrp on ovn_cluster_router. Will also remove gateway chassis.
	dgpName := types.RouterToSwitchPrefix + types.NodeLocalSwitch
	logicalRouter := nbdb.LogicalRouter{}
	logicalRouterPort := nbdb.LogicalRouterPort{
		Name: dgpName,
	}
	opModels = []libovsdbops.OperationModel{
		{
			Model: &logicalRouterPort,
			DoAfter: func() {
				if logicalRouterPort.UUID != "" {
					logicalRouter.Ports = []string{logicalRouterPort.UUID}
				}
			},
		},
		{
			Model: &logicalRouter,
			ModelPredicate: func(lr *nbdb.LogicalRouter) bool {
				return lr.Name == types.OVNClusterRouter
			},
			OnModelMutations: []interface{}{
				&logicalRouter.Ports,
			},
		},
	}
	if err := oc.mc.modelClient.Delete(opModels...); err != nil {
		return fmt.Errorf("unable to delete DGP LRP, error: %v", err)
	}
	return nil
}
