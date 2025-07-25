package node

import (
	"fmt"
	"net"
	"strings"
	"time"

	"k8s.io/klog/v2"
	utilnet "k8s.io/utils/net"

	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/config"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/kube"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util"
)

const extIDCustomGatewaySnatRules = "custom-gwsnat-rules"

func (nc *DefaultNodeNetworkController) pollCustomGatewaySnatRules(nodeAnnotator kube.Annotator) {
	timer := time.NewTicker(60 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-timer.C:
			snatRulesString, err := getCustomGwSnatRules()
			if err != nil {
				klog.Errorf("Failed to look up %s: %v", extIDCustomGatewaySnatRules, err)
				continue
			}
			if snatRulesString == config.Gateway.CustomSnatRules {
				continue
			}
			snatRules, err := parseCustomSnatRules(snatRulesString)
			if err != nil {
				klog.Errorf("Failed to parse custom-gwsnat-rules %s: %v", snatRulesString, err)
				continue
			}
			err = nc.updateL3GatewayConfig(nodeAnnotator, snatRules)
			if err != nil {
				klog.Errorf("Failed to update L3 gateway config for node %s: %v", nc.name, err)
				continue
			}
			config.Gateway.CustomSnatRules = snatRulesString
		case <-nc.stopChan:
			klog.Info("Stopping custom-gwsnat-rules polling routine")
		}
	}
}

func (nc *DefaultNodeNetworkController) updateL3GatewayConfig(nodeAnnotator kube.Annotator, snatRules []*util.GWSNATRule) error {
	node, err := nc.watchFactory.GetNode(nc.name)
	if err != nil {
		return fmt.Errorf("error retrieving node %s: %v", nc.name, err)
	}
	l3GwConfig, err := util.ParseNodeL3GatewayAnnotation(node)
	if err != nil {
		return err
	}
	l3GwConfig.GWSNATRules = snatRules
	if err := util.SetL3GatewayConfig(nodeAnnotator, l3GwConfig); err != nil {
		return err
	}
	return nodeAnnotator.Run()
}

func getCustomGwSnatRules() (string, error) {
	gwSnatRules, stderr, err := util.RunOVSVsctl("--if-exists", "get", "Open_vSwitch", ".", fmt.Sprintf("external_ids:%s", extIDCustomGatewaySnatRules))
	if err != nil {
		return "", fmt.Errorf("stderr: %q, error: %v", stderr, err)
	}
	return gwSnatRules, nil
}

func parseCustomSnatRules(customSnatRules string) ([]*util.GWSNATRule, error) {
	customSnatRules = strings.Trim(customSnatRules, "\" ")
	if customSnatRules == "" {
		return nil, nil
	}
	snatRules := []*util.GWSNATRule{}
	extIPMap := map[string]bool{}
	for _, rule := range strings.Split(customSnatRules, ";") {
		if strings.TrimSpace(rule) == "" {
			continue
		}
		segs := strings.Split(rule, "=")
		if len(segs) != 2 {
			return nil, fmt.Errorf("invalid custom snat rule: %s", rule)
		}
		extIP := net.ParseIP(segs[0])
		if extIP == nil {
			return nil, fmt.Errorf("invalid external IP %s in snat rule: %s", segs[0], rule)
		}
		if _, ok := extIPMap[segs[0]]; ok {
			return nil, fmt.Errorf("duplicate external IP %s found in custom snat rules: %s", segs[0], customSnatRules)
		}
		destinations := []*net.IPNet{}
		for _, dest := range strings.Split(segs[1], ",") {
			dest = strings.TrimSpace(dest)
			if !strings.Contains(dest, "/") {
				if utilnet.IsIPv4String(dest) {
					dest += "/32"
				} else if utilnet.IsIPv6String(dest) {
					dest += "/128"
				} else {
					return nil, fmt.Errorf("invalid IP %v", dest)
				}
			}
			if _, ipnet, err := net.ParseCIDR(strings.TrimSpace(dest)); err == nil {
				destinations = append(destinations, ipnet)
			} else {
				return nil, fmt.Errorf("failed to parse ipnet %s: %v", dest, err)
			}
		}
		if len(destinations) == 0 {
			return nil, fmt.Errorf("empty destination IPs for snat ip %s", segs[0])
		}
		snatRules = append(snatRules, &util.GWSNATRule{
			ExternalIP:   extIP,
			Destinations: destinations,
		})
		extIPMap[segs[0]] = true
	}
	return snatRules, nil
}
