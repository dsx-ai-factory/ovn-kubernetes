// +build windows

package node

import "net"

func initLocalOnlyGateway(nodeName string, subnet *net.IPNet, stopChan chan struct{}) (net.HardwareAddr, error) {
	return nil, nil
}
