package ovs

import (
	"context"
	"fmt"

	libovsdbclient "github.com/ovn-kubernetes/libovsdb/client"

	"github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/types"
	"github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/vswitchd"
)

// ListPorts looks up all ovs bridge ports from the cache
func ListPorts(ovsClient libovsdbclient.Client) ([]*vswitchd.Port, error) {
	ctx, cancel := context.WithTimeout(context.Background(), types.OVSDBTimeout)
	defer cancel()
	searchedPorts := []*vswitchd.Port{}
	err := ovsClient.List(ctx, &searchedPorts)
	return searchedPorts, err
}

// FindPortsWithPredicate returns all the ovs ports in the cache that matches the lookup function
func FindPortsWithPredicate(ovsClient libovsdbclient.Client, lookupFunction func(item *vswitchd.Port) bool) ([]*vswitchd.Port, error) {
	ctx, cancel := context.WithTimeout(context.Background(), types.OVSDBTimeout)
	defer cancel()
	searchedPorts := []*vswitchd.Port{}

	err := ovsClient.WhereCache(lookupFunction).List(ctx, &searchedPorts)
	if err != nil {
		return nil, fmt.Errorf("failed listing ports : %v", err)
	}

	return searchedPorts, nil
}
