package util

import (
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync"

	nettypes "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/types"
)

const (
	MissRateLimitConfigAnnot = "k8s.ovn.org/miss-rl-config"
	NADRoutesAnnot           = "k8s.ovn.org/nad-routes"
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
type NadConfig struct {
	sync.RWMutex
	MissRateLimitConfig
}

func (nc *NadConfig) GetMissRateLimitConfig(hostType string) (uint, uint, bool) {
	nc.RLock()
	defer nc.RUnlock()
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

// GetNadConfig returns the nad specific configuration obtained from the net-attach-def annotation
func GetNadConfig(netattachdef *nettypes.NetworkAttachmentDefinition, isSecondary bool) (*NadConfig, error) {
	// if pkt rate limit annotation does not exist, the configuration for primary network comes from the cli; otherwise,
	// it is the default {0, 0} which means no rate limiting unless pkt rate limit annotation is explicitly specified.
	// DisableDoSCheck is false  by default. Note, if it is 0,0 then dsabledoscheck being false is redundant, but
	// we'll keep it as false by default.
	pktRateLimitCfg := MissRateLimitConfig{0, 0, false, []string{}}
	prlConfAnnotation, ok := netattachdef.Annotations[MissRateLimitConfigAnnot]
	if !ok {
		return &NadConfig{MissRateLimitConfig: pktRateLimitCfg}, nil
	}

	if err := json.Unmarshal([]byte(prlConfAnnotation), &pktRateLimitCfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal %s annotation for net-attach-def %s/%s: %v",
			MissRateLimitConfigAnnot, netattachdef.Namespace, netattachdef.Name, err)
	}

	return &NadConfig{MissRateLimitConfig: pktRateLimitCfg}, nil
}

// GetNADNetConfig returns the network specific configuration obtained from the net-attach-def annotation
func GetNADNetConfig(netattachdef *nettypes.NetworkAttachmentDefinition, nadInfo *NetAttachDefInfo) error {
	nadRoutesAnnot, ok := netattachdef.Annotations[NADRoutesAnnot]
	if !ok {
		return nil
	}

	if nadInfo.TopoType == types.LocalnetAttachDefTopoType && nadInfo.Gateway == "" {
		return fmt.Errorf("missing Gateway config in the localnet NAD %s/%s", netattachdef.Namespace, netattachdef.Name)
	}

	if nadInfo.TopoType == types.Layer2AttachDefTopoType && nadInfo.ConnectToNad == "" {
		return fmt.Errorf("missing connectToNAD config in the layer2 NAD %s/%s", netattachdef.Namespace, netattachdef.Name)
	}

	routeStrings := []string{}
	if err := json.Unmarshal([]byte(nadRoutesAnnot), &routeStrings); err != nil {
		return fmt.Errorf("failed to unmarshal %s annotation %q of NAD %s/%s: %v",
			NADRoutesAnnot, nadRoutesAnnot, netattachdef.Namespace, netattachdef.Name, err)
	}

	routes := make([]*net.IPNet, len(routeStrings))
	for i, routeString := range routeStrings {
		routeString = strings.TrimSpace(routeString)
		_, route, err := net.ParseCIDR(routeString)
		if err != nil {
			return fmt.Errorf("invalid NAD routes %s in %s annotation %q of NAD %s/%s: %v",
				routeString, NADRoutesAnnot, nadRoutesAnnot, netattachdef.Namespace, netattachdef.Name, err)
		}
		routes[i] = route
	}

	nadInfo.NADRoutes = routes
	return nil
}

func AreNADRoutesSame(routes1, routes2 []*net.IPNet) bool {
	if len(routes1) != len(routes2) {
		return false
	}
	routeStrings1 := make([]string, len(routes1))
	routeStrings2 := make([]string, len(routes2))

	for i := 0; i < len(routes1); i++ {
		routeStrings1[i] = routes1[i].String()
		routeStrings2[i] = routes2[i].String()
	}
	return IsStringListEqual(routeStrings1, routeStrings2)
}
