package node

import (
	"fmt"
	"strings"
	"time"

	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/config"
	OFManager "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/openflow-manager"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util"
	"github.com/pkg/errors"

	"k8s.io/klog/v2"
)

// Add OpenFlow flows
// -- to steer incoming TCP traffic to the XDP service
// -- to send packets from the XDP service to the K8s pod/VMI  to OVN
// -- to send outgoing from the K8s pod/VMI to the XDP service
// -- to send outgoing from the XDP service to external on the wire.
func (nc *SecondaryLocalnetNodeNetworkController) newXDPSharedGatewayOpenFlowManager(gwBridge *bridgeConfiguration, ofFlowManager bool) (*openflowManager, error) {

	klog.Info("Creating new XDP shared gateway for DPU")
	// for primary dpu, update open flows of default bridge, for non-primary dpu, update open flows of xdp bridge
	bridge := gwBridge.bridgeName
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

// A watered down version of bridgeForInterface since we assume bridge is created
// etc.
func (nc *SecondaryLocalnetNodeNetworkController) bridgeForXDPInterface() (*bridgeConfiguration, error) {
	klog.Infof("Setting up new XDP shared gateway for NAD %s", nc.GetNetworkName())

	bridgeName := nc.bridgeName
	uplinkName, err := getIntfName(bridgeName)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to find uplink for %s", bridgeName)
	}

	res := bridgeConfiguration{}
	res.bridgeName = bridgeName
	res.uplinkName = uplinkName

	// the name of the patch port created by ovn-controller is of the form (e.g. for NAD ovn-public)
	// patch-ovn.public_ovn_localnet_port-to-br-int
	// TODO(gmoodalbail): can this be nc.NetworkPrefix?
	patchNADStr := strings.Replace(nc.GetNetworkName(), "-", ".", -1)
	res.patchPort = "patch-" + patchNADStr + "_ovn_localnet_port-to-br-int"
	klog.Infof("Patch port for XDP shared gateway for NAD %s is %s", nc.GetNetworkName(), res.patchPort)

	return &res, nil
}

func setXDPBridgePhysOfPorts(bridge *bridgeConfiguration) error {
	// Get ofport of physical interface
	ofportPhys, stderr, err := util.GetOVSOfPort("get", "interface", bridge.uplinkName, "ofport")
	if err != nil {
		return fmt.Errorf("failed to get ofport of %s, stderr: %q, error: %v",
			bridge.uplinkName, stderr, err)
	}
	bridge.ofPortPhys = ofportPhys

	return nil
}

// XXX We separate this out from phys since, the localnet port could change. Need to take care of
// that in healthcheck.
func setXDPBridgePatchOfPorts(bridge *bridgeConfiguration) error {
	// Get ofport of patchPort
	ofportPatch, stderr, err := util.GetOVSOfPort("--if-exists", "get", "interface", bridge.patchPort, "ofport")
	if err != nil || len(ofportPatch) == 0 {
		return fmt.Errorf("patch port %q not created by ovn-controller: "+
			"stderr: %q, error: %v", bridge.patchPort, stderr, err)
	}

	// Since the localnet patch port could be created/destroyed by ovn-controller as needed,
	// we could have bridge.ofPortPatch set to a diff value. healthcheck will see if the
	// changed value invalidates flows. Here, we don't need to since, either the patch port already
	// exists and should be the same as bridge.ofPortPatch or the patch port doesn't, in which
	// case the new value will differ from bridge.ofPortPatch, but there should be no flows
	// configured. We check just to make sure.
	if bridge.ofPortPatch != "" && bridge.ofPortPatch != ofportPatch {
		xdpCheckPatchPortOFFlows(bridge.bridgeName, bridge.ofPortPhys, bridge.patchPort, bridge.ofPortPatch, ofportPatch)
		klog.Infof("XDP patch port %q changing ofport from %s to %s", bridge.patchPort, bridge.ofPortPatch,
			ofportPatch)
	}
	bridge.ofPortPatch = ofportPatch
	return nil
}

func (nc *SecondaryLocalnetNodeNetworkController) newXDPSharedGateway(isPrimaryDPU bool) (*gateway, error) {
	klog.Infof("Creating new XDP shared gateway for %s", nc.GetNetworkName())
	gw := &gateway{}

	gwBridge, err := nc.bridgeForXDPInterface()
	if err != nil {
		klog.Infof("Failed creating new XDP shared gateway: %v", err)
		return nil, err
	}

	gw.readyFunc = func() (bool, error) {
		klog.Info("Setting patch ports for XDP Shared Gateway Openflow Manager")
		err := setXDPBridgePatchOfPorts(gwBridge)
		if err != nil {
			klog.Infof("Failed setting up  XDP Shared Gateway: %v", err)
			return false, err
		}
		klog.Info("XDP Gateway is ready")
		return true, nil
	}

	gw.initFunc = func() error {
		klog.Info("Setting phys ports for XDP Shared Gateway Openflow Manager")
		err := setXDPBridgePhysOfPorts(gwBridge)
		if err != nil {
			klog.Infof("Failed setting up  XDP Shared Gateway: %v", err)
			return err
		}

		gw.openflowManager, err = nc.newXDPSharedGatewayOpenFlowManager(gwBridge, !isPrimaryDPU)
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

func (nc *SecondaryLocalnetNodeNetworkController) cleanGatewayDPUXDP(gw *gateway) error {

	klog.Infof("Destroying XDP for NAD %s", nc.GetNetworkName())
	err := nc.DestroyXDPServiceForNAD()
	if err != nil {
		klog.Infof("Failed to remove XDP config: %v", err)
	}
	klog.Infof("Removed XDP")

	return nil
}
