package node

import (
	"os"
	"strings"
	"sync"
	"time"

	OFManager "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/openflow-manager"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util"
	"github.com/pkg/errors"

	"k8s.io/klog/v2"
)

type openflowManager struct {
	defaultBridge         *bridgeConfiguration
	externalGatewayBridge *bridgeConfiguration
	defaultBridgeFlowID   string
	extGWBridgeFlowID     string
}

func (c *openflowManager) updateFlowCacheEntry(key string, flows []string) {
	OFManager.OpenFlowCacheManager.UpdateFlowCacheEntry(c.defaultBridgeFlowID, key, flows, false)
}

func (c *openflowManager) deleteFlowsByKey(key string) {
	OFManager.OpenFlowCacheManager.DeleteFlowsByKey(c.defaultBridgeFlowID, key, false)
}

func (c *openflowManager) getFlowCacheEntry(key string) []string {
	return OFManager.OpenFlowCacheManager.GetFlowsByKey(c.defaultBridgeFlowID, key)
}

func (c *openflowManager) updateExBridgeFlowCacheEntry(key string, flows []string) {
	OFManager.OpenFlowCacheManager.UpdateFlowCacheEntry(c.extGWBridgeFlowID, key, flows, false)
}

func (c *openflowManager) requestFlowSync() {
	OFManager.OpenFlowCacheManager.RequestFlowSync(c.defaultBridgeFlowID)
	if c.externalGatewayBridge != nil {
		OFManager.OpenFlowCacheManager.RequestFlowSync(c.extGWBridgeFlowID)
	}
}

// checkDefaultOpenFlow checks for the existence of default OpenFlow rules and
// exits if the output is not as expected
func (c *openflowManager) Run(stopChan <-chan struct{}, doneWg *sync.WaitGroup) {
	doneWg.Add(1)
	go func() {
		defer doneWg.Done()
		syncPeriod := 15 * time.Second
		timer := time.NewTicker(syncPeriod)
		defer timer.Stop()
		for {
			select {
			case <-timer.C:
				if err := checkPorts(c.defaultBridge); err != nil {
					klog.Errorf("Checkports failed %v", err)
					continue
				}
				if c.externalGatewayBridge != nil {
					if err := checkPorts(c.externalGatewayBridge); err != nil {
						klog.Errorf("Checkports failed %v", err)
						continue
					}
				}
			case <-stopChan:
				return
			}
		}
	}()
	OFManager.OpenFlowCacheManager.StartFlowCacheWorker(c.defaultBridgeFlowID)
	OFManager.OpenFlowCacheManager.StartFlowCacheWorker(c.extGWBridgeFlowID)
}

// For XDP gateway the localnet patch port may be deleted and recreated as needed. So, we can't
// always expect the ofPortPatch to agree. If the ofPortPatch changes we just check if there
// are any flows using the ofPortPatch and error out if so; i.e. the localnet is deleted
// but flows using the localnet port are still around.
// However, if the of ports disagree, but there are no flows that use the old of port,
// then it is not an error.
// This assumes ofPortPhys doesn't change, which we'll still consider as fatal.
// For the N/S gateway we should not have a situation where the patch's OF port changes,
// so will make this check specific to localnet ports.
func checkPorts(bridge *bridgeConfiguration) error {
	// it could be that the ovn-controller recreated the patch between the host OVS bridge and
	// the integration bridge, as a result the ofport number changed for that patch interface
	curOfportPatch, stderr, err := util.GetOVSOfPort("--if-exists", "get", "Interface", bridge.patchPort, "ofport")
	if err != nil {
		return errors.Wrapf(err, "Failed to get ofport of %s, stderr: %q", bridge.patchPort, stderr)

	}
	if bridge.ofPortPatch != curOfportPatch {
		// XXX- Maybe, use gateway type
		if strings.Contains(bridge.patchPort, "localnet_port") {
			xdpCheckPatchPortOFFlows(bridge.bridgeName, bridge.ofPortPhys, bridge.patchPort, bridge.ofPortPatch, curOfportPatch)
		} else {
			klog.Errorf("Fatal error: patch port %s ofport changed from %s to %s",
				bridge.patchPort, bridge.ofPortPatch, curOfportPatch)
			os.Exit(1)
		}
	}

	// it could be that someone removed the physical interface and added it back on the OVS host
	// bridge, as a result the ofport number changed for that physical interface
	curOfportPhys, stderr, err := util.GetOVSOfPort("--if-exists", "get", "interface", bridge.uplinkName, "ofport")
	if err != nil {
		return errors.Wrapf(err, "Failed to get ofport of %s, stderr: %q", bridge.uplinkName, stderr)
	}
	if bridge.ofPortPhys != curOfportPhys {
		klog.Errorf("Fatal error: phys port %s ofport changed from %s to %s",
			bridge.uplinkName, bridge.ofPortPhys, curOfportPhys)
		os.Exit(1)
	}
	// it could be ofport number of host representor interface changed
	if bridge.hostRepName != "" {
		curOfportHost, stderr, err := util.GetOVSOfPort("--if-exists", "get", "interface", bridge.hostRepName, "ofport")
		if err != nil {
			return errors.Wrapf(err, "Failed to get ofport of %s, stderr: %q", bridge.hostRepName, stderr)
		}
		if bridge.ofPortHost != curOfportHost {
			klog.Errorf("Fatal error: host representor port %s ofport changed from %s to %s",
				bridge.hostRepName, bridge.ofPortHost, curOfportHost)
			os.Exit(1)
		}
	}
	return nil
}
