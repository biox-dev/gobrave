package dataflow

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	dagruntime "github.com/biox-dev/gobrave/internal/dag"
	"github.com/biox-dev/gobrave/internal/event"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
)

// lifecycleRepoV3 tracks only what the analysis job_status lifecycle needs:
// the lease transition (created -> running) and the terminal writes.
type lifecycleRepoV3 struct {
	interfaces.AnalysisRepository
	mu     sync.Mutex
	status string
	getErr error
}

func (r *lifecycleRepoV3) TryMarkAnalysisRunning(_ context.Context, _ int64, _ time.Time, _ time.Time) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.status = types.AnalysisStatusRunning
	return true, nil
}

func (r *lifecycleRepoV3) UpdateAnalysisByID(_ context.Context, _ int64, values map[string]any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s, ok := values["job_status"].(string); ok {
		r.status = s
	}
	return nil
}

func (r *lifecycleRepoV3) GetAnalysisByID(_ context.Context, _ int64) (*types.Analysis, error) {
	if r.getErr != nil {
		return nil, r.getErr
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return &types.Analysis{ID: 7, JobStatus: r.status}, nil
}

func (r *lifecycleRepoV3) currentStatus() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.status
}

type recordingBusV3 struct {
	mu   sync.Mutex
	seen []string
}

func (b *recordingBusV3) Publish(e event.Event) {
	evt, ok := e.(dagruntime.RuntimeEvent)
	if !ok {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.seen = append(b.seen, evt.Name)
}

func (b *recordingBusV3) Subscribe(event.Handler) {}

func (b *recordingBusV3) events() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.seen...)
}

func waitForStatusV3(t *testing.T, repo *lifecycleRepoV3, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if repo.currentStatus() == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("analysis status = %q, want %q", repo.currentStatus(), want)
}

// TestDataflowV3AnalysisStatusRunningThenFinished pins the lifecycle contract:
// starting a run must move the analysis to running, and a clean finish must land
// on finished instead of leaving the stale created/running status behind.
func TestDataflowV3AnalysisStatusRunningThenFinished(t *testing.T) {
	repo := &lifecycleRepoV3{status: "created"}
	bus := &recordingBusV3{}
	orch := &dataflowDagOrchestratorV3{
		bus:    bus,
		repo:   repo,
		router: dagruntime.NewEventRouter(),
	}

	// Empty graph: the loop converges immediately, which isolates the status
	// lifecycle from node execution.
	if err := orch.StartAsync(context.Background(), 7, map[string]any{}, map[string]any{}); err != nil {
		t.Fatalf("StartAsync failed: %v", err)
	}
	if got := repo.currentStatus(); got != types.AnalysisStatusRunning {
		t.Fatalf("status after start = %q, want %q", got, types.AnalysisStatusRunning)
	}

	waitForStatusV3(t, repo, types.AnalysisStatusFinished)

	seen := map[string]bool{}
	for _, name := range bus.events() {
		seen[name] = true
	}
	if !seen[dagruntime.EventDagStarted] {
		t.Fatalf("dag.started not published, got %v", bus.events())
	}
	if !seen[dagruntime.EventDagCompleted] {
		t.Fatalf("dag.completed not published, got %v", bus.events())
	}
}

// TestDataflowV3AnalysisStatusFailedOnPrepareError pins that a run that fails
// before scheduling anything still converges the analysis to failed.
func TestDataflowV3AnalysisStatusFailedOnPrepareError(t *testing.T) {
	repo := &lifecycleRepoV3{status: "created", getErr: errors.New("boom")}
	bus := &recordingBusV3{}
	orch := &dataflowDagOrchestratorV3{
		bus:    bus,
		repo:   repo,
		router: dagruntime.NewEventRouter(),
	}

	if err := orch.StartAsync(context.Background(), 7, map[string]any{}, map[string]any{}); err != nil {
		t.Fatalf("StartAsync failed: %v", err)
	}

	waitForStatusV3(t, repo, types.AnalysisStatusFailed)

	seen := map[string]bool{}
	for _, name := range bus.events() {
		seen[name] = true
	}
	if !seen[dagruntime.EventDagFailed] {
		t.Fatalf("dag.failed not published, got %v", bus.events())
	}
}

// TestDataflowV3ShouldStopByJobStatus pins that the persisted stop signal written
// by the control API is recognized so the loop can converge to stopped.
func TestDataflowV3ShouldStopByJobStatus(t *testing.T) {
	ctx := context.Background()
	for _, status := range []string{types.AnalysisStatusStopping, types.AnalysisStatusStopped} {
		repo := &lifecycleRepoV3{status: status}
		orch := &dataflowDagOrchestratorV3{repo: repo}
		if !orch.isStopRequested(ctx, 7) {
			t.Fatalf("status %q must be treated as a stop request", status)
		}
	}

	repo := &lifecycleRepoV3{status: types.AnalysisStatusRunning}
	orch := &dataflowDagOrchestratorV3{repo: repo}
	if orch.isStopRequested(ctx, 7) {
		t.Fatal("running must not be treated as a stop request")
	}
}
