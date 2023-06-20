package libovsdbops

import (
	"context"
	"fmt"

	libovsdbclient "github.com/ovn-org/libovsdb/client"

	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/sbdb"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/types"
)

// FindSBPortBinding looks up the port-binding for lsp in the cache
func FindSBPortBinding(sbClient libovsdbclient.Client, logicalPortName string) (*sbdb.PortBinding, error) {
	ctx, cancel := context.WithTimeout(context.Background(), types.OVSDBTimeout)
	defer cancel()
	pbs := []sbdb.PortBinding{}
	err := sbClient.WhereCache(func(pb *sbdb.PortBinding) bool {
		return pb.LogicalPort == logicalPortName
	}).List(ctx, &pbs)
	if err != nil {
		return nil, fmt.Errorf("can't find port-binding for virtual port %s: %v", logicalPortName, err)
	}

	if len(pbs) > 1 {
		return nil, fmt.Errorf("unexpectedly found multiple port-bindings for virtual port %s: %+v", logicalPortName, pbs)
	}

	if len(pbs) == 0 {
		return nil, libovsdbclient.ErrNotFound
	}
	return &pbs[0], nil
}
