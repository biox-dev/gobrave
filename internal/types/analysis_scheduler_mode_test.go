package types

import "testing"

func TestNormalizeSchedulerMode(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty falls back to legacy", in: "", want: SchedulerModeDagV1},
		{name: "blank falls back to legacy", in: "   ", want: SchedulerModeDagV1},
		{name: "legacy passes through", in: SchedulerModeDagV1, want: SchedulerModeDagV1},
		{name: "v2 passes through", in: SchedulerModeDynamicV2, want: SchedulerModeDynamicV2},
		{name: "case is normalized", in: "DYNAMIC_V2", want: SchedulerModeDynamicV2},
		{name: "whitespace is trimmed", in: " dynamic_v2 ", want: SchedulerModeDynamicV2},
		{name: "v3 passes through", in: SchedulerModeDataflowV3, want: SchedulerModeDataflowV3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NormalizeSchedulerMode(tc.in); got != tc.want {
				t.Fatalf("NormalizeSchedulerMode(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestSchedulerModeHasDedicatedRecovery pins the routing contract that prevents an
// analysis from being recovered by two schedulers at once.
//
// Only modes with their own recovery loop may return true; everything else (including
// the empty value stored on historical rows) must keep being handled by the legacy
// orchestrator, which is the pre-existing behaviour.
func TestSchedulerModeHasDedicatedRecovery(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{name: "historical rows stay with legacy", in: "", want: false},
		{name: "legacy owns its rows", in: SchedulerModeDagV1, want: false},
		// {name: "node_v1 stays with legacy", in: SchedulerModeNodeV1, want: false},
		{name: "v3 is not routed away yet", in: SchedulerModeDataflowV3, want: false},
		{name: "v2 owns its own recovery", in: SchedulerModeDynamicV2, want: true},
		{name: "v2 ownership survives normalization", in: "  Dynamic_V2  ", want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SchedulerModeHasDedicatedRecovery(tc.in); got != tc.want {
				t.Fatalf("SchedulerModeHasDedicatedRecovery(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
