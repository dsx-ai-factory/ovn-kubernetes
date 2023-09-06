package util

import (
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	nettypes "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	libovsdbclient "github.com/ovn-org/libovsdb/client"
	cnitypes "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/cni/types"

	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/config"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/libovsdbops"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/nbdb"

	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/types"

	"github.com/urfave/cli/v2"

	kapi "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
	utilnet "k8s.io/utils/net"
)

var (
	rePciDeviceName = regexp.MustCompile(`^[0-9a-f]{4}:[0-9a-f]{2}:[01][0-9a-f]\.[0-7]$`)
	reAuxDeviceName = regexp.MustCompile(`^\w+.\w+.\d+$`)
)

// IsPCIDeviceName check if passed device id is a PCI device name
func IsPCIDeviceName(deviceID string) bool {
	return rePciDeviceName.MatchString(deviceID)
}

// IsAuxDeviceName check if passed device id is a Auxiliary device name
func IsAuxDeviceName(deviceID string) bool {
	return reAuxDeviceName.MatchString(deviceID)
}

// StringArg gets the named command-line argument or returns an error if it is empty
func StringArg(context *cli.Context, name string) (string, error) {
	val := context.String(name)
	if val == "" {
		return "", fmt.Errorf("argument --%s should be non-null", name)
	}
	return val, nil
}

// GetLegacyK8sMgmtIntfName returns legacy management ovs-port name
func GetLegacyK8sMgmtIntfName(nodeName string) string {
	if len(nodeName) > 11 {
		return GetClusterScopedName(types.K8sPrefix + (nodeName[:11]))
	}
	return GetClusterScopedName(types.K8sPrefix + nodeName)
}

// GetWorkerFromGatewayRouter determines a node's corresponding worker switch name from a gateway router name
func GetWorkerFromGatewayRouter(gr string) string {
	return strings.TrimPrefix(gr, GetClusterScopedName(types.GWRouterPrefix))
}

// GetGatewayRouterFromNode determines a node's corresponding gateway router name
func GetGatewayRouterFromNode(node string) string {
	return GetClusterScopedName(types.GWRouterPrefix + node)
}

// GetNodeChassisID returns the machine's OVN chassis ID
func GetNodeChassisID() (string, error) {
	chassisID, stderr, err := RunOVSVsctl("--if-exists", "get",
		"Open_vSwitch", ".", "external_ids:system-id")
	if err != nil {
		klog.Errorf("No system-id configured in the local host, "+
			"stderr: %q, error: %v", stderr, err)
		return "", err
	}
	if chassisID == "" {
		return "", fmt.Errorf("no system-id configured in the local host")
	}

	return chassisID, nil
}

// GetHybridOverlayPortName returns the name of the hybrid overlay switch port
// for a given node
func GetHybridOverlayPortName(nodeName string) string {
	return "int-" + nodeName
}

type annotationNotSetError struct {
	msg string
}

type annotationAlreadySetError struct {
	msg string
}

func (anse annotationNotSetError) Error() string {
	return anse.msg
}

func (aase annotationAlreadySetError) Error() string {
	return aase.msg
}

var ErrorAttachDefNotOvnManaged = errors.New("net-attach-def not managed by OVN")

// newAnnotationNotSetError returns an error for an annotation that is not set
func newAnnotationNotSetError(format string, args ...interface{}) error {
	return annotationNotSetError{msg: fmt.Sprintf(format, args...)}
}

// newAnnotationAlreadySetError returns an error for an annotation that is not set
func newAnnotationAlreadySetError(format string, args ...interface{}) error {
	return annotationAlreadySetError{msg: fmt.Sprintf(format, args...)}
}

// IsAnnotationNotSetError returns true if the error indicates that an annotation is not set
func IsAnnotationNotSetError(err error) bool {
	_, ok := err.(annotationNotSetError)
	return ok
}

// IsAnnotationAlreadySetError returns true if the error indicates that an annotation is already set
func IsAnnotationAlreadySetError(err error) bool {
	_, ok := err.(annotationAlreadySetError)
	return ok
}

// CalculateHostSubnetsForClusterEntry calculates the host subnets
// available in a CIDR entry
func CalculateHostSubnetsForClusterEntry(cidrEntry config.CIDRNetworkEntry,
	v4HostSubnetCount, v6HostSubnetCount *float64) {
	prefixLength, _ := cidrEntry.CIDR.Mask.Size()
	var one uint64 = 1
	if prefixLength > cidrEntry.HostSubnetLength {
		klog.Warningf("Invalid cidr entry: %+v found while calculating subnet count",
			cidrEntry)
		return
	}
	if !utilnet.IsIPv6CIDR(cidrEntry.CIDR) {
		*v4HostSubnetCount = *v4HostSubnetCount + float64(one<<(cidrEntry.HostSubnetLength-prefixLength))
	} else {
		*v6HostSubnetCount = *v6HostSubnetCount + float64(one<<(cidrEntry.HostSubnetLength-prefixLength))

	}
}

// UpdateUsedHostSubnetsCount increments the v4/v6 host subnets count based on
// the subnet being visited
func UpdateUsedHostSubnetsCount(subnet *net.IPNet,
	v4SubnetsAllocated, v6SubnetsAllocated *float64, isAdd bool) {
	op := -1
	if isAdd {
		op = 1
	}
	if !utilnet.IsIPv6CIDR(subnet) {
		*v4SubnetsAllocated = *v4SubnetsAllocated + float64(1*op)
	} else {
		*v6SubnetsAllocated = *v6SubnetsAllocated + float64(1*op)
	}
}

// HashforOVN hashes the provided input to make it a valid addressSet or portGroup name.
func HashForOVN(s string) string {
	h := fnv.New64a()
	_, err := h.Write([]byte(s))
	if err != nil {
		klog.Errorf("Failed to hash %s", s)
		return ""
	}
	hashString := strconv.FormatUint(h.Sum64(), 10)
	return fmt.Sprintf("a%s", hashString)
}

type NetNameInfo struct {
	// netconf's name
	NetName     string
	Prefix      string
	IsSecondary bool
}

type NetAttachDefInfo struct {
	NetNameInfo
	// net-attach-defs share the same NetConf. so, multiple net-attach-defs share the same logical switch
	// (subnet cidr, MTU, and so on), but they might have different attachment points (no VF, requires a VF, requires a
	// VF from a different PF). in the map below, key is <Namespace>/<Name> of net-attach-def and value is the nad
	// specific configuration (derived from the annotations of the net-attach-def).
	NetAttachDefs sync.Map
	NetCidr       string
	MTU           int

	TopoType   string
	ExcludeIPs []net.IP

	// localnet network only
	VlanId     int
	Gateway    string
	GatewayMac string
	BridgeName string
	XDPService bool

	// layer 2 network only
	ConnectToNad string

	// additional NAD routes for this network
	NADRoutes []*net.IPNet
}

func NewNetAttachDefInfo(netconf *cnitypes.NetConf) (*NetAttachDefInfo, error) {
	// if not_default is set to be true, override IsSecondary
	if netconf.NotDefault {
		netconf.IsSecondary = true
	}

	if netconf.TopoType != types.Layer3AttachDefTopoType &&
		netconf.TopoType != types.LocalnetAttachDefTopoType &&
		netconf.TopoType != types.Layer2AttachDefTopoType {
		return nil, fmt.Errorf("invalid topotype %s for net-attach-def %s", netconf.TopoType, netconf.Name)
	}

	if !netconf.IsSecondary && netconf.TopoType != types.Layer3AttachDefTopoType {
		return nil, fmt.Errorf("invalid topotype %s for default net-attach-def %s", netconf.TopoType, netconf.Name)
	}

	netName := "default"
	if netconf.IsSecondary {
		netName = netconf.Name
	}
	prefix := GetNetworkPrefix(netName, !netconf.IsSecondary)

	nadInfo := NetAttachDefInfo{
		NetCidr:     netconf.NetCidr,
		MTU:         netconf.MTU,
		TopoType:    netconf.TopoType,
		VlanId:      netconf.VlanId,
		NetNameInfo: NetNameInfo{netName, prefix, netconf.IsSecondary},
		XDPService:  netconf.XDPService,
		Gateway:     netconf.Gateway,
		GatewayMac:  netconf.GatewayMac,
	}

	if netconf.TopoType == types.LocalnetAttachDefTopoType || netconf.TopoType == types.Layer2AttachDefTopoType {
		if len(netconf.ExcludeCIDRs) == 0 {
			return &nadInfo, nil
		}
		// TODO(gmoodalbail): we should parse this once and stash it in a structure
		netCIDRs, err := config.ParseClusterSubnetEntries(netconf.NetCidr, false)
		if err != nil {
			return nil, fmt.Errorf("failed while parsing the provided NetCIDR %q for Network %q", netconf.NetCidr, netName)
		}

		nadInfo.ExcludeIPs = make([]net.IP, 0)
		for _, excludeCIDRstr := range netconf.ExcludeCIDRs {
			_, excludeCIDR, err := net.ParseCIDR(excludeCIDRstr)
			if err != nil {
				return nil, fmt.Errorf("invalid subnet %q provided in the exclude_cidrs list %s for network %s",
					excludeCIDRstr, netconf.ExcludeCIDRs, netName)
			}

			for excludeIP := excludeCIDR.IP; excludeCIDR.Contains(excludeIP); excludeIP = NextIP(excludeIP) {
				found := false
				for _, netCIDR := range netCIDRs {
					if netCIDR.CIDR.Contains(excludeIP) {
						found = true
						break
					}
				}
				if !found {
					return nil, fmt.Errorf("ip to be excluded %q is not part of any of the provided Network CIDRs "+
						"(%v) for network %s", excludeIP, netCIDRs, netName)
				}
				nadInfo.ExcludeIPs = append(nadInfo.ExcludeIPs, excludeIP)
			}
		}
	}

	return &nadInfo, nil
}

// Parse config in NAD spec and return a NetAttachDefInfo object
func ParseNADInfo(netattachdef *nettypes.NetworkAttachmentDefinition) (*NetAttachDefInfo, *NadConfig, error) {
	netconf, err := ParseNetConf(netattachdef)
	if err != nil {
		return nil, nil, err
	}
	nadKey := GetNadKeyName(netattachdef.Namespace, netattachdef.Name)
	if netconf.IsSecondary && netconf.NadName != nadKey {
		return nil, nil, fmt.Errorf("net-attach-def name (%s) is inconsistent with config (%s)", nadKey, netconf.NadName)
	}
	nadInfo, err := NewNetAttachDefInfo(netconf)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to construct NetAttachDefInfo %s/%s: %s", netattachdef.Namespace, netattachdef.Name, err)
	}
	if netconf.TopoType == types.Layer2AttachDefTopoType {
		if nad, ok := netattachdef.Annotations[types.OvnK8sConnectToNad]; ok {
			nadInfo.ConnectToNad = nad
		}
	}
	err = GetNADNetConfig(netattachdef, nadInfo)
	if err != nil {
		return nil, nil, err
	}
	nadConfig, err := GetNadConfig(netattachdef, nadInfo.IsSecondary)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to construct NadConfig %s/%s: %s", netattachdef.Namespace, netattachdef.Name, err)
	}
	if config.OvnKubeNode.Mode == types.NodeModeDPU && netconf.XDPService {
		if netconf.TopoType != types.LocalnetAttachDefTopoType {
			return nil, nil, fmt.Errorf("XDP only supported for Localnet based Network Attachment Definition")
		}
		if config.OvnKubeNode.XDPSFRep == "" || config.OvnKubeNode.XDPVeth == "" || config.OvnKubeNode.XDPNamespace == "" {
			return nil, nil, fmt.Errorf("DPU not configured for XDP")
		}
	}
	return nadInfo, nadConfig, nil
}

func ParseNetConf(netattachdef *nettypes.NetworkAttachmentDefinition) (*cnitypes.NetConf, error) {
	netconf := &cnitypes.NetConf{MTU: config.Default.MTU, TopoType: types.Layer3AttachDefTopoType}
	// looking for network attachment definition that use OVN K8S CNI only
	err := json.Unmarshal([]byte(netattachdef.Spec.Config), &netconf)
	if err != nil {
		return nil, fmt.Errorf("error parsing Network Attachment Definition %s/%s: %v", netattachdef.Namespace, netattachdef.Name, err)
	}
	if netconf.Type != "ovn-k8s-cni-overlay" {
		return nil, ErrorAttachDefNotOvnManaged
	}
	if netconf.Name == "" {
		netconf.Name = netattachdef.Name
	}
	if netconf.IsSecondary && netconf.Name == types.DefaultNetworkName {
		return nil, fmt.Errorf("non-default Network attachment definition's name cannot be %s", types.DefaultNetworkName)
	}
	return netconf, nil
}

// Note that for port_group and address_set, it does not allow the '-' character
func GetNadName(namespace, name string, isDefault bool) string {
	if isDefault {
		return types.DefaultNetworkName
	}
	return GetNadKeyName(namespace, name)
}

// for default network, nadName is always "default", otherwide, it is the same as nadKeyName
func GetAnnotationKeyFromNadName(nadName string, isDefault bool) string {
	if isDefault {
		return "default"
	}
	return nadName
}

// key of NetAttachDefInfo.NetAttachDefs map, always in he form of namespace/name no matter it is of default network
func GetNadKeyName(namespace, name string) string {
	return fmt.Sprintf("%s/%s", namespace, name)
}

// Note that for port_group and address_set, it does not allow the '-' character
// Also replace "/" in nadName with "."
func GetNetworkPrefix(netName string, isDefault bool) string {
	if isDefault {
		return ""
	}
	name := strings.ReplaceAll(netName, "-", ".")
	name = strings.ReplaceAll(name, "/", ".")
	return name + "_"
}

// UpdateIPsSlice will search for values of oldIPs in the slice "s" and update it with newIPs values of same IP family
func UpdateIPsSlice(s, oldIPs, newIPs []string) []string {
	n := make([]string, len(s))
	copy(n, s)
	for i, entry := range s {
		for _, oldIP := range oldIPs {
			if entry == oldIP {
				for _, newIP := range newIPs {
					if utilnet.IsIPv6(net.ParseIP(oldIP)) {
						if utilnet.IsIPv6(net.ParseIP(newIP)) {
							n[i] = newIP
							break
						}
					} else {
						if !utilnet.IsIPv6(net.ParseIP(newIP)) {
							n[i] = newIP
							break
						}
					}
				}
				break
			}
		}
	}
	return n
}

// FilterIPsSlice will filter a list of IPs by a list of CIDRs. By default,
// it will *remove* all IPs that match filter, unless keep is true.
//
// It is dual-stack aware.
func FilterIPsSlice(s []string, filter []net.IPNet, keep bool) []string {
	out := make([]string, 0, len(s))
ipLoop:
	for _, ipStr := range s {
		ip := net.ParseIP(ipStr)
		is4 := ip.To4() != nil

		for _, cidr := range filter {
			if is4 && cidr.IP.To4() != nil && cidr.Contains(ip) {
				if keep {
					out = append(out, ipStr)
					continue ipLoop
				} else {
					continue ipLoop
				}
			}
			if !is4 && cidr.IP.To4() == nil && cidr.Contains(ip) {
				if keep {
					out = append(out, ipStr)
					continue ipLoop
				} else {
					continue ipLoop
				}
			}
		}
		if !keep { // discard mode, and nothing matched.
			out = append(out, ipStr)
		}
	}

	return out
}

func GetLogicalPortName(podNamespace, podName, nadName string, isDefault bool) string {
	netPrefix := GetNetworkPrefix(nadName, isDefault)
	return GetClusterNamePrefix() + composePortName(podNamespace, podName, netPrefix)
}

// IsClusterIP checks if the provided IP is a clusterIP
func IsClusterIP(svcVIP string) bool {
	ip := net.ParseIP(svcVIP)
	is4 := ip.To4() != nil
	for _, svcCIDR := range config.Kubernetes.ServiceCIDRs {
		if is4 && svcCIDR.IP.To4() != nil && svcCIDR.Contains(ip) {
			return true
		}
		if !is4 && svcCIDR.IP.To4() == nil && svcCIDR.Contains(ip) {
			return true
		}
	}
	return false
}

func GetIfaceId(podNamespace, podName, nadName string, isDefault bool, clusterPrefix string) string {
	netPrefix := GetNetworkPrefix(nadName, isDefault)
	return clusterPrefix + composePortName(podNamespace, podName, netPrefix)
}

// composePortName should be called both for LogicalPortName and iface-id
// because ovn-nb man says:
// Logical_Switch_Port.name must match external_ids:iface-id
// in the Open_vSwitch database’s Interface table,
// because hypervisors use external_ids:iface-id as a lookup key to
// identify the network interface of that entity.
func composePortName(podNamespace, podName, netPrefix string) string {
	return netPrefix + podNamespace + "_" + podName
}

// Get all possible logical ports name of this network
func GetAllLogicalPortNames(pod *kapi.Pod, nadInfo *NetAttachDefInfo) []string {
	ports := []string{}
	on, networkMap, err := IsNetworkOnPod(pod, nadInfo)
	if err != nil {
		klog.Errorf(err.Error())
	} else if on {
		// the pod is attached to this specific network
		for nadName := range networkMap {
			portName := GetLogicalPortName(pod.Namespace, pod.Name, nadName, !nadInfo.IsSecondary)
			ports = append(ports, portName)
		}
	}
	return ports
}

func SliceHasStringItem(slice []string, item string) bool {
	for _, i := range slice {
		if i == item {
			return true
		}
	}
	return false
}

var updateNodeSwitchLock sync.Mutex

// UpdateNodeSwitchExcludeIPs should be called after adding the management port
// and after adding the hybrid overlay port, and ensures that each port's IP
// is added to the logical switch's exclude_ips. This prevents ovn-northd log
// spam about duplicate IP addresses.
// See https://github.com/ovn-org/ovn-kubernetes/pull/779
func UpdateNodeSwitchExcludeIPs(nbClient libovsdbclient.Client, nodeName string, subnet *net.IPNet) error {
	if utilnet.IsIPv6CIDR(subnet) {
		// We don't exclude any IPs in IPv6
		return nil
	}

	updateNodeSwitchLock.Lock()
	defer updateNodeSwitchLock.Unlock()

	// Only query the cache for mp0 and HO LSPs
	haveManagementPort := true
	managmentPort := &nbdb.LogicalSwitchPort{Name: GetClusterScopedName(types.K8sPrefix + nodeName)}
	_, err := libovsdbops.GetLogicalSwitchPort(nbClient, managmentPort)
	if errors.Is(err, libovsdbclient.ErrNotFound) {
		klog.V(5).Infof("Management port does not exist for node %s", nodeName)
		haveManagementPort = false
	} else if err != nil {
		return fmt.Errorf("failed to get management port for node %s error: %v", nodeName, err)
	}

	haveHybridOverlayPort := true
	HOPort := &nbdb.LogicalSwitchPort{Name: GetClusterScopedName(types.HybridOverlayPrefix + nodeName)}
	_, err = libovsdbops.GetLogicalSwitchPort(nbClient, HOPort)
	if errors.Is(err, libovsdbclient.ErrNotFound) {
		klog.V(5).Infof("Hybridoverlay port does not exist for node %s", nodeName)
		haveHybridOverlayPort = false
	} else if err != nil {
		return fmt.Errorf("failed to get hybrid overlay port for node %s error: %v", nodeName, err)
	}

	mgmtIfAddr := GetNodeManagementIfAddr(subnet)
	hybridOverlayIfAddr := GetNodeHybridOverlayIfAddr(subnet)

	klog.V(5).Infof("haveMP %v haveHO %v ManagementPortAddress %v HybridOverlayAddressOA %v", haveManagementPort, haveHybridOverlayPort, mgmtIfAddr, hybridOverlayIfAddr)
	var excludeIPs string
	if config.HybridOverlay.Enabled {
		if haveHybridOverlayPort && haveManagementPort {
			// no excluded IPs required
		} else if !haveHybridOverlayPort && !haveManagementPort {
			// exclude both
			excludeIPs = mgmtIfAddr.IP.String() + ".." + hybridOverlayIfAddr.IP.String()
		} else if haveHybridOverlayPort {
			// exclude management port IP
			excludeIPs = mgmtIfAddr.IP.String()
		} else if haveManagementPort {
			// exclude hybrid overlay port IP
			excludeIPs = hybridOverlayIfAddr.IP.String()
		}
	} else if !haveManagementPort {
		// exclude management port IP
		excludeIPs = mgmtIfAddr.IP.String()
	}

	sw := nbdb.LogicalSwitch{
		Name:        GetClusterScopedName(nodeName),
		OtherConfig: map[string]string{"exclude_ips": excludeIPs},
	}
	err = libovsdbops.UpdateLogicalSwitchSetOtherConfig(nbClient, &sw)
	if err != nil {
		return fmt.Errorf("failed to update exclude_ips %+v: %v", sw, err)
	}

	return nil
}

// GetDbValByKey returns the value of the specified key in a space separated string (each in the form of k=v)
func GetDbValByKey(keyValString, key string) string {
	keyVals := strings.Fields(keyValString)
	for _, keyVal := range keyVals {
		if strings.HasPrefix(keyVal, key+"=") {
			return strings.TrimPrefix(keyVal, key+"=")
		}
	}
	return ""
}

// Get OVS interface associated pod information (sandbox/network), return false if the OVS interface does not exists
func GetOVSPortPodInfo(hostIfName string) (bool, string, string, error) {
	stdout, stderr, err := RunOVSVsctl("--no-heading", "--format=csv", "--data=bare",
		"--columns=external_ids", "find", "Interface", "name="+hostIfName)
	if err != nil {
		return false, "", "", fmt.Errorf("failed to get OVS interface %s, stderr %v: %v", hostIfName, stderr, err)
	}
	if stdout == "" {
		return false, "", "", nil
	}
	sandbox := GetDbValByKey(stdout, "sandbox")
	nadName := GetDbValByKey(stdout, "network_name")
	// if network_name does not exists, it is default network
	if nadName == "" {
		nadName = types.DefaultNetworkName
	}
	return true, sandbox, nadName, nil
}

func ArrayHasString(array []string, target string) bool {
	for _, str := range array {
		if str == target {
			return true
		}
	}
	return false
}

func IsStringListEqual(stringList1, stringList2 []string) bool {
	if len(stringList1) != len(stringList2) {
		return false
	}
	sort.Strings(stringList1)
	sort.Strings(stringList2)
	for i, s := range stringList1 {
		if s != stringList2[i] {
			return false
		}
	}
	return true
}

func IsIPNetListEqual(ipnets1, ipnets2 []*net.IPNet) bool {
	if len(ipnets1) != len(ipnets2) {
		return false
	}
	ipnetStringList1 := make([]string, len(ipnets1))
	ipnetStringList2 := make([]string, len(ipnets2))
	for index, ipnet := range ipnets1 {
		ipnetStringList1[index] = ipnet.String()
	}
	for index, ipnet := range ipnets2 {
		ipnetStringList2[index] = ipnet.String()
	}
	return IsStringListEqual(ipnetStringList1, ipnetStringList2)
}
