package util

import (
	"encoding/json"
	"fmt"

	nettypes "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/config"
)

const (
	MissRateLimitConfigAnnot = "k8s.ovn.org/miss-rl-config"
)

type MissRateLimitConfig struct {
	// Limit on new connections initiated (PPS)
	MaxNewConnPPS uint `json:"maxNewConnPPS,omitempty"`
	// Max Burst of new connections initiated (Packets)
	MaxNewConnBurst uint `json:"maxNewConnBurst,omitempty"`
}

// per nad configuration, currently only rate limit config
type NadConfig struct {
	MissRateLimitConfig
}

// GetNadConfig returns the nad specific configuration obtained from the net-attach-def annotation
func GetNadConfig(netattachdef *nettypes.NetworkAttachmentDefinition, isSecondary bool) (*NadConfig, error) {
	// if pkt rate limit annotation does not exist, the configuration for primary network comes from the cli; otherwise,
	// it is the default {0, 0} which means no rate limiting unless pkt rate limit annotation is explicitly specified.
	pktRateLimitCfg := MissRateLimitConfig{0, 0}
	if !isSecondary {
		pktRateLimitCfg = MissRateLimitConfig{config.OvnKubeNode.MaxNewConnPPS, config.OvnKubeNode.MaxNewConnBurst}
	}

	prlConfAnnotation, ok := netattachdef.Annotations[MissRateLimitConfigAnnot]
	if !ok {
		return &NadConfig{pktRateLimitCfg}, nil
	}

	if err := json.Unmarshal([]byte(prlConfAnnotation), &pktRateLimitCfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal %s annotation for net-attach-def %s/%s: %v",
			MissRateLimitConfigAnnot, netattachdef.Namespace, netattachdef.Name, err)
	}

	return &NadConfig{pktRateLimitCfg}, nil
}
