// +build linux

package node

import (
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/config"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util"
	"github.com/vishvananda/netlink"

	"k8s.io/klog"
	utilnet "k8s.io/utils/net"
)

func initLocalOnlyGateway(nodeName string, subnet *net.IPNet, stopChan chan struct{}) (net.HardwareAddr, error) {
	// Create a localnet OVS bridge.
	localnetBridgeName := "br-local"
	_, stderr, err := util.RunOVSVsctl("--may-exist", "add-br", localnetBridgeName)
	if err != nil {
		return nil, fmt.Errorf("failed to create localnet bridge %s"+
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
		"--if-exists", "del-port", localnetBridgeName, legacyLocalnetGatewayNextHopPort,
		"--", "--may-exist", "add-port", localnetBridgeName, localnetGatewayNextHopPort,
		"--", "set", "interface", localnetGatewayNextHopPort, "type=internal",
		"mtu_request="+fmt.Sprintf("%d", config.Default.MTU),
		fmt.Sprintf("mac=%s", strings.ReplaceAll(localnetGatewayNextHopMac, ":", "\\:")))
	if err != nil {
		return nil, fmt.Errorf("failed to create localnet bridge next hop %s"+
			", stderr:%s (%v)", localnetGatewayNextHopPort, stderr, err)
	}
	// Up the localnetGatewayNextHopPort interface
	link, err := util.LinkSetUp(localnetGatewayNextHopPort)
	if err != nil {
		return nil, err
	}

	var gatewayIP, gatewayNextHop net.IP
	var gatewaySubnetMask net.IPMask
	isSubnetIPv6 := utilnet.IsIPv6CIDR(subnet)
	if isSubnetIPv6 {
		gatewayIP = net.ParseIP(util.V6LocalnetGatewayIP)
		gatewayNextHop = net.ParseIP(util.V6LocalnetGatewayNextHop)
		gatewaySubnetMask = net.CIDRMask(util.V6LocalnetGatewaySubnetPrefix, 128)
	} else {
		gatewayIP = net.ParseIP(util.V4LocalnetGatewayIP)
		gatewayNextHop = net.ParseIP(util.V4LocalnetGatewayNextHop)
		gatewaySubnetMask = net.CIDRMask(util.V4LocalnetGatewaySubnetPrefix, 32)
	}
	gatewayNextHopCIDR := &net.IPNet{IP: gatewayNextHop, Mask: gatewaySubnetMask}

	// Flush all IP addresses on localnetGatewayNextHopPort and add the new IP address
	if err = util.LinkAddrFlush(link); err == nil {
		err = util.LinkAddrAdd(link, gatewayNextHopCIDR)
	}
	if err != nil {
		return nil, err
	}

	// Add arp entry for local service gateway, it is used for return traffic of local service access
	if isSubnetIPv6 {
		err = util.LinkNeighSet(link, gatewayIP, macAddress)
		if err == nil {
			klog.V(5).Infof("Added MAC binding for %s on %s", gatewayIP, localnetGatewayNextHopPort)
		} else {
			klog.Errorf("Error in adding MAC binding for %s on %s: %v", gatewayIP, localnetGatewayNextHopPort, err)
		}
	} else {
		err = util.LinkNeighSet(link, gatewayIP, macAddress)
		if err != nil {
			return nil, err
		}
	}

	// add health check function to check ARP/ND entry for localnet gateway IP
	go checkARPEntryForLocalGatewayIP(link, gatewayIP, macAddress, stopChan)
	return macAddress, nil
}

// add health check function to check ARP/ND entry for localnet gateway IP
func checkARPEntryForLocalGatewayIP(link netlink.Link, localGwIP net.IP, macAddress net.HardwareAddr,
	stopChan chan struct{}) {
	for {
		select {
		case <-time.After(30 * time.Second):
			if exists, err := util.LinkNeighExists(link, localGwIP, macAddress); err == nil {
				if exists {
					continue
				}
				klog.Errorf("Missing neighbour entry %s/%s on link %v, adding the entry",
					localGwIP, macAddress, link.Attrs().Name)
				err = util.LinkNeighAdd(link, localGwIP, macAddress)
				if err != nil {
					klog.Errorf("failed while checking existence of an neighbour entry %s/%s: %v",
						localGwIP, macAddress, err)
				}
			} else {
				klog.Errorf(err.Error())
			}
		case <-stopChan:
			return
		}
	}
}
