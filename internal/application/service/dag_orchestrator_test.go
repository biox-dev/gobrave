package service

import (
	"testing"

	"github.com/biox-dev/gobrave/internal/config"
	dagruntime "github.com/biox-dev/gobrave/internal/dag"
)

func TestResumeNodeStatusForRestart(t *testing.T) {
	tests := []struct {
		name         string
		status       string
		hasContainer bool
		wantStatus   string
		wantReset    bool
	}{
		{
			name:         "submitted should become ready",
			status:       dagruntime.StatusSubmitted,
			hasContainer: false,
			wantStatus:   dagruntime.StatusReady,
			wantReset:    true,
		},
		{
			name:         "running without container should become ready",
			status:       dagruntime.StatusRunning,
			hasContainer: false,
			wantStatus:   dagruntime.StatusReady,
			wantReset:    true,
		},
		{
			name:         "running with container should keep running",
			status:       dagruntime.StatusRunning,
			hasContainer: true,
			wantStatus:   "",
			wantReset:    false,
		},
		{
			name:         "terminal status should not reset",
			status:       dagruntime.StatusDone,
			hasContainer: false,
			wantStatus:   "",
			wantReset:    false,
		},
		{
			name:         "status trim and lowercase",
			status:       "  SUBMITTED ",
			hasContainer: false,
			wantStatus:   dagruntime.StatusReady,
			wantReset:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotStatus, gotReset := resumeNodeStatusForRestart(tt.status, tt.hasContainer)
			if gotStatus != tt.wantStatus {
				t.Fatalf("status mismatch: got=%q want=%q", gotStatus, tt.wantStatus)
			}
			if gotReset != tt.wantReset {
				t.Fatalf("reset flag mismatch: got=%v want=%v", gotReset, tt.wantReset)
			}
		})
	}
}

func TestCleanupDagNodeContainersBeforeStartEnabled(t *testing.T) {
	tests := []struct {
		name string
		o    *dagOrchestrator
		want bool
	}{
		{name: "nil orchestrator defaults true", o: nil, want: true},
		{name: "nil config defaults true", o: &dagOrchestrator{}, want: true},
		{name: "nil container defaults true", o: &dagOrchestrator{cfg: &config.Config{}}, want: true},
		{
			name: "enabled true",
			o: &dagOrchestrator{cfg: &config.Config{Container: &config.ContainerConfig{
				CleanupDagNodeContainersBeforeStart: true,
			}}},
			want: true,
		},
		{
			name: "enabled false",
			o: &dagOrchestrator{cfg: &config.Config{Container: &config.ContainerConfig{
				CleanupDagNodeContainersBeforeStart: false,
			}}},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.o.cleanupDagNodeContainersBeforeStartEnabled()
			if got != tt.want {
				t.Fatalf("unexpected result: got=%v want=%v", got, tt.want)
			}
		})
	}
}

func TestBuildNodeRerunReason(t *testing.T) {
	tests := []struct {
		name             string
		commandMatched   bool
		paramsMatched    bool
		requireParamsMD5 bool
		want             string
	}{
		{
			name:             "command and params both changed",
			commandMatched:   false,
			paramsMatched:    false,
			requireParamsMD5: true,
			want:             "command and params changed",
		},
		{
			name:             "only command changed",
			commandMatched:   false,
			paramsMatched:    true,
			requireParamsMD5: true,
			want:             "command changed",
		},
		{
			name:             "only params changed when params required",
			commandMatched:   true,
			paramsMatched:    false,
			requireParamsMD5: true,
			want:             "params changed",
		},
		{
			name:             "fallback reason when params are not required",
			commandMatched:   true,
			paramsMatched:    false,
			requireParamsMD5: false,
			want:             "node cache invalidated",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildNodeRerunReason(tt.commandMatched, tt.paramsMatched, tt.requireParamsMD5)
			if got != tt.want {
				t.Fatalf("rerun reason mismatch: got=%q want=%q", got, tt.want)
			}
		})
	}
}
