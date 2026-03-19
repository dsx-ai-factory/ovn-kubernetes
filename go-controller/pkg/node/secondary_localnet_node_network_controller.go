package node

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"k8s.io/klog/v2"

	"github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/config"
	"github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/networkmanager"
	"github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/types"
	"github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/util"
)

// TBD-merge rename secondary network to UDN
// UserDefinedNodeNetworkController structure is the object which holds the controls for starting
// and reacting upon the watched resources (e.g. pods, endpoints) for secondary network
type UserDefinedLocalnetNodeNetworkController struct {
	UserDefinedNodeNetworkController
	// bridge name for this localnet network
	bridgeName string
}

// NewUserDefinedLocalnetNodeNetworkController creates a new OVN controller for creating logical network
// infrastructure and policy for user defined localnet network
func NewUserDefinedLocalnetNodeNetworkController(cnnci *CommonNodeNetworkControllerInfo, netInfo util.NetInfo, networkManager networkmanager.Interface) *UserDefinedLocalnetNodeNetworkController {
	return &UserDefinedLocalnetNodeNetworkController{
		UserDefinedNodeNetworkController: UserDefinedNodeNetworkController{
			BaseNodeNetworkController: BaseNodeNetworkController{
				CommonNodeNetworkControllerInfo: *cnnci,
				ReconcilableNetInfo:             util.NewReconcilableNetInfo(netInfo),
				stopChan:                        make(chan struct{}),
				wg:                              &sync.WaitGroup{},
				DoSCheckStopChan:                nil,
				networkManager:                  networkManager,
			},
		},
	}
}

// Start starts the default controller; handles all events and creates all needed logical entities
func (nc *UserDefinedLocalnetNodeNetworkController) Start(_ context.Context) error {
	var err error
	klog.Infof("Start secondary node network controller of network %s", nc.GetNetworkName())

	if config.OvnKubeNode.Mode != types.NodeModeDPUHost {
		err = nc.updateLocalnetOvnBridgeMapping(true)
		if err != nil {
			return err
		}
	}

	if config.OvnKubeNode.Mode == types.NodeModeDPU {
		err := nc.startNADController()
		if err != nil {
			return err
		}

		handler, err := nc.watchPodsDPU()
		if err != nil {
			return err
		}
		nc.podHandler = handler
	}
	return nil
}

// Stop gracefully stops the controller
func (nc *UserDefinedLocalnetNodeNetworkController) Stop() {
	nc.UserDefinedNodeNetworkController.Stop()

	nc.stopNADController()

	if config.OvnKubeNode.Mode != types.NodeModeDPUHost {
		err := nc.updateLocalnetOvnBridgeMapping(false)
		if err != nil {
			klog.Error(err.Error())
		}
	}
}

// Cleanup cleans up node entities for the given secondary network
func (nc *UserDefinedLocalnetNodeNetworkController) Cleanup() error {
	return nil
}

var ovsMutex = sync.Mutex{}

// updateLocalnetOvnBridgeMapping reconciles this network's entry in
// Open_vSwitch.external_ids:ovn-bridge-mappings.
//
// On add (toAdd=true), it discovers whether this node should host the network
// by looking up ngn-localnet-bridge-mappings, and records the selected bridge
// in nc.bridgeName. If no bridge is selected, the network is considered not
// needed on this node and no ovn-bridge-mappings update is performed.
//
// On remove (toAdd=false), it only removes the mapping when nc.bridgeName is
// set, meaning this controller previously managed the mapping on this node.
// This ownership check prevents deleting mappings that were configured by
// external actors (e.g. test harness or bootstrap scripts).
//
// Calls are serialized with ovsMutex because multiple NAD workers can update
// ovn-bridge-mappings concurrently.
func (nc *UserDefinedLocalnetNodeNetworkController) updateLocalnetOvnBridgeMapping(toAdd bool) error {
	// The NAD controller may have multiple workers that call this function concurrently.
	// A mutex is required to prevent concurrent overwrites of external_ids:ovn-bridge-mappings.
	ovsMutex.Lock()
	defer ovsMutex.Unlock()
	bridgeName := ""
	if toAdd {
		// Reset any stale value from a previous run before trying to discover
		// whether this node should manage bridge-mappings for the network.
		nc.bridgeName = ""
		// ngn-localnet-bridge-mappings exernal_ids is in the form of "<network_prefix1>:<br1>,<network_prefix2>:<br2>...".
		// It sets all the possible localnet networks and associated bridge names on this node.
		stdout, stderr, err := util.RunOVSVsctl("--if-exists", "get", "Open_vSwitch", ".",
			"external_ids:ngn-localnet-bridge-mappings")
		if err != nil {
			klog.Warningf("Failed to get ngn-localnet-bridge-mappings from Open_vSwitch table stderr:%s (%v)", stderr, err)
			return nil
		}

		// find the entry that has a complete match. this is to disambiguate two entries that
		// have the same prefix, but different OVS bridges.
		bridgeMapConfs := strings.Split(stdout, ",")
		for _, bridgeMapConf := range bridgeMapConfs {
			maps := strings.Split(bridgeMapConf, ":")
			if len(maps) == 2 && nc.GetNetworkName() == maps[0] {
				bridgeName = maps[1]
				break
			}
		}

		// now find partial match, if required
		if bridgeName == "" {
			for _, bridgeMapConf := range bridgeMapConfs {
				maps := strings.Split(bridgeMapConf, ":")
				if len(maps) == 2 && strings.HasPrefix(nc.GetNetworkName(), maps[0]) {
					bridgeName = maps[1]
					break
				}
			}
		}

		if bridgeName == "" {
			klog.V(5).Infof("Localnet network %s is not needed on this node %s", nc.GetNetworkName(), nc.name)
			return nil
		}
		klog.V(5).Infof("Set bridge %s for localnet network %s", bridgeName, nc.GetNetworkName())
		nc.bridgeName = bridgeName
	} else if nc.bridgeName == "" {
		// If this node never selected a bridge for this network, the mapping was
		// not managed by this controller and should not be removed on stop.
		klog.V(5).Infof("Skip removing bridge mapping for localnet network %s on node %s: not managed by this controller", nc.GetNetworkName(), nc.name)
		return nil
	}

	// ovn-bridge-mappings maps a physical network name to a local ovs bridge
	// that provides connectivity to that network. It is in the form of physnet1:br1,physnet2:br2.
	// Note that there may be multiple ovs bridge mappings, be sure not to override
	// the mappings for the other physical network

	// NVIDIA: add br-localnet suffix to retain original downstream behavior. Otherwise we'd have to
	// change bridge mappings on all nodes to use the new suffix-less names.
	networkName := nc.GetNetworkScopedName(types.LocalNetBridgeName)
	stdout, stderr, err := util.RunOVSVsctl("--if-exists", "get", "Open_vSwitch", ".",
		"external_ids:ovn-bridge-mappings")
	if err != nil {
		return fmt.Errorf("failed to get ovn-bridge-mappings stderr:%s (%v)", stderr, err)
	}

	bridgeMap := map[string]string{}
	bridgeMappings := strings.Split(stdout, ",")
	for _, bridgeMapping := range bridgeMappings {
		m := strings.Split(bridgeMapping, ":")
		if len(m) == 2 {
			bridgeMap[m[0]] = m[1]
		}
	}

	bridge, ok := bridgeMap[networkName]
	if toAdd {
		if ok && bridge == bridgeName {
			return nil
		}
		bridgeMap[networkName] = bridgeName
	} else {
		if !ok {
			return nil
		}
		delete(bridgeMap, networkName)
	}

	if len(bridgeMap) == 0 {
		return nil
	}

	mapString := ""
	for networkName, bridge = range bridgeMap {
		if len(mapString) != 0 {
			mapString += ","
		}
		mapString = mapString + networkName + ":" + bridge
	}

	_, stderr, err = util.RunOVSVsctl("set", "Open_vSwitch", ".",
		fmt.Sprintf("external_ids:ovn-bridge-mappings=%s", mapString))
	if err != nil {
		return fmt.Errorf("failed to set ovn-bridge-mappings %s, stderr:%s (%v)", mapString, stderr, err)
	}
	return nil
}
