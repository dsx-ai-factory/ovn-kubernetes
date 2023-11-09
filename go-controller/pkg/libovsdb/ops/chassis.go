package ops

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/util/sets"

	libovsdbclient "github.com/ovn-org/libovsdb/client"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/sbdb"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/types"
)

// ListChassis looks up all chassis from the cache
func ListChassis(sbClient libovsdbclient.Client) ([]*sbdb.Chassis, error) {
	ctx, cancel := context.WithTimeout(context.Background(), types.OVSDBTimeout)
	defer cancel()
	searchedChassis := []*sbdb.Chassis{}
	err := sbClient.List(ctx, &searchedChassis)
	return searchedChassis, err
}

// ListChassisPrivate looks up all chassis private models from the cache
func ListChassisPrivate(sbClient libovsdbclient.Client) ([]*sbdb.ChassisPrivate, error) {
	ctx, cancel := context.WithTimeout(context.Background(), types.OVSDBTimeout)
	defer cancel()
	found := []*sbdb.ChassisPrivate{}
	err := sbClient.List(ctx, &found)
	return found, err
}

// GetChassis looks up a chassis from the cache using the 'Name' column which is an indexed
// column.
func GetChassis(sbClient libovsdbclient.Client, chassis *sbdb.Chassis) (*sbdb.Chassis, error) {
	found := []*sbdb.Chassis{}
	opModel := operationModel{
		Model:          chassis,
		ExistingResult: &found,
		ErrNotFound:    true,
		BulkOp:         false,
	}

	m := newModelClient(sbClient)
	err := m.Lookup(opModel)
	if err != nil {
		return nil, err
	}

	return found[0], nil
}

func ListChassisPrivateWithClusterName(sbClient libovsdbclient.Client, clusterName string) ([]*sbdb.ChassisPrivate, error) {
	// cluster_name set is not set in the external_ids of Chassis_Private entries unlike Chassis entries.
	// So we cannot filter based on that. So instead we get list of ChassisPrivate entries as well a list of
	// chassis entries filtered by cluster name. We prepare a final list with only items of the chassis Private
	// present in the chassis List (which is already filtered for cluster name)

	chassisPrivate, err := ListChassisPrivate(sbClient)
	if err != nil {
		return nil, err
	}
	knownChassisNames := sets.NewString()
	chassisList, err := ListChassisWithClusterName(sbClient, clusterName)
	if err != nil {
		return nil, err
	}
	for _, chassis := range chassisList {
		knownChassisNames.Insert(chassis.Name)
	}
	chassisPrivateResultList := []*sbdb.ChassisPrivate{}

	for _, chassisPrivateItem := range chassisPrivate {
		if knownChassisNames.Has(chassisPrivateItem.Name) {
			chassisPrivateResultList = append(chassisPrivateResultList, chassisPrivateItem)
		}
	}

	return chassisPrivateResultList, err
}

// ListChassisWithClusterName returns all the logical chassis that has the ovn-cms-option `cluster_name` set to the specified value
func ListChassisWithClusterName(sbClient libovsdbclient.Client, clusterName string) ([]*sbdb.Chassis, error) {
	ovnCmsOpts := fmt.Sprintf("cluster_name:%s", clusterName)
	searchPredicate := func(item *sbdb.Chassis) bool {
		return item.ExternalIDs["ovn-cms-options"] == ovnCmsOpts
	}
	searchedChassis, err := ListChassisByPredicate(sbClient, searchPredicate)
	if err != nil {
		return nil, err
	}

	return searchedChassis, nil
}

// ListChassisByPredicate returns all the logical chassis in the cache that matches the lookup function
func ListChassisByPredicate(sbClient libovsdbclient.Client, lookupFunction func(item *sbdb.Chassis) bool) ([]*sbdb.Chassis, error) {
	ctx, cancel := context.WithTimeout(context.Background(), types.OVSDBTimeout)
	defer cancel()
	searchedChassis := []*sbdb.Chassis{}

	err := sbClient.WhereCache(lookupFunction).List(ctx, &searchedChassis)
	if err != nil {
		return nil, fmt.Errorf("failed listing chassis err: %v", err)
	}

	return searchedChassis, nil
}

// DeleteChassis deletes the provided chassis and associated private chassis
func DeleteChassis(sbClient libovsdbclient.Client, chassis ...*sbdb.Chassis) error {
	opModels := make([]operationModel, 0, len(chassis))
	for i := range chassis {
		foundChassis := []*sbdb.Chassis{}
		chassisPrivate := sbdb.ChassisPrivate{
			Name: chassis[i].Name,
		}
		chassisUUID := ""
		opModel := []operationModel{
			{
				Model:          chassis[i],
				ExistingResult: &foundChassis,
				ErrNotFound:    false,
				BulkOp:         false,
				DoAfter: func() {
					if len(foundChassis) > 0 {
						chassisPrivate.Name = foundChassis[0].Name
						chassisUUID = foundChassis[0].UUID
					}
				},
			},
			{
				Model:       &chassisPrivate,
				ErrNotFound: false,
				BulkOp:      false,
			},
			// IGMPGroup has a weak link to chassis, deleting multiple chassis may result in IGMP_Groups
			// with identical values on columns "address", "datapath", and "chassis", when "chassis" goes empty
			{
				Model: &sbdb.IGMPGroup{},
				ModelPredicate: func(group *sbdb.IGMPGroup) bool {
					return group.Chassis != nil && chassisUUID != "" && *group.Chassis == chassisUUID
				},
				ErrNotFound: false,
				BulkOp:      true,
			},
		}
		opModels = append(opModels, opModel...)
	}

	m := newModelClient(sbClient)
	err := m.Delete(opModels...)
	return err
}

type chassisPredicate func(*sbdb.Chassis) bool

// DeleteChassisWithPredicate looks up chassis from the cache based on a given
// predicate and deletes them as well as the associated private chassis
func DeleteChassisWithPredicate(sbClient libovsdbclient.Client, p chassisPredicate) error {
	foundChassis := []*sbdb.Chassis{}
	foundChassisNames := sets.NewString()
	foundChassisUUIDS := sets.NewString()
	opModels := []operationModel{
		{
			Model:          &sbdb.Chassis{},
			ModelPredicate: p,
			ExistingResult: &foundChassis,
			ErrNotFound:    false,
			BulkOp:         true,
			DoAfter: func() {
				for _, chassis := range foundChassis {
					foundChassisNames.Insert(chassis.Name)
					foundChassisUUIDS.Insert(chassis.UUID)
				}
			},
		},
		{
			Model:          &sbdb.ChassisPrivate{},
			ModelPredicate: func(item *sbdb.ChassisPrivate) bool { return foundChassisNames.Has(item.Name) },
			ErrNotFound:    false,
			BulkOp:         true,
		},
		// IGMPGroup has a weak link to chassis, deleting multiple chassis may result in IGMP_Groups
		// with identical values on columns "address", "datapath", and "chassis", when "chassis" goes empty
		{
			Model:          &sbdb.IGMPGroup{},
			ModelPredicate: func(group *sbdb.IGMPGroup) bool { return group.Chassis != nil && foundChassisUUIDS.Has(*group.Chassis) },
			ErrNotFound:    false,
			BulkOp:         true,
		},
	}
	m := newModelClient(sbClient)
	err := m.Delete(opModels...)
	return err
}

// CreateOrUpdateChassis creates or updates the chassis record along with the encap record
func CreateOrUpdateChassis(sbClient libovsdbclient.Client, chassis *sbdb.Chassis, encap *sbdb.Encap) error {
	m := newModelClient(sbClient)
	opModels := []operationModel{
		{
			Model: encap,
			DoAfter: func() {
				chassis.Encaps = []string{encap.UUID}
			},
			OnModelUpdates: onModelUpdatesAllNonDefault(),
			ErrNotFound:    false,
			BulkOp:         false,
		},
		{
			Model:            chassis,
			OnModelMutations: []interface{}{&chassis.OtherConfig, &chassis.Encaps},
			ErrNotFound:      false,
			BulkOp:           false,
		},
	}

	if _, err := m.CreateOrUpdate(opModels...); err != nil {
		return err
	}

	return nil
}
