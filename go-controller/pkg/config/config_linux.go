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
	// validates existence of MgmtPortNetdev for:
	//	 - primary DPU node
	//	 - DPU-host node
	//	 - full mode node when MgmtPortNetdev is configured
	if (OvnKubeNode.Mode == types.NodeModeDPU && OvnKubeNode.IsPrimaryDPU) ||
		(OvnKubeNode.Mode != types.NodeModeDPU && OvnKubeNode.MgmtPortNetdev != "") {
		// management port device may already be renamed to sepcifiec interface name
		_, err := netlink.LinkByName(OvnKubeNode.MgmtPortIntfName)
		if err != nil {
			klog.V(5).Infof("Couldn't find %s device, so going to find %s device", OvnKubeNode.MgmtPortIntfName, OvnKubeNode.MgmtPortNetdev)
			// management port device may not be renamed to specified mgmtIntfName yet, check it directly
			_, err = netlink.LinkByName(OvnKubeNode.MgmtPortNetdev)
			if err != nil {
				return fmt.Errorf("failed to get management port device %s, error: %v", OvnKubeNode.MgmtPortNetdev, err)
			}
		}
	}
	return nil
}
