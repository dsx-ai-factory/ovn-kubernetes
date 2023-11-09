package ops

import (
	"fmt"
	"testing"

	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/nbdb"
	libovsdbtest "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/testing/libovsdb"
)

func TestUpdatePortGroupSetNameAndExternalIDsOps(t *testing.T) {
	initialPortGroup := &nbdb.PortGroup{
		UUID:        buildNamedUUID(),
		Name:        "portgroup1",
		ExternalIDs: map[string]string{"k1": "v1"},
	}

	tests := []struct {
		desc             string
		newName          string
		initialPortGroup *nbdb.PortGroup
		finalPortGroup   *nbdb.PortGroup
		updateExtIDs     map[string]string
	}{
		{
			desc:             "updates portgroup name",
			initialPortGroup: initialPortGroup,
			newName:          "portgroup2",
			updateExtIDs:     map[string]string{},
			finalPortGroup: &nbdb.PortGroup{
				UUID:        buildNamedUUID(),
				ExternalIDs: map[string]string{"k1": "v1"},
				Name:        "portgroup2",
			},
		},
		{
			desc:             "updates portgroup name",
			initialPortGroup: initialPortGroup,
			newName:          "portgroup2",
			updateExtIDs:     map[string]string{"k1": "", "k2": "v2"},
			finalPortGroup: &nbdb.PortGroup{
				UUID:        buildNamedUUID(),
				ExternalIDs: map[string]string{"k2": "v2"},
				Name:        "portgroup2",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			nbClient, cleanup, err := libovsdbtest.NewNBTestHarness(libovsdbtest.TestSetup{
				NBData: []libovsdbtest.TestData{
					tt.initialPortGroup,
				},
			}, nil)
			if err != nil {
				t.Fatalf("test: \"%s\" failed to set up test harness: %v", tt.desc, err)
			}
			t.Cleanup(cleanup.Cleanup)

			// test update with UUID set
			p := func(item *nbdb.PortGroup) bool {
				return item.ExternalIDs["k1"] == "v1"
			}
			initialPortGroups, err := FindPortGroupsWithPredicate(nbClient, p)
			if err != nil {
				t.Fatalf("test: \"%s\" failed to find initial PortGroup: %v", tt.desc, err)
			}
			if len(initialPortGroups) != 1 {
				t.Fatalf("test: \"%s\" found %d intitial PortGroup, expected 1", tt.desc, len(initialPortGroups))
			}
			initialPortGroups[0].ExternalIDs = tt.updateExtIDs
			ops, err := UpdatePortGroupSetNameAndExternalIDsOps(nbClient, nil, initialPortGroups[0], tt.newName)
			if err != nil {
				t.Fatalf("test: \"%s\" failed to get transact OPS to update portgroup name: %v", tt.desc, err)
			}
			_, err = TransactAndCheck(nbClient, ops)
			if err != nil {
				t.Fatalf("test: \"%s\" failed to update portgroup name: %v", tt.desc, err)
			}
			matcher := libovsdbtest.HaveData([]libovsdbtest.TestData{tt.finalPortGroup})
			success, err := matcher.Match(nbClient)
			if !success {
				t.Fatal(fmt.Errorf("test: \"%s\" didn't match expected with actual, err: %v", tt.desc, matcher.FailureMessage(nbClient)))
			}
			if err != nil {
				t.Fatal(fmt.Errorf("test: \"%s\" encountered error: %v", tt.desc, err))
			}
		})
	}
}
