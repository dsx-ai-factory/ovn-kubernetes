package libovsdbops

import (
	"context"

	libovsdbclient "github.com/ovn-org/libovsdb/client"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/nbdb"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/types"
)

func FindLogicalRouterPoliciesByOwner(nbClient libovsdbclient.Client, owner string) ([]*nbdb.LogicalRouterPolicy, error) {
	ctx, cancel := context.WithTimeout(context.Background(), types.OVSDBTimeout)
	defer cancel()
	policies := []*nbdb.LogicalRouterPolicy{}
	err := nbClient.WhereCache(func(item *nbdb.LogicalRouterPolicy) bool {
		return item.ExternalIDs[types.ExternalIDK8sOwner] == owner
	}).List(ctx, &policies)
	if err != nil {
		return nil, err
	}
	return policies, nil
}
