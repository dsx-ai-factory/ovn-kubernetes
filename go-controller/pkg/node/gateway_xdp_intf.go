package node

import (
	"fmt"
	"time"

	"k8s.io/klog/v2"

	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/config"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/node/bridgeconfig"
	OFManager "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/openflow-manager"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util"
)

// Add OpenFlow flows
// -- to steer incoming TCP traffic to the XDP service
// -- to send packets from the XDP service to the K8s pod/VMI  to OVN
// -- to send outgoing from the K8s pod/VMI to the XDP service
// -- to send outgoing from the XDP service to external on the wire.
func (nc *SecondaryLocalnetNodeNetworkController) newXDPSharedGatewayOpenFlowManager(gwBridge *bridgeconfig.BridgeConfiguration, ofFlowManager bool) (*openflowManager, error) {

	klog.Info("Creating new XDP shared gateway for DPU")
	// for primary dpu, update open flows of default bridge, for non-primary dpu, update open flows of xdp bridge
	bridge := gwBridge.GetBridgeName()
	if !ofFlowManager {
		bridge = nc.defaultBridgeName
	}
	dftID := OFManager.OpenFlowCacheManager.CreateFlowCache(bridge)
	// add health check function to check default OpenFlow flows are on the shared gateway bridge
	ofm := &openflowManager{
		defaultBridge:       gwBridge,
		defaultBridgeFlowID: dftID,
	}

	if ofFlowManager {
		// Assume the shared gw will have no rules managed outside ovn-k8s
		ofm.updateFlowCacheEntry("NORMAL", []string{fmt.Sprintf("table=0,priority=0,actions=%s\n", util.NormalAction)})
	}
	return ofm, nil
}

func (nc *SecondaryLocalnetNodeNetworkController) newXDPSharedGateway(isPrimaryDPU bool) (*gateway, error) {
	klog.Infof("Creating new XDP shared gateway for %s", nc.GetNetworkName())
	gw := &gateway{}

	b, err := bridgeconfig.NewXDPBridgeConfiguration(nc.GetNetworkName(), nc.bridgeName)
	if err != nil {
		klog.Infof("Failed creating new XDP shared gateway: %v", err)
		return nil, err
	}

	gw.readyFunc = func() (bool, error) {
		klog.Info("Setting patch ports for XDP Shared Gateway Openflow Manager")
		err := b.SetXDPBridgePatchOfPorts(nc.GetNetworkName())
		if err != nil {
			klog.Infof("Failed setting up  XDP Shared Gateway: %v", err)
			return false, err
		}
		klog.Info("XDP Gateway is ready")
		return true, nil
	}

	gw.initFunc = func() error {
		klog.Info("Setting phys ports for XDP Shared Gateway Openflow Manager")
		err := b.SetOfPorts() // TBD-merge set ofPatch again?
		if err != nil {
			klog.Infof("Failed setting up XDP Shared Gateway: %v", err)
			return err
		}

		gw.openflowManager, err = nc.newXDPSharedGatewayOpenFlowManager(b, !isPrimaryDPU)
		if err != nil {
			klog.Infof("Failed Creating XDP Shared Gateway Openflow Manager: %v", err)
			return err
		}

		return nil
	}

	klog.Info("Shared XDP Gateway Creation Complete")
	return gw, nil
}

func (nc *SecondaryLocalnetNodeNetworkController) initGatewayDPUXDP() (*gateway, error) {
	klog.Infof("Initializing XDP Gateway Functionality on DPU for %s", nc.GetNetworkName())
	var err error

	start := time.Now()
	// We need to manage a shared gw to monitor:
	//	- patch ports
	//	- openflow rules for XDP
	// for primary DPU the openflow rules are managed by the N-S gateway, so we'll use
	// the same to manage XDP openflow rules. For the primary DPU, we'll still use
	// a controller specific gw, just to monitor the patch ports since overloading
	// the N-S gateway for that purpose is not very clean.
	gw, err := nc.newXDPSharedGateway(config.OvnKubeNode.IsPrimaryDPU)
	if err != nil {
		return nil, fmt.Errorf("failed to setup Shared gatway for XDP: %v", err)
	}

	// the localnet patch port will be created when we create a logical port in that
	// network, so we can't make gw.readyFunc a prereq, i.e.
	//	waiter.AddWait(readyGwFunc, initGwFunc)
	//
	//	if err := waiter.Wait(); err != nil {
	//		return nil, fmt.Errorf("failed waiting for XDP bridge to  be ready: %v", err)
	//	}

	// Don't need to call gw.Init since we are not going to set up watchers etc. on this gw
	err = gw.initFunc()
	if err != nil {
		return nil, fmt.Errorf("error initializing XDP shared gateway: %v", err)
	}

	// XXX Check wg
	go gw.openflowManager.Run(nc.stopChan, nc.wg)

	err = nc.InitializeXDPServiceForNAD()
	if err != nil {
		return nil, fmt.Errorf("failed to bootstrap XDP: %v", err)
	}
	klog.Infof("Initializing XDP for NAD %s took %v", nc.GetNetworkName(), time.Since(start))

	return gw, nil
}

func (nc *SecondaryLocalnetNodeNetworkController) cleanGatewayDPUXDP(_ *gateway) error {

	klog.Infof("Destroying XDP for NAD %s", nc.GetNetworkName())
	err := nc.DestroyXDPServiceForNAD()
	if err != nil {
		klog.Infof("Failed to remove XDP config: %v", err)
	}
	klog.Infof("Removed XDP")

	return nil
}
