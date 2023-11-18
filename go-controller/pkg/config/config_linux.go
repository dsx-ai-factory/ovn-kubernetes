//go:build linux
// +build linux

package config

import (
	"fmt"
	"k8s.io/klog/v2"

	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/types"
	"github.com/vishvananda/netlink"
)

// ValidateConfig validates the configuration
func ValidateConfig() error {
	// validates existence of MgmtPortNetdev if MgmtPortNetdev is provided, for:
	//	 - primary DPU node
	//	 - DPU-host node
	//	 - full mode node when MgmtPortNetdev
	if OvnKubeNode.MgmtPortNetdev != "" && (OvnKubeNode.Mode != types.NodeModeDPU || OvnKubeNode.IsPrimaryDPU) {
		// management port device may already be renamed to specific interface name
		_, err := netlink.LinkByName(types.K8sMgmtIntfName)
		if err != nil {
			klog.V(5).Infof("Couldn't find %s device, so going to find %s device", types.K8sMgmtIntfName, OvnKubeNode.MgmtPortNetdev)
			// management port device may not be renamed to specified mgmtIntfName yet, check it directly
			_, err = netlink.LinkByName(OvnKubeNode.MgmtPortNetdev)
			if err != nil {
				return fmt.Errorf("failed to get management port device %s, error: %v", OvnKubeNode.MgmtPortNetdev, err)
			}
		}
	}
	return nil
}
