// +build linux

package node

import (
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/config"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/ovn"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util"
	"github.com/vishvananda/netlink"
	"k8s.io/klog"
)

func initLocalOnlyGateway(nodeName string, stopChan chan struct{}) (map[string]string, error) {
	// Create a localnet OVS bridge.
	localnetBridgeName := "br-local"
	_, stderr, err := util.RunOVSVsctl("--may-exist", "add-br",
		localnetBridgeName)
	if err != nil {
		return nil, fmt.Errorf("Failed to create localnet bridge %s"+
			", stderr:%s (%v)", localnetBridgeName, stderr, err)
	}

	_, macAddress, err := bridgedGatewayNodeSetup(nodeName, localnetBridgeName, localnetBridgeName,
		util.LocalNetworkName, true)
	if err != nil {
		return nil, fmt.Errorf("failed to set up shared interface gateway: %v", err)
	}

	// Up the localnetBridgeName
	_, err = util.LinkSetUp(localnetBridgeName)
	if err != nil {
		return nil, err
	}

	// Create a localnet bridge nexthop
	_, stderr, err = util.RunOVSVsctl(
		"--if-exists", "del-port", localnetBridgeName, util.LegacyLocalnetGatewayNextHopPort,
		"--", "--may-exist", "add-port", localnetBridgeName, util.LocalnetGatewayNextHopPort,
		"--", "set", "interface", util.LocalnetGatewayNextHopPort, "type=internal",
		"mtu_request="+fmt.Sprintf("%d", config.Default.MTU),
		fmt.Sprintf("mac=%s", strings.ReplaceAll(util.LocalnetGatewayNextHopMac, ":", "\\:")))
	if err != nil {
		return nil, fmt.Errorf("Failed to create localnet bridge next hop %s"+
			", stderr:%s (%v)", util.LocalnetGatewayNextHopPort, stderr, err)
	}
	// Up the util.LocalnetGatewayNextHopPort interface
	link, err := util.LinkSetUp(util.LocalnetGatewayNextHopPort)
	if err != nil {
		return nil, err
	}

	// Flush all IP addresses on util.LocalnetGatewayNextHopPort and add the new IP address
	if err = util.LinkAddrFlush(link); err == nil {
		err = util.LinkAddrAdd(link, util.LocalnetGatewayNextHopSubnet())
	}
	if err != nil {
		return nil, err
	}

	// Add arp entry for local service gateway, it is used for return traffic of local service access
	ip, _, _ := net.ParseCIDR(util.LocalnetGatewayIP())
	if config.IPv6Mode {
		err = util.LinkNeighAdd(link, ip.String(), macAddress)
		if err == nil {
			klog.V(5).Infof("Added MAC binding for %s on %s", util.LocalnetGatewayNextHopPort, ip.String())
		} else {
			klog.Errorf("Error in adding MAC binding for %s on %s: %v", util.LocalnetGatewayNextHopPort, ip.String(), err)
		}
	} else {
		err = util.LinkNeighAdd(link, ip.String(), macAddress)
		if err != nil {
			return nil, err
		}
	}

	// add health check function to check ARP/ND entry for localnet gateway IP
	go checkARPEntryForLocalGatewayIP(link, ip.String(), macAddress, stopChan)
	return map[string]string{
		ovn.OvnNodeLocalGatewayMacAddress: macAddress,
	}, nil
}

// add health check function to check ARP/ND entry for localnet gateway IP
func checkARPEntryForLocalGatewayIP(link netlink.Link, localGwIP, macAddress string, stopChan chan struct{}) {
	ticker := time.NewTicker(30 * time.Second)

	for {
		select {
		case <-ticker.C:
			if exists, err := util.LinkNeighExists(link, localGwIP, macAddress); err == nil {
				if exists {
					continue
				}
				klog.Errorf("missing neighbour entry %s/%s on link %v, adding the entry",
					util.LocalnetGatewayNextHop(), macAddress, link.Attrs().Name)
				err = util.LinkNeighAdd(link, util.LocalnetGatewayNextHop(), macAddress)
				if err != nil {
					klog.Errorf("failed while checking existence of an neighbour entry %s/%s: %v",
						util.LocalnetGatewayNextHop(), macAddress, err)
				}
			} else {
				klog.Errorf(err.Error())
			}
		case <-stopChan:
			return
		}
	}
}
