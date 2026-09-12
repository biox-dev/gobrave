package types

import "testing"

// TestNormalizeSchedulerMode pins the canonical scheduler names and the legacy
// value folding that keeps old analysis rows resolving to the right scheduler.
func TestNormalizeSchedulerMode(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty falls back to default dag", in: "", want: SchedulerModeDag},
		{name: "blank falls back to default dag", in: "   ", want: SchedulerModeDag},
		{name: "dag stays dag", in: SchedulerModeDag, want: SchedulerModeDag},
		{name: "legacy dag_v1 folds to dag", in: "dag_v1", want: SchedulerModeDag},
		{name: "legacy node_v1 folds to dag", in: "node_v1", want: SchedulerModeDag},
		{name: "dynamic stays dynamic", in: SchedulerModeDynamic, want: SchedulerModeDynamic},
		{name: "legacy dynamic_v2 folds to dynamic", in: "dynamic_v2", want: SchedulerModeDynamic},
		{name: "case is normalized", in: "DYNAMIC_V2", want: SchedulerModeDynamic},
		{name: "whitespace is trimmed", in: " dynamic ", want: SchedulerModeDynamic},
		{name: "dataflow stays dataflow", in: SchedulerModeDataflow, want: SchedulerModeDataflow},
		{name: "legacy dataflow_v3 folds to dataflow", in: "dataflow_v3", want: SchedulerModeDataflow},
		{name: "unknown falls back to default dag", in: "does-not-exist", want: SchedulerModeDag},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NormalizeSchedulerMode(tc.in); got != tc.want {
				t.Fatalf("NormalizeSchedulerMode(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
