package types

import (
	"github.com/containernetworking/cni/pkg/types"
)

// NetConf is CNI NetConf with DeviceID
type NetConf struct {
	types.NetConf
	// PciAddrs in case of using sriov
	DeviceID string `json:"deviceID,omitempty"`
	// Network Cidr
	NetCidr string `json:"net_cidr,omitempty"`
	// Network MTU
	MTU int `json:"mtu,omitempty"`
	// set to localnet if it needs public interface
	TopoType string `json:"topology,omitempty"`
	// captures net-attach-def name in the form of namespace/name
	NadName string `json:"net_attach_def_name,omitempty"`
	// set true if it is default networkattachmentdefintion
	NotDefault bool `json:"not_default,omitempty"`

	// VlanID, valid in localnet topology network
	VlanId int `json:"vlan_id,omitempty"`
	// bridge name, valid in localnet topology network
	BridgeName string `json:"bridge_name,omitempty"`
	// list of IPs to be excluded from being allocated for Pod, valid in localnet topology network
	ExcludeIPs []string `json:"exclude_ips,omitempty"`

	// LogFile to log all the messages from cni shim binary to
	LogFile string `json:"logFile,omitempty"`
	// Level is the logging verbosity level
	LogLevel string `json:"logLevel,omitempty"`
	// LogFileMaxSize is the maximum size in bytes of the logfile
	// before it gets rolled.
	LogFileMaxSize int `json:"logfile-maxsize"`
	// LogFileMaxBackups represents the the maximum number of
	// old log files to retain
	LogFileMaxBackups int `json:"logfile-maxbackups"`
	// LogFileMaxAge represents the maximum number
	// of days to retain old log files
	LogFileMaxAge int `json:"logfile-maxage"`
}
