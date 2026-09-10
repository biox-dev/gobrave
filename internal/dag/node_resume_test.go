package dag

import "testing"

func TestResumeNodeStatusForRestart(t *testing.T) {
	cases := []struct {
		name         string
		status       string
		hasContainer bool
		wantStatus   string
		wantReset    bool
	}{
		{name: "submitted always rolls back", status: StatusSubmitted, hasContainer: false, wantStatus: StatusReady, wantReset: true},
		{name: "submitted rolls back even with a container", status: StatusSubmitted, hasContainer: true, wantStatus: StatusReady, wantReset: true},
		{name: "running without container rolls back", status: StatusRunning, hasContainer: false, wantStatus: StatusReady, wantReset: true},
		{name: "running with live container is left alone", status: StatusRunning, hasContainer: true, wantStatus: "", wantReset: false},
		{name: "ready is untouched", status: StatusReady, wantStatus: "", wantReset: false},
		{name: "done is untouched", status: StatusDone, wantStatus: "", wantReset: false},
		{name: "failed is untouched", status: StatusFailed, wantStatus: "", wantReset: false},
		{name: "skipped is untouched", status: StatusSkipped, wantStatus: "", wantReset: false},
		{name: "empty status is untouched", status: "", wantStatus: "", wantReset: false},
		{name: "status is case insensitive", status: "RUNNING", wantStatus: StatusReady, wantReset: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotStatus, gotReset := ResumeNodeStatusForRestart(tc.status, tc.hasContainer)
			if gotReset != tc.wantReset {
				t.Fatalf("ResumeNodeStatusForRestart(%q, %v) reset = %v, want %v", tc.status, tc.hasContainer, gotReset, tc.wantReset)
			}
			if gotStatus != tc.wantStatus {
				t.Fatalf("ResumeNodeStatusForRestart(%q, %v) status = %q, want %q", tc.status, tc.hasContainer, gotStatus, tc.wantStatus)
			}
		})
	}
}
