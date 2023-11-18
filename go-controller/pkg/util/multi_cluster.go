package util

import (
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/types"
)

func GetPhysNetNameKeyForNode(nodeName string, labels map[string]string) string {
	// If the node has the physnet-name-key label, use that.
	if nodeName != "" {
		if value, ok := labels["k8s.ovn.org/physnet-name-key"]; ok {
			return value
		}
	}
	return types.PhysicalNetworkName
}
