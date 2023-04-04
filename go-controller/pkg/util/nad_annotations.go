package util

import (
	"encoding/json"
	"fmt"
	"sync"

	nettypes "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
)

const (
	MissRateLimitConfigAnnot = "k8s.ovn.org/miss-rl-config"
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
