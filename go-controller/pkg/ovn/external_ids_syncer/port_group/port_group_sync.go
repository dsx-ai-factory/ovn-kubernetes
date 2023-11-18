package ovnentity

import (
	"fmt"
	"strings"

	libovsdbclient "github.com/ovn-org/libovsdb/client"
	libovsdb "github.com/ovn-org/libovsdb/ovsdb"
	libovsdbops "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/libovsdb/ops"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/nbdb"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/types"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util"
	"k8s.io/klog/v2"
)

type portGroupSyncer struct {
	util.NetInfo
	nbClient libovsdbclient.Client
	// txnBatchSize is used to control how many acls will be updated with 1 db transaction.
	txnBatchSize      int
	ignoredPortGroups int
}

// NetInfo is the one of the network controller that own all OVN logical entities
func NewPortGroupSyncer(nbClient libovsdbclient.Client, netInfo util.NetInfo, txnBatchSize int) *portGroupSyncer {
	return &portGroupSyncer{
		NetInfo:      netInfo,
		nbClient:     nbClient,
		txnBatchSize: txnBatchSize,
	}
}

const (
	// port groups suffixes
	// ingressDefaultDenySuffix is the suffix used when creating the ingress port group for a namespace
	ingressDefaultDenySuffix = "ingressDefaultDeny"
	// egressDefaultDenySuffix is the suffix used when creating the ingress port group for a namespace
	egressDefaultDenySuffix = "egressDefaultDeny"
)

func (syncer *portGroupSyncer) oldDefaultDenyPortGroupName(namespace, gressSuffix string) string {
	return syncer.GetNetworkScopedName(util.HashForOVN(namespace)) + "_" + gressSuffix
}

func (syncer *portGroupSyncer) newDefaultDenyPortGroupName(namespace, gressSuffix string) string {
	return util.HashForOVN(syncer.GetNetworkScopedName(namespace)) + "_" + gressSuffix
}

func (syncer *portGroupSyncer) oldNetworkPolicyPGName(namespace, name string) string {
	readableGroupName := fmt.Sprintf("%s_%s", namespace, name)
	return syncer.GetNetworkScopedName(util.HashForOVN(readableGroupName))
}

func (syncer *portGroupSyncer) newNetworkPolicyPGName(namespace, name string) string {
	readableGroupName := fmt.Sprintf("%s_%s", namespace, name)
	return util.HashForOVN(syncer.GetNetworkScopedName(readableGroupName))
}

func (syncer *portGroupSyncer) oldClusterPortGroupNameBaseName(baseName string) string {
	return syncer.GetNetworkScopedName(baseName)
}

func (syncer *portGroupSyncer) newClusterPortGroupNameBaseName(baseName string) string {
	if syncer.IsSecondary() {
		return util.HashForOVN(syncer.GetNetworkName()) + "_" + baseName
	}
	return baseName
}

func (syncer *portGroupSyncer) oldMulticastPortGroupName(namespace string) string {
	return syncer.GetNetworkScopedName(util.HashForOVN(namespace))
}

func (syncer *portGroupSyncer) newMulticastPortGroupName(namespace string) string {
	return util.HashForOVN(syncer.GetNetworkScopedName(namespace))
}

func (syncer *portGroupSyncer) SyncPortGroups(expectedPolicies map[string]map[string]bool) error {
	var err error
	p := func(item *nbdb.PortGroup) bool {
		return item.ExternalIDs[types.LegacyNetworkExternalID] == syncer.GetNetworkName()
	}
	legacyPortGroups, err := libovsdbops.FindPortGroupsWithPredicate(syncer.nbClient, p)
	if err != nil {
		return fmt.Errorf("unable to find stale port groups for network %s: %v", syncer.GetNetworkName(), err)
	}
	if len(legacyPortGroups) == 0 {
		return nil
	}

	// There are 4 types of legacy port groups:
	//  1. ingressDefaultDeny and egressDefaultDeny port groups:
	//       external-ids:name: hash(<ns>)_["ingressDefaultDeny"|"egressDefaultDeny"]
	//       name: <cluster_prefix><network_prefix>hash(<ns>)_["ingressDefaultDeny"|"egressDefaultDeny"]
	//       newName: hash(<cluster_prefix><network_prefix><ns>)_["ingressDefaultDeny"|"egressDefaultDeny"]
	//  2. policy port group
	//       extenral-ids:name: <policy_namespace>_<policy_name>
	//       name: <cluster_prefix><network_prefix>hash(<policy_namespace>_<policy_name>)
	//       newName: hash(<cluster_prefix><network_prefix><policy_namespace>_<policy_name>)
	//  3. types.ClusterPortGroupNameBase or types.ClusterRtrPortGroupNameBase port groups:
	//       external-ids:name: types.ClusterPortGroupNameBase or types.ClusterRtrPortGroupNameBase
	//       name: <cluster_prefix><network_prefix>types.ClusterPortGroupNameBase or <cluster_prefix><network_prefix>types.ClusterRtrPortGroupNameBase
	//       newName: hash(<cluster_prefix><network_prefix>)types.ClusterPortGroupNameBase or hash(<cluster_prefix><network_prefix>)types.ClusterRtrPortGroupNameBase
	//  4. per namespace multicast port group:
	//       external-ids:name: <ns>
	//       name: <cluster_prefix><network_prefix>hash(<ns>)
	//       newName: hash(<cluster_prefix><network_prefix><ns>)
	expectedPortGroupName := map[string]string{}
	expectedPortGroupName[syncer.oldClusterPortGroupNameBaseName(types.ClusterPortGroupNameBase)] = syncer.newClusterPortGroupNameBaseName(types.ClusterPortGroupNameBase)
	expectedPortGroupName[syncer.oldClusterPortGroupNameBaseName(types.ClusterRtrPortGroupNameBase)] = syncer.newClusterPortGroupNameBaseName(types.ClusterRtrPortGroupNameBase)
	for ns, policies := range expectedPolicies {
		for name := range policies {
			expectedPortGroupName[syncer.oldDefaultDenyPortGroupName(ns, ingressDefaultDenySuffix)] =
				syncer.newDefaultDenyPortGroupName(ns, ingressDefaultDenySuffix)
			expectedPortGroupName[syncer.oldDefaultDenyPortGroupName(ns, egressDefaultDenySuffix)] =
				syncer.newDefaultDenyPortGroupName(ns, egressDefaultDenySuffix)
			expectedPortGroupName[syncer.oldMulticastPortGroupName(ns)] = syncer.newMulticastPortGroupName(ns)
			expectedPortGroupName[syncer.oldNetworkPolicyPGName(ns, name)] = syncer.newNetworkPolicyPGName(ns, name)
		}
	}
	updatedCount := 0
	defer func() {
		klog.Infof("SyncPortGroups for network %s handled %d of %d stale port groups, %d of them were ignored",
			syncer.GetNetworkName, updatedCount-syncer.ignoredPortGroups, len(legacyPortGroups), syncer.ignoredPortGroups)
	}()

	i := 0
	for i < len(legacyPortGroups) {
		ops := []libovsdb.Operation{}
		aclsToUpdate := map[string]*nbdb.ACL{}
		for j := 0; (j < syncer.txnBatchSize || syncer.txnBatchSize == 0) && i < len(legacyPortGroups); i, j = i+1, j+1 {
			legacyPortGroup := legacyPortGroups[i]
			oldPortGroupName := legacyPortGroup.Name
			// check if legacyPortGroup is in the form of old name that needs to be updated
			newPortGroupName := expectedPortGroupName[oldPortGroupName]
			if newPortGroupName == "" {
				// unexpected name
				klog.Errorf("Found unexpected port group %s, delete it", oldPortGroupName, err)
				ops, err = libovsdbops.DeletePortGroupsOps(syncer.nbClient, ops, oldPortGroupName)
				syncer.ignoredPortGroups += 1
				continue
			}
			aclPred := func(acl *nbdb.ACL) bool {
				return strings.Contains(acl.Match, "@"+oldPortGroupName)
			}
			acls, err := libovsdbops.FindACLsWithPredicate(syncer.nbClient, aclPred)
			if err != nil {
				return fmt.Errorf("failed to find acls reference port group %s: %v", oldPortGroupName, err)
			}
			klog.V(5).Infof("Found old port group %s, update its name to %s", oldPortGroupName, newPortGroupName)
			legacyPortGroup.ExternalIDs = map[string]string{
				types.NetworkExternalID:       syncer.GetNetworkName(),
				types.LegacyNetworkExternalID: "",
			}
			ops, err = libovsdbops.UpdatePortGroupSetNameAndExternalIDsOps(syncer.nbClient, ops, legacyPortGroup, newPortGroupName)
			if err != nil {
				return fmt.Errorf("failed to get transact ops to update port group %s's name to %s, err: %v",
					oldPortGroupName, newPortGroupName, err)
			}
			for _, acl := range acls {
				if _, ok := aclsToUpdate[acl.UUID]; !ok {
					aclsToUpdate[acl.UUID] = acl
				}
				aclsToUpdate[acl.UUID].Match = strings.ReplaceAll(aclsToUpdate[acl.UUID].Match, oldPortGroupName, newPortGroupName)
			}
		}
		for _, acl := range aclsToUpdate {
			ops, err = libovsdbops.UpdateACLsOps(syncer.nbClient, ops, acl)
			if err != nil {
				return fmt.Errorf("failed to get update acl ops: %v", err)
			}
		}
		_, err = libovsdbops.TransactAndCheck(syncer.nbClient, ops)
		if err != nil {
			return fmt.Errorf("failed to transact port group sync ops for network %s: %v", syncer.GetNetworkName(), err)
		}
		updatedCount = i
	}
	return nil
}
