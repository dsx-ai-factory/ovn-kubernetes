package util

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	kapi "k8s.io/api/core/v1"
	knet "k8s.io/utils/net"

	nettypes "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	ovncnitypes "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/cni/types"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/config"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/nbdb"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/types"
)

var (
	ErrorAttachDefNotOvnManaged = errors.New("net-attach-def not managed by OVN")
	UnsupportedIPAMKeyError     = errors.New("IPAM key is not supported. Use OVN-K provided IPAM via the `subnets` attribute")
)

type InterConnectInfo struct {
	NetName string
	// the logical entity to connect to, it is either a logical router or a logical switch
	LogicalEntityToConnect interface{}
	// the subnets of this network to reach for, routes need to be added to the Pod of other network inter-connected with
	Subnets []config.CIDRNetworkEntry
}

// BasicNetInfo is interface which holds basic network information
type BasicNetInfo interface {
	// basic network information
	GetNetworkName() string
	IsSecondary() bool
	GetAnnotationKey(string) string
	TopologyType() string
	MTU() int
	IPMode() (bool, bool)
	Subnets() []config.CIDRNetworkEntry
	ExcludeSubnets() []*net.IPNet
	Vlan() uint
	Gateway() string
	GatewayMAC() string
	XDPService() bool
	NADRoutes() []*net.IPNet

	// inter-connection information
	NADToInterConnect() string
	GetInterConnectInfo() *InterConnectInfo

	// utility methods
	CompareNetInfo(BasicNetInfo) bool
	GetNetworkScopedName(name string) string
	RemoveNetworkScopeFromName(name string) string
}

// NetInfo correlates which NADs refer to a network in addition to the basic
// network information
type NetInfo interface {
	BasicNetInfo
	AddNAD(string, *NADConfig)
	DeleteNAD(nadName string)
	HasNAD(nadName string) bool
}

type DefaultNetInfo struct {
	// all net-attach-def NAD names for this network, used to determine if a pod needs
	// to be plumbed for this network
	nadNames sync.Map
}

//var DefaultNetworkNetInfo = &DefaultNetInfo{}

// GetNetworkName returns the network name
func (nInfo *DefaultNetInfo) GetNetworkName() string {
	return types.DefaultNetworkName
}

// IsSecondary returns if this network is secondary
func (nInfo *DefaultNetInfo) IsSecondary() bool {
	return false
}

func (nInfo *DefaultNetInfo) GetAnnotationKey(nadName string) string {
	return types.DefaultNetworkName
}

// GetNetworkScopedName returns a network scoped name form the provided one
// appropriate to use globally.
func (nInfo *DefaultNetInfo) GetNetworkScopedName(name string) string {
	// for the default network, names are not scoped
	return name
}

func (nInfo *DefaultNetInfo) RemoveNetworkScopeFromName(name string) string {
	// for the default network, names are not scoped
	return name
}

// AddNAD adds the specified NAD, no op for default network
func (nInfo *DefaultNetInfo) AddNAD(nadName string, nadConf *NADConfig) {
	nInfo.nadNames.Store(nadName, nadConf)
}

// DeleteNAD deletes the specified NAD, no op for default network
func (nInfo *DefaultNetInfo) DeleteNAD(nadName string) {
	nInfo.nadNames.Delete(nadName)
}

// HasNAD returns true if the given NAD exists
func (nInfo *DefaultNetInfo) HasNAD(nadName string) bool {
	_, ok := nInfo.nadNames.Load(nadName)
	return ok
}

func (nInfo *DefaultNetInfo) CompareNetInfo(netBasicInfo BasicNetInfo) bool {
	_, ok := netBasicInfo.(*DefaultNetInfo)
	return ok
}

// TopologyType returns the defaultNetConfInfo's topology type which is empty
func (nInfo *DefaultNetInfo) TopologyType() string {
	// TODO(trozet): optimize other checks using this function after changing default network type from "" -> L3
	return types.Layer3Topology
}

// MTU returns the defaultNetConfInfo's MTU value
func (nInfo *DefaultNetInfo) MTU() int {
	return config.Default.MTU
}

// IPMode returns the defaultNetConfInfo's ipv4/ipv6 mode
func (nInfo *DefaultNetInfo) IPMode() (bool, bool) {
	return config.IPv4Mode, config.IPv6Mode
}

// Subnets returns the defaultNetConfInfo's Subnets value
func (nInfo *DefaultNetInfo) Subnets() []config.CIDRNetworkEntry {
	return config.Default.ClusterSubnets
}

// ExcludeSubnets returns the defaultNetConfInfo's ExcludeSubnets value
func (nInfo *DefaultNetInfo) ExcludeSubnets() []*net.IPNet {
	return nil
}

// Vlan returns the defaultNetConfInfo's Vlan value
func (nInfo *DefaultNetInfo) Vlan() uint {
	return config.Gateway.VLANID
}

// Gateway returns the defaultNetConfInfo's Gateway value
func (nInfo *DefaultNetInfo) Gateway() string {
	panic("unexpected call for default network")
}

// GatewayMAC returns the defaultNetConfInfo's GatewayMAC value
func (nInfo *DefaultNetInfo) GatewayMAC() string {
	panic("unexpected call for default network")
}

// XDPService returns the defaultNetConfInfo's XDPService value
func (nInfo *DefaultNetInfo) XDPService() bool {
	panic("unexpected call for default network")
}

// NADRoutes returns the defaultNetConfInfo's NADRoutes value
func (nInfo *DefaultNetInfo) NADRoutes() []*net.IPNet {
	panic("unexpected call for default network")
}

// NADToInterConnect returns the NAD this network is requested to inter-connected with
func (nInfo *DefaultNetInfo) NADToInterConnect() string {
	return ""
}

// InterConnectInfo returns the information used by inter-connection
func (nInfo *DefaultNetInfo) GetInterConnectInfo() *InterConnectInfo {
	logicalRouter := &nbdb.LogicalRouter{
		Name: GetClusterScopedName(types.OVNClusterRouter),
	}

	return &InterConnectInfo{
		NetName:                nInfo.GetNetworkName(),
		LogicalEntityToConnect: logicalRouter,
		Subnets:                config.Default.ClusterSubnets,
	}
}

// SecondaryNetInfo holds the network name information for secondary network if non-nil
type secondaryNetInfo struct {
	netName    string
	topology   string
	mtu        int
	vlan       uint
	gateway    string
	gatewayMAC string
	xdpService bool
	nadRoutes  []*net.IPNet

	// layer2 topology only
	connectToNAD string

	ipv4mode, ipv6mode bool
	subnets            []config.CIDRNetworkEntry
	excludeSubnets     []*net.IPNet

	// all net-attach-def NAD names for this network, used to determine if a pod needs
	// to be plumbed for this network
	nadNames sync.Map
}

// GetNetworkName returns the network name
func (nInfo *secondaryNetInfo) GetNetworkName() string {
	return nInfo.netName
}

// IsSecondary returns if this network is secondary
func (nInfo *secondaryNetInfo) IsSecondary() bool {
	return true
}

func (nInfo *secondaryNetInfo) GetAnnotationKey(nadName string) string {
	return nadName
}

// GetNetworkScopedName returns a network scoped name from the provided one
// appropriate to use globally.
func (nInfo *secondaryNetInfo) GetNetworkScopedName(name string) string {
	return fmt.Sprintf("%s%s", nInfo.getPrefix(), name)
}

// RemoveNetworkScopeFromName removes the name without the network scope added
// by a previous call to GetNetworkScopedName
func (nInfo *secondaryNetInfo) RemoveNetworkScopeFromName(name string) string {
	// for the default network, names are not scoped
	return strings.Trim(name, nInfo.getPrefix())
}

// getPrefix returns if the logical entities prefix for this network
func (nInfo *secondaryNetInfo) getPrefix() string {
	return GetSecondaryNetworkPrefix(nInfo.netName)
}

// AddNAD adds the specified NAD
func (nInfo *secondaryNetInfo) AddNAD(nadName string, nadConf *NADConfig) {
	nInfo.nadNames.Store(nadName, nadConf)
}

// DeleteNAD deletes the specified NAD
func (nInfo *secondaryNetInfo) DeleteNAD(nadName string) {
	nInfo.nadNames.Delete(nadName)
}

// HasNAD returns true if the given NAD exists, used
// to check if the network needs to be plumbed over
func (nInfo *secondaryNetInfo) HasNAD(nadName string) bool {
	_, ok := nInfo.nadNames.Load(nadName)
	return ok
}

// TopologyType returns the topology type
func (nInfo *secondaryNetInfo) TopologyType() string {
	return nInfo.topology
}

// MTU returns the layer3NetConfInfo's MTU value
func (nInfo *secondaryNetInfo) MTU() int {
	return nInfo.mtu
}

// Vlan returns the Vlan value
func (nInfo *secondaryNetInfo) Vlan() uint {
	return nInfo.vlan
}

// IPMode returns the ipv4/ipv6 mode
func (nInfo *secondaryNetInfo) IPMode() (bool, bool) {
	return nInfo.ipv4mode, nInfo.ipv6mode
}

// Subnets returns the Subnets value
func (nInfo *secondaryNetInfo) Subnets() []config.CIDRNetworkEntry {
	return nInfo.subnets
}

// ExcludeSubnets returns the ExcludeSubnets value
func (nInfo *secondaryNetInfo) ExcludeSubnets() []*net.IPNet {
	return nInfo.excludeSubnets
}

// Gateway returns the Gateway value
func (nInfo *secondaryNetInfo) Gateway() string {
	return nInfo.gateway
}

// GatewayMAC returns the GatewayMAC value
func (nInfo *secondaryNetInfo) GatewayMAC() string {
	return nInfo.gatewayMAC
}

// XDPService returns the XDPService value
func (nInfo *secondaryNetInfo) XDPService() bool {
	return nInfo.xdpService
}

// NADRoutes returns the NADRoutes value
func (nInfo *secondaryNetInfo) NADRoutes() []*net.IPNet {
	return nInfo.nadRoutes
}

// NADToInterConnect returns the NAD this network is requested to inter-connected with
func (nInfo *secondaryNetInfo) NADToInterConnect() string {
	return nInfo.connectToNAD
}

// GetInterConnectInfo returns the information used by inter-connection
func (nInfo *secondaryNetInfo) GetInterConnectInfo() *InterConnectInfo {
	switch nInfo.topology {
	case types.Layer3Topology:
		return &InterConnectInfo{
			NetName:                nInfo.GetNetworkName(),
			LogicalEntityToConnect: &nbdb.LogicalRouter{Name: GetClusterScopedName(nInfo.GetNetworkScopedName(types.OVNClusterRouter))},
			Subnets:                nInfo.Subnets(),
		}
	case types.Layer2Topology:
		return &InterConnectInfo{
			NetName:                nInfo.GetNetworkName(),
			LogicalEntityToConnect: &nbdb.LogicalSwitch{Name: GetClusterScopedName(nInfo.GetNetworkScopedName(types.OVNLayer2Switch))},
			Subnets:                nInfo.Subnets(),
		}
	}
	return nil
}

// CompareNetInfo compares for equality this network information with the other
func (nInfo *secondaryNetInfo) CompareNetInfo(other BasicNetInfo) bool {
	if nInfo.netName != other.GetNetworkName() {
		return false
	}
	if nInfo.topology != other.TopologyType() {
		return false
	}
	if nInfo.mtu != other.MTU() {
		return false
	}
	if nInfo.vlan != other.Vlan() {
		return false
	}
	if nInfo.gateway != other.Gateway() {
		return false
	}
	if nInfo.gatewayMAC != other.GatewayMAC() {
		return false
	}
	if nInfo.xdpService != other.XDPService() {
		return false
	}
	if nInfo.connectToNAD != other.NADToInterConnect() {
		return false
	}

	lessCIDRNetworkEntry := func(a, b config.CIDRNetworkEntry) bool { return a.String() < b.String() }
	if !cmp.Equal(nInfo.subnets, other.Subnets(), cmpopts.SortSlices(lessCIDRNetworkEntry)) {
		return false
	}

	lessIPNet := func(a, b net.IPNet) bool { return a.String() < b.String() }
	if !cmp.Equal(nInfo.nadRoutes, other.NADRoutes(), cmpopts.SortSlices(lessIPNet)) {
		return false
	}

	return cmp.Equal(nInfo.excludeSubnets, other.ExcludeSubnets(), cmpopts.SortSlices(lessIPNet))
}

func newLayer3NetConfInfo(netconf *ovncnitypes.NetConf, annotations map[string]string) (NetInfo, error) {
	subnets, _, err := parseSubnets(netconf.Subnets, "", types.Layer3Topology)
	if err != nil {
		return nil, err
	}

	ni := &secondaryNetInfo{
		netName:  netconf.Name,
		topology: types.Layer3Topology,
		subnets:  subnets,
		mtu:      netconf.MTU,
	}
	ni.nadRoutes, err = getNADRoutesConfig(annotations)
	if err != nil {
		return nil, err
	}
	ni.ipv4mode, ni.ipv6mode = getIPMode(subnets)
	return ni, nil
}

func newLayer2NetConfInfo(netconf *ovncnitypes.NetConf, annotations map[string]string) (NetInfo, error) {
	subnets, excludes, err := parseSubnets(netconf.Subnets, netconf.ExcludeSubnets, types.Layer2Topology)
	if err != nil {
		return nil, fmt.Errorf("invalid %s netconf %s: %v", netconf.Topology, netconf.Name, err)
	}

	nad := annotations[types.OvnK8sConnectToNad]

	ni := &secondaryNetInfo{
		netName:        netconf.Name,
		topology:       types.Layer2Topology,
		subnets:        subnets,
		excludeSubnets: excludes,
		mtu:            netconf.MTU,
		connectToNAD:   nad,
	}
	ni.nadRoutes, err = getNADRoutesConfig(annotations)
	if err != nil {
		return nil, err
	}
	if ni.nadRoutes != nil && ni.connectToNAD == "" {
		return nil, fmt.Errorf("invalid %s configuration as the %s annotation is missing in the %s NAD %s",
			OvnK8sNADRoutes, types.OvnK8sConnectToNad, netconf.Topology, netconf.NADName)
	}
	ni.ipv4mode, ni.ipv6mode = getIPMode(subnets)
	return ni, nil
}

func newLocalnetNetConfInfo(netconf *ovncnitypes.NetConf, annotations map[string]string) (NetInfo, error) {
	subnets, excludes, err := parseSubnets(netconf.Subnets, netconf.ExcludeSubnets, types.LocalnetTopology)
	if err != nil {
		return nil, fmt.Errorf("invalid %s netconf %s: %v", netconf.Topology, netconf.Name, err)
	}

	ni := &secondaryNetInfo{
		netName:        netconf.Name,
		topology:       types.LocalnetTopology,
		subnets:        subnets,
		excludeSubnets: excludes,
		mtu:            netconf.MTU,
		vlan:           uint(netconf.VLANID),
		gateway:        netconf.Gateway,
		gatewayMAC:     netconf.GatewayMAC,
		xdpService:     netconf.XDPService,
	}
	ni.nadRoutes, err = getNADRoutesConfig(annotations)
	if err != nil {
		return nil, err
	}

	if ni.nadRoutes != nil && ni.gateway == "" {
		return nil, fmt.Errorf("invalid %s configuration as the Gateway config is missing in the %s NAD %s",
			OvnK8sNADRoutes, netconf.Topology, netconf.NADName)
	}

	ni.ipv4mode, ni.ipv6mode = getIPMode(subnets)
	return ni, nil
}

func parseSubnets(subnetsString, excludeSubnetsString, topology string) ([]config.CIDRNetworkEntry, []*net.IPNet, error) {
	var parseSubnets func(clusterSubnetCmd string) ([]config.CIDRNetworkEntry, error)

	switch topology {
	case types.Layer3Topology:
		// For L3 topology, subnet is validated
		parseSubnets = config.ParseClusterSubnetEntries
	case types.LocalnetTopology, types.Layer2Topology:
		// For L2 topologies, host specific prefix length is ignored (using 0 as
		// prefix length)
		parseSubnets = func(clusterSubnetCmd string) ([]config.CIDRNetworkEntry, error) {
			return config.ParseClusterSubnetEntriesWithDefaults(clusterSubnetCmd, 0, 0)
		}
	}

	var subnets []config.CIDRNetworkEntry
	if strings.TrimSpace(subnetsString) != "" {
		var err error
		subnets, err = parseSubnets(subnetsString)
		if err != nil {
			return nil, nil, err
		}
		config.SortClusterSubnetEntries(subnets)
	}

	var excludeIPNets []*net.IPNet
	if strings.TrimSpace(excludeSubnetsString) != "" {
		// For L2 topologies, host specific prefix length is ignored (using 0 as
		// prefix length)
		excludeSubnets, err := config.ParseClusterSubnetEntriesWithDefaults(excludeSubnetsString, 0, 0)
		if err != nil {
			return nil, nil, err
		}
		excludeIPNets = make([]*net.IPNet, 0, len(excludeSubnets))
		for _, excludeSubnet := range excludeSubnets {
			found := false
			for _, subnet := range subnets {
				if ContainsCIDR(subnet.CIDR, excludeSubnet.CIDR) {
					found = true
					break
				}
			}
			if !found {
				return nil, nil, fmt.Errorf("the provided network subnets %v do not contain exluded subnets %v",
					subnets, excludeSubnet.CIDR)
			}
			excludeIPNets = append(excludeIPNets, excludeSubnet.CIDR)
		}
	}

	return subnets, excludeIPNets, nil
}

func getIPMode(subnets []config.CIDRNetworkEntry) (bool, bool) {
	var ipv6Mode, ipv4Mode bool
	for _, subnet := range subnets {
		if knet.IsIPv6CIDR(subnet.CIDR) {
			ipv6Mode = true
		} else {
			ipv4Mode = true
		}
	}
	return ipv4Mode, ipv6Mode
}

// GetNADName returns key of NetAttachDefInfo.NetAttachDefs map, also used as Pod annotation key
func GetNADName(namespace, name string) string {
	return fmt.Sprintf("%s/%s", namespace, name)
}

// GetSecondaryNetworkPrefix gets the string used as prefix of the logical entities
// of the secondary network of the given network name, in the form of <netName>_.
//
// Note that for port_group and address_set, it does not allow the '-' character,
// which will be replaced with ".". Also replace "/" in the nadName with "."
func GetSecondaryNetworkPrefix(netName string) string {
	name := strings.ReplaceAll(netName, "-", ".")
	name = strings.ReplaceAll(name, "/", ".")
	return name + "_"
}

// ParseNADInfo parses config in NAD spec and return a NetAttachDefInfo object for secondary networks
func ParseNADInfo(netattachdef *nettypes.NetworkAttachmentDefinition) (NetInfo, *NADConfig, error) {
	netconf, err := ParseNetConf(netattachdef)
	if err != nil {
		return nil, nil, err
	}

	nadConfig, err := GetNADConfig(netattachdef)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to Get per-NAD Config for net-attach-def %s/%s: %v",
			netattachdef.Namespace, netattachdef.Name, err)
	}

	ni, err := NewNetInfo(netconf, netattachdef.Annotations)
	if err != nil {
		return nil, nil, err
	}

	return ni, nadConfig, nil
}

func NewNetInfo(netconf *ovncnitypes.NetConf, annotation map[string]string) (NetInfo, error) {
	if netconf.Name == types.DefaultNetworkName {
		return &DefaultNetInfo{}, nil
	}
	switch netconf.Topology {
	case types.Layer3Topology:
		return newLayer3NetConfInfo(netconf, annotation)
	case types.Layer2Topology:
		return newLayer2NetConfInfo(netconf, annotation)
	case types.LocalnetTopology:
		return newLocalnetNetConfInfo(netconf, annotation)
	default:
		// other topology NAD can be supported later
		return nil, fmt.Errorf("topology %s not supported", netconf.Topology)
	}
}

// ParseNetConf parses config in NAD spec for secondary networks
func ParseNetConf(netattachdef *nettypes.NetworkAttachmentDefinition) (*ovncnitypes.NetConf, error) {
	netconf, err := config.ParseNetConf([]byte(netattachdef.Spec.Config))
	if err != nil {
		return nil, fmt.Errorf("error parsing Network Attachment Definition %s/%s: %v", netattachdef.Namespace, netattachdef.Name, err)
	}

	if netconf.Name != types.DefaultNetworkName {
		nadName := GetNADName(netattachdef.Namespace, netattachdef.Name)
		if netconf.NADName != nadName {
			return nil, fmt.Errorf("net-attach-def name (%s) is inconsistent with config (%s)", nadName, netconf.NADName)
		}
	}

	if netconf.IPAM.Type != "" {
		return nil, fmt.Errorf("error parsing Network Attachment Definition %s/%s: %v", netattachdef.Namespace, netattachdef.Name, UnsupportedIPAMKeyError)
	}

	return netconf, nil
}

var defaultOVNPrimary = nettypes.NetworkSelectionElement{
	Namespace: "default",
	Name:      "ovn-primary",
}

// GetPodNADToNetworkMapping sees if the given pod needs to plumb over this given network specified by netconf,
// and return the matching NetworkSelectionElement if any exists.
//
// Return value:
//
//	bool: if this Pod is on this Network; true or false
//	map[string]*nettypes.NetworkSelectionElement: all NetworkSelectionElement that pod is requested
//	    for the specified network, key is NADName. Note multiple NADs of the same network are allowed
//	    on one pod, as long as they are of different NADName.
//	error:  error in case of failure
func GetPodNADToNetworkMapping(pod *kapi.Pod, nInfo NetInfo) (bool, map[string]*nettypes.NetworkSelectionElement, error) {
	if pod.Spec.HostNetwork {
		return false, nil, nil
	}

	networkSelections := map[string]*nettypes.NetworkSelectionElement{}
	podDesc := fmt.Sprintf("%s/%s", pod.Namespace, pod.Name)
	if !nInfo.IsSecondary() {
		network, err := GetK8sPodDefaultNetworkSelection(pod)
		if err != nil {
			// multus won't add this Pod if this fails, should never happen
			return false, nil, fmt.Errorf("error getting default-network's network-attachment for pod %s: %v", podDesc, err)
		}
		if network == nil {
			network = &defaultOVNPrimary
		}
		nadName := GetNADName(network.Namespace, network.Name)
		if !nInfo.HasNAD(nadName) {
			return false, nil, nil
		}
		networkSelections[nadName] = network
		return true, networkSelections, nil
	}

	// For non-default network controller, try to see if its name exists in the Pod's k8s.v1.cni.cncf.io/networks, if no,
	// return false;
	allNetworks, err := GetK8sPodAllNetworkSelections(pod)
	if err != nil {
		return false, nil, err
	}

	for _, network := range allNetworks {
		nadName := GetNADName(network.Namespace, network.Name)
		if nInfo.HasNAD(nadName) {
			if _, ok := networkSelections[nadName]; ok {
				return false, nil, fmt.Errorf("unexpected error: more than one of the same NAD %s specified for pod %s",
					nadName, podDesc)
			}
			networkSelections[nadName] = network
		}
	}
	if len(networkSelections) == 0 {
		return false, nil, nil
	}
	return true, networkSelections, nil
}

func IsMultiNetworkPoliciesSupportEnabled() bool {
	return config.OVNKubernetesFeature.EnableMultiNetwork && config.OVNKubernetesFeature.EnableMultiNetworkPolicy
}

func DoesNetworkRequireIPAM(netInfo NetInfo) bool {
	return !((netInfo.TopologyType() == types.Layer2Topology || netInfo.TopologyType() == types.LocalnetTopology) && len(netInfo.Subnets()) == 0)
}

func DoesNetworkRequireTunnelIDs(netInfo NetInfo) bool {
	// Layer2Topology with IC require that we allocate tunnel IDs for each pod
	return netInfo.TopologyType() == types.Layer2Topology && config.OVNKubernetesFeature.EnableInterconnect
}
