package libovsdbops

import (
	"context"

	libovsdbclient "github.com/ovn-org/libovsdb/client"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/nbdb"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/types"
)

// Mirror OPs

type mirrorPredicate func(*nbdb.Mirror) bool

// GetMirror looks up a mirror from the cache
func GetMirror(nbClient libovsdbclient.Client, mirror *nbdb.Mirror) (*nbdb.Mirror, error) {
	found := []*nbdb.Mirror{}
	opModel := operationModel{
		Model:          mirror,
		ModelPredicate: func(item *nbdb.Mirror) bool { return item.Name == mirror.Name },
		ExistingResult: &found,
		ErrNotFound:    true,
		BulkOp:         false,
	}

	m := newModelClient(nbClient)
	err := m.Lookup(opModel)
	if err != nil {
		return nil, err
	}

	return found[0], nil
}

// FindMirrorWithPredicate looks up portmirror from the cache based on a
// given predicate
func FindMirrorWithPredicate(nbClient libovsdbclient.Client, pmPredicate mirrorPredicate) ([]*nbdb.Mirror, error) {
	ctx, cancel := context.WithTimeout(context.Background(), types.OVSDBTimeout)
	defer cancel()
	found := []*nbdb.Mirror{}
	err := nbClient.WhereCache(pmPredicate).List(ctx, &found)
	return found, err
}

// ListMirrors looks up all mirrors from the cache
func ListMirrors(nbClient libovsdbclient.Client) ([]*nbdb.Mirror, error) {
	ctx, cancel := context.WithTimeout(context.Background(), types.OVSDBTimeout)
	defer cancel()
	mirrorList := []*nbdb.Mirror{}
	err := nbClient.List(ctx, &mirrorList)
	return mirrorList, err
}

// CreateOrUpdateMirror creates or updates the provided portmirror
func CreateOrUpdateMirror(nbClient libovsdbclient.Client, mirror *nbdb.Mirror, fields ...interface{}) error {
	if len(fields) == 0 {
		fields = onModelUpdatesAllNonDefault()
	}
	opModel := operationModel{
		Model:          mirror,
		ModelPredicate: func(item *nbdb.Mirror) bool { return item.Name == mirror.Name },
		OnModelUpdates: fields,
		ErrNotFound:    false,
		BulkOp:         false,
	}

	m := newModelClient(nbClient)
	_, err := m.CreateOrUpdate(opModel)
	return err
}

// DeletePortMirror deletes the provided portmirror
func DeleteMirror(nbClient libovsdbclient.Client, mirror *nbdb.Mirror) error {
	opModel := operationModel{
		Model:       mirror,
		ErrNotFound: false,
		BulkOp:      false,
	}

	m := newModelClient(nbClient)
	return m.Delete(opModel)
}
