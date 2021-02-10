package ovn

import (
	"fmt"
	goovn "github.com/ebay/go-ovn"

	util "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util"
	"k8s.io/klog/v2"
)

// hash the provided input to make it a valid portGroup name.
func hashedPortGroup(s string) string {
	return util.HashForOVN(s)
}

// hashName is portGroupName without network prefix
func createPortGroup(ovnNBClient goovn.Client, name string, hashName string, netNameInfo util.NetNameInfo) (string, error) {
	klog.V(5).Infof("createPortGroup with %s for network %s", name, netNameInfo.NetName)
	hashName = netNameInfo.Prefix + hashName
	externalIds := map[string]string{"name": name}
	if netNameInfo.NotDefault {
		externalIds["network_name"] = netNameInfo.NetName
	}
	cmd, err := ovnNBClient.PortGroupAdd(hashName, nil, externalIds)
	if err == nil {
		if err = ovnNBClient.Execute(cmd); err != nil {
			return "", fmt.Errorf("execute error for add port group: %s, %v", name, err)
		}
	} else if err != goovn.ErrorExist {
		// Ignore goovn.ErrorExist to implement "--may-exist" behavior
		return "", fmt.Errorf("add error for port group: %s, %v", name, err)
	}

	pg, err := ovnNBClient.PortGroupGet(hashName)
	if err == nil {
		return pg.UUID, nil
	} else {
		return "", fmt.Errorf("failed to get port group UUID: %s, %v", name, err)
	}
}

// hashName is the portGroupName without network Prefix
func deletePortGroup(ovnNBClient goovn.Client, hashName string, netNameInfo util.NetNameInfo) error {
	klog.V(5).Infof("deletePortGroup %s for network %s", hashName, netNameInfo.NetName)
	hashName = netNameInfo.Prefix + hashName
	cmd, err := ovnNBClient.PortGroupDel(hashName)
	if err == nil {
		if err = ovnNBClient.Execute(cmd); err != nil {
			return fmt.Errorf("execute error for delete port group: %s, %v", hashName, err)
		}
	} else if err != goovn.ErrorNotFound {
		// Ignore goovn.ErrorNotFound to implement "--if-exist" behavior
		return fmt.Errorf("delete error for port group: %s, %v", hashName, err)
	}
	return nil
}
