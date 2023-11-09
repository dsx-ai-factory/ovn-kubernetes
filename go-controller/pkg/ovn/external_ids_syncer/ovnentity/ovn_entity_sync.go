package ovnentity

import (
	"fmt"

	libovsdbclient "github.com/ovn-org/libovsdb/client"
	libovsdb "github.com/ovn-org/libovsdb/ovsdb"
	libovsdbops "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/libovsdb/ops"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/nbdb"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/types"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util"

	"k8s.io/klog/v2"
)

type ovnEntitySyncer struct {
	util.NetInfo
	nbClient libovsdbclient.Client
	// txnBatchSize is used to control how many acls will be updated with 1 db transaction.
	txnBatchSize int
}

// NetInfo is the one of the network controller that own all OVN logical entities
func NeOVNEntitySyncer(nbClient libovsdbclient.Client, netInfo util.NetInfo, txnBatchSize int) *ovnEntitySyncer {
	return &ovnEntitySyncer{
		NetInfo:      netInfo,
		nbClient:     nbClient,
		txnBatchSize: txnBatchSize,
	}
}

func (syncer *ovnEntitySyncer) SyncLogicalSwitches() error {
	var err error
	p := func(item *nbdb.LogicalSwitch) bool {
		return item.ExternalIDs[types.LegacyNetworkExternalID] == syncer.GetNetworkName() &&
			util.HasExternalIDsForCluster(item.ExternalIDs)
	}
	legacyLogicalSwitches, err := libovsdbops.FindLogicalSwitchesWithPredicate(syncer.nbClient, p)
	if err != nil {
		return fmt.Errorf("unable to find stale switches for network %s: %v", syncer.GetNetworkName(), err)
	}
	if len(legacyLogicalSwitches) == 0 {
		return nil
	}
	updatedCount := 0
	defer func() {
		klog.Infof("SyncLogicalSwitches for network %s handled %d of %d stale logical switches",
			syncer.GetNetworkName(), updatedCount, len(legacyLogicalSwitches))
	}()

	i := 0
	for i < len(legacyLogicalSwitches) {
		ops := []libovsdb.Operation{}
		for j := 0; (j < syncer.txnBatchSize || syncer.txnBatchSize == 0) && i < len(legacyLogicalSwitches); i, j = i+1, j+1 {
			legacySwitch := legacyLogicalSwitches[i]
			logicalSwitch := nbdb.LogicalSwitch{
				Name: legacySwitch.Name,
				ExternalIDs: map[string]string{
					types.NetworkExternalID:       syncer.GetNetworkName(),
					types.TopologyExternalID:      syncer.TopologyType(),
					types.LegacyNetworkExternalID: "",
				},
			}
			ops, err = libovsdbops.UpdateLogicalSwitchSetExternalIDsOps(syncer.nbClient, ops, &logicalSwitch)
			if err != nil {
				return fmt.Errorf("failed to update external_ids %v for switch %s, err: %v",
					logicalSwitch.ExternalIDs, logicalSwitch.Name, err)
			}
		}
		_, err = libovsdbops.TransactAndCheck(syncer.nbClient, ops)
		if err != nil {
			return fmt.Errorf("failed to transact logical switch sync ops for network %s: %v", syncer.GetNetworkName(), err)
		}
		updatedCount = i
	}
	return nil
}

func (syncer *ovnEntitySyncer) SyncLogicalRouters() error {
	var err error
	p := func(item *nbdb.LogicalRouter) bool {
		return item.ExternalIDs[types.LegacyNetworkExternalID] == syncer.GetNetworkName() &&
			util.HasExternalIDsForCluster(item.ExternalIDs)
	}
	legacyLogicalRouters, err := libovsdbops.FindLogicalRoutersWithPredicate(syncer.nbClient, p)
	if err != nil {
		return fmt.Errorf("unable to find stale logical routers for network %s: %v", syncer.GetNetworkName(), err)
	}
	if len(legacyLogicalRouters) == 0 {
		return nil
	}

	updatedCount := 0
	defer func() {
		klog.Infof("SyncLogicalRouters for network %s handled %d of %d stale logical routers",
			syncer.GetNetworkName(), updatedCount, len(legacyLogicalRouters))
	}()

	i := 0
	for i < len(legacyLogicalRouters) {
		ops := []libovsdb.Operation{}
		for j := 0; (j < syncer.txnBatchSize || syncer.txnBatchSize == 0) && i < len(legacyLogicalRouters); i, j = i+1, j+1 {
			legacyRouter := legacyLogicalRouters[i]
			logicalRouter := nbdb.LogicalRouter{
				Name: legacyRouter.Name,
				ExternalIDs: map[string]string{
					types.NetworkExternalID:       syncer.GetNetworkName(),
					types.TopologyExternalID:      syncer.TopologyType(),
					types.LegacyNetworkExternalID: "",
				},
			}
			ops, err = libovsdbops.UpdateLogicalRouterSetExternalIDsOps(syncer.nbClient, ops, &logicalRouter)
			if err != nil {
				return fmt.Errorf("failed to update external_ids %v for router %s, err: %v",
					logicalRouter.ExternalIDs, logicalRouter.Name, err)
			}
		}
		_, err = libovsdbops.TransactAndCheck(syncer.nbClient, ops)
		if err != nil {
			return fmt.Errorf("failed to transact logical router sync ops for network %s: %v", syncer.GetNetworkName(), err)
		}
		updatedCount = i
	}
	return nil
}

func (syncer *ovnEntitySyncer) SyncLogicalSwitchPorts() error {
	var err error
	p := func(item *nbdb.LogicalSwitchPort) bool {
		return item.ExternalIDs[types.LegacyNetworkExternalID] == syncer.GetNetworkName() &&
			item.ExternalIDs["pod"] == "true" && util.HasExternalIDsForCluster(item.ExternalIDs)
	}
	legacyLogicalSwitchPorts, err := libovsdbops.FindAllLogicalSwitchPortsWithPredicate(syncer.nbClient, p)
	if err != nil {
		return fmt.Errorf("unable to find stale logical switch port for network %s: %v", syncer.GetNetworkName(), err)
	}
	if len(legacyLogicalSwitchPorts) == 0 {
		return nil
	}
	updatedCount := 0
	defer func() {
		klog.Infof("SyncLogicalSwitches for network %s handled %d of %d stale logical switch ports",
			syncer.GetNetworkName(), updatedCount, len(legacyLogicalSwitchPorts))
	}()

	i := 0
	for i < len(legacyLogicalSwitchPorts) {
		ops := []libovsdb.Operation{}
		for j := 0; (j < syncer.txnBatchSize || syncer.txnBatchSize == 0) && i < len(legacyLogicalSwitchPorts); i, j = i+1, j+1 {
			legacySwitchPort := legacyLogicalSwitchPorts[i]
			// legacySwitchPort may already have the legacy NAD name external_ids, update to the correct key
			// NADExternalID if that is the case. It is all right to not update legacy lsp's NADExternalID though
			// as it is not really used anywhere.
			externalIds := map[string]string{
				types.NetworkExternalID:       syncer.GetNetworkName(),
				types.TopologyExternalID:      syncer.TopologyType(),
				types.LegacyNetworkExternalID: "",
				"nad_name":                    "",
			}
			if nadName := legacySwitchPort.ExternalIDs["nad_name"]; nadName != "" {
				externalIds[types.NADExternalID] = nadName
			}
			logicalSwitchPort := nbdb.LogicalSwitchPort{
				Name:        legacySwitchPort.Name,
				ExternalIDs: externalIds,
			}
			ops, err = libovsdbops.UpdateLogicalSwitchPortSetExternalIDsOps(syncer.nbClient, ops, &logicalSwitchPort)
			if err != nil {
				return fmt.Errorf("failed to update external_ids %v for switch %s, err: %v",
					logicalSwitchPort.ExternalIDs, logicalSwitchPort.Name, err)
			}
		}
		_, err = libovsdbops.TransactAndCheck(syncer.nbClient, ops)
		if err != nil {
			return fmt.Errorf("failed to transact logical switch port sync ops for network %s: %v", syncer.GetNetworkName(), err)
		}
		updatedCount = i
	}
	return nil
}
