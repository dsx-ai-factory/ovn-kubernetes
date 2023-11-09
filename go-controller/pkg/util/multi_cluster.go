package util

import (
	"fmt"
	"strings"

	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/config"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/types"
)

// var clustername string
// var clusterNamePrefix string
// var prefixSeparator = "_"
//
//	func SetClusterName(name string) {
//		if name != "" {
//			clustername = name
//			clusterNamePrefix = "CLUSTER_" + clustername + prefixSeparator
//		}
//	}
//
//	func GetClusterName() string {
//		return clustername
//	}
//
// // GetClusterNamePrefix generates a prefix with cluster_name
// // If there is no cluster_name specified, it returns empty
//
//	func GetClusterNamePrefix() string {
//		return clusterNamePrefix
//	}
//
//	func GetOVNJoinSwitchName() string {
//		return GetClusterNamePrefix() + types.OVNJoinSwitch
//	}
//
// IsClusterScoped returns true if this cluster has a cluster name set.
func IsClusterScoped() bool {
	return config.Kubernetes.ClusterName != ""
}

// GetClusterPrefix returns prefix string for the clusterScoped name
func GetClusterPrefix() string {
	if !IsClusterScoped() {
		return ""
	}
	return "CLUSTER_" + config.Kubernetes.ClusterName + "_"
}

// GetClusterScopedName returns names after adding cluster prefix to the given string if cluster name is not empty,
// otherwise, returns the name directly
func GetClusterScopedName(name string) string {
	return fmt.Sprintf("%s%s", GetClusterPrefix(), name)
}

// RemoveMultiClusterScopeFromName returns the name after trimming its cluster prefix if cluster name is not empty,
// otherwise, returns the name directly
func RemoveMultiClusterScopeFromName(name string) string {
	return strings.Trim(name, GetClusterPrefix())
}

//	func CreateClusterScopedExternalIDs() map[string]string {
//		if config.Kubernetes.ClusterName != "" {
//			return map[string]string{types.OvnK8sClusterNameKey: config.Kubernetes.ClusterName}
//		}
//		return nil
//	}
func ExternalIDsForCluster(externalIDs map[string]string) map[string]string {
	if len(externalIDs) == 0 || externalIDs == nil {
		externalIDs = make(map[string]string)
	}
	if config.Kubernetes.ClusterName != "" {
		externalIDs[types.OvnK8sClusterNameKey] = config.Kubernetes.ClusterName
	}
	return externalIDs
}

// HasExternalIDsForCluster returns true if the current item's `cluster_name` external_id
// matches that of the current cluster. In case of non cluster scoped clusters (i.e no such external_id)
// it defaults to true.
func HasExternalIDsForCluster(externalIDs map[string]string) bool {
	clusterNameExtID, ok := externalIDs[types.OvnK8sClusterNameKey]
	if (config.Kubernetes.ClusterName == "" && !ok) || (config.Kubernetes.ClusterName != "" && config.Kubernetes.ClusterName == clusterNameExtID) {
		return true
	}
	return false
}

func GetPhysNetNameKey() string {
	if config.Gateway.PhysNetNameKey != "" {
		return config.Gateway.PhysNetNameKey
	}
	return types.PhysicalNetworkName
}

func GetPhysNetNameKeyForNode(nodeName string, labels map[string]string) string {
	if config.Gateway.PhysNetNameKey != "" {
		// If the node has the physnet-name-key label, use that.
		if nodeName != "" {
			if value, ok := labels["k8s.ovn.org/physnet-name-key"]; ok {
				return value
			}
		}
		// Default behaviour
		return config.Gateway.PhysNetNameKey
	}
	return types.PhysicalNetworkName
}

var conntrackzone int

func GetConntrackZone() int {
	if conntrackzone == 0 {
		conntrackzone = config.Default.ConntrackZone
	}
	return conntrackzone
}
