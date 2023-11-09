package util

import (
	"encoding/json"
	"fmt"
	"net"
	"reflect"
	"strings"

	nettypes "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/types"
)

const (
	MissRateLimitConfigAnnot = types.OvnK8sPrefix + "/" + "miss-rl-config"
	OvnK8sNADRoutes          = types.OvnK8sPrefix + "/" + "nad-routes"
)

type MissRateLimitConfig struct {
	// Limit on new connections initiated (PPS)
	MaxNewConnPPS uint `json:"maxNewConnPPS,omitempty"`
	// Max Burst of new connections initiated (Packets)
	MaxNewConnBurst uint `json:"maxNewConnBurst,omitempty"`
	// Disable DoS check for this NAD, even if rate is configured
	// Mostly for debugging purposes
	DisableDoSCheck bool `json:"disableDoSCheck,omitempty"`
	// Host types for which this config should be applied
	HostTypes []string `json:"hostTypes,omitempty"`
}

// per nad configuration, currently only rate limit config
type NADConfig struct {
	MissRateLimitConfig
}

func (nc *NADConfig) GetMissRateLimitConfig(hostType string) (uint, uint, bool) {
	if len(nc.HostTypes) == 0 {
		// if HostType is not specified, consider it's applicable to all types
		return nc.MaxNewConnPPS, nc.MaxNewConnBurst, nc.DisableDoSCheck
	}
	for _, ht := range nc.HostTypes {
		if ht == hostType {
			return nc.MaxNewConnPPS, nc.MaxNewConnBurst, nc.DisableDoSCheck
		}
	}
	return 0, 0, false
}

// GetNADConfig returns the nad specific configuration obtained from the net-attach-def annotation
func GetNADConfig(netattachdef *nettypes.NetworkAttachmentDefinition) (*NADConfig, error) {
	// if pkt rate limit annotation does not exist, the configuration for primary network comes from the cli; otherwise,
	// it is the default {0, 0} which means no rate limiting unless pkt rate limit annotation is explicitly specified.
	// DisableDoSCheck is false  by default. Note, if it is 0,0 then dsabledoscheck being false is redundant, but
	// we'll keep it as false by default.
	pktRateLimitCfg := MissRateLimitConfig{0, 0, false, []string{}}
	prlConfAnnotation, ok := netattachdef.Annotations[MissRateLimitConfigAnnot]
	if !ok {
		return &NADConfig{MissRateLimitConfig: pktRateLimitCfg}, nil
	}

	if err := json.Unmarshal([]byte(prlConfAnnotation), &pktRateLimitCfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal %s annotation for net-attach-def %s/%s: %v",
			MissRateLimitConfigAnnot, netattachdef.Namespace, netattachdef.Name, err)
	}

	return &NADConfig{MissRateLimitConfig: pktRateLimitCfg}, nil
}

// IsNADConfSame compares the given two NADConfig and returns true if they are the same
func IsNADConfSame(nadConf1 *NADConfig, nadConf2 *NADConfig) bool {
	if nadConf1 == nil || nadConf2 == nil {
		return true
	}
	if nadConf1 != nil || nadConf2 != nil {
		return reflect.DeepEqual(*nadConf1, *nadConf2)
	}
	return false
}

func getNADRoutesConfig(annotations map[string]string) ([]*net.IPNet, error) {
	nadRoutesAnnot, ok := annotations[OvnK8sNADRoutes]
	if !ok {
		return nil, nil
	}

	routeStrings := []string{}
	if err := json.Unmarshal([]byte(nadRoutesAnnot), &routeStrings); err != nil {
		return nil, fmt.Errorf("failed to unmarshal %s annotation %q: %v", OvnK8sNADRoutes, nadRoutesAnnot, err)
	}

	routes := make([]*net.IPNet, len(routeStrings))
	for i, routeString := range routeStrings {
		routeString = strings.TrimSpace(routeString)
		_, route, err := net.ParseCIDR(routeString)
		if err != nil {
			return nil, fmt.Errorf("invalid NAD routes %s in %s annotation %q: %v", routeString, OvnK8sNADRoutes, nadRoutesAnnot, err)
		}
		routes[i] = route
	}

	return routes, nil
}
