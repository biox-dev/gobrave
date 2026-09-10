package orchestratorv2

import (
	"testing"
	"time"

	"github.com/biox-dev/gobrave/internal/types"
)

// TestOwnsAnalysis pins the ownership gate. RecoverRunningAnalyses must only adopt
// analyses whose persisted scheduler_mode says dynamic_v2 owns them; adopting any
// other mode is exactly what caused the same analysis to be run twice.
func TestOwnsAnalysis(t *testing.T) {
	o := &dynamicDagOrchestratorV2{}

	cases := []struct {
		name string
		item *types.Analysis
		want bool
	}{
		{name: "nil analysis", item: nil, want: false},
		{name: "zero id", item: &types.Analysis{ID: 0, SchedulerMode: types.SchedulerModeDynamicV2}, want: false},
		{name: "v2 is owned", item: &types.Analysis{ID: 1, SchedulerMode: types.SchedulerModeDynamicV2}, want: true},
		{name: "v2 ownership survives normalization", item: &types.Analysis{ID: 2, SchedulerMode: " Dynamic_V2 "}, want: true},
		{name: "legacy is not owned", item: &types.Analysis{ID: 3, SchedulerMode: types.SchedulerModeDagV1}, want: false},
		{name: "historical empty row is not owned", item: &types.Analysis{ID: 4}, want: false},
		{name: "v3 is not owned", item: &types.Analysis{ID: 5, SchedulerMode: types.SchedulerModeDataflowV3}, want: false},
		// {name: "node_v1 is not owned", item: &types.Analysis{ID: 6, SchedulerMode: types.SchedulerModeNodeV1}, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := o.ownsAnalysis(tc.item); got != tc.want {
				t.Fatalf("ownsAnalysis(%+v) = %v, want %v", tc.item, got, tc.want)
			}
		})
	}
}

// TestIsLeaseStale guards the cheap pre-filter that keeps recovery from touching a
// run that another instance is still heartbeating.
func TestIsLeaseStale(t *testing.T) {
	o := &dynamicDagOrchestratorV2{}

	cases := []struct {
		name string
		item *types.Analysis
		want bool
	}{
		{name: "nil analysis", item: nil, want: false},
		{name: "zero timestamp is stale", item: &types.Analysis{ID: 1}, want: true},
		{name: "fresh heartbeat is live", item: &types.Analysis{ID: 2, UpdatedAt: time.Now().UTC()}, want: false},
		{name: "heartbeat inside the lease is live", item: &types.Analysis{ID: 3, UpdatedAt: time.Now().UTC().Add(-dynamicV2LeaseTTL / 2)}, want: false},
		{name: "heartbeat past the lease is stale", item: &types.Analysis{ID: 4, UpdatedAt: time.Now().UTC().Add(-dynamicV2LeaseTTL - time.Second)}, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := o.isLeaseStale(tc.item); got != tc.want {
				t.Fatalf("isLeaseStale(%+v) = %v, want %v", tc.item, got, tc.want)
			}
		})
	}
}
