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
	// Host types for which this config should be applied
	HostTypes []string `json:"hostTypes,omitempty"`
}

// per nad configuration, currently only rate limit config
type NadConfig struct {
	sync.RWMutex
	MissRateLimitConfig
}

func (nc *NadConfig) GetMissRateLimitConfig(hostType string) (uint, uint) {
	nc.RLock()
	defer nc.RUnlock()
	if len(nc.HostTypes) == 0 {
		// if HostType is not specified, consider it's applicable to all types
		return nc.MaxNewConnPPS, nc.MaxNewConnBurst
	}
	for _, ht := range nc.HostTypes {
		if ht == hostType {
			return nc.MaxNewConnPPS, nc.MaxNewConnBurst
		}
	}
	return 0, 0
}

// GetNadConfig returns the nad specific configuration obtained from the net-attach-def annotation
func GetNadConfig(netattachdef *nettypes.NetworkAttachmentDefinition, isSecondary bool) (*NadConfig, error) {
	// if pkt rate limit annotation does not exist, the configuration for primary network comes from the cli; otherwise,
	// it is the default {0, 0} which means no rate limiting unless pkt rate limit annotation is explicitly specified.
	pktRateLimitCfg := MissRateLimitConfig{0, 0, []string{}}
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
