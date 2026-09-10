package orchestratorv2

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"github.com/biox-dev/gobrave/internal/utils"
	"gorm.io/gorm"
)

var initSnowflakeOnce sync.Once

// materialize allocates node ids through the global snowflake generator.
func initSnowflake(t *testing.T) {
	t.Helper()
	initSnowflakeOnce.Do(func() {
		if err := utils.InitSnowflake(1); err != nil {
			t.Fatalf("init snowflake failed: %v", err)
		}
	})
}

// fakeWorkflowRepo only implements the script lookup used by the reconciler.
// Every other method panics through the embedded nil interface, which is fine
// because these tests never reach them.
type fakeWorkflowRepo struct {
	interfaces.WorkflowRepository

	script      *types.Script
	err         error
	requestedID string
}

func (f *fakeWorkflowRepo) GetScriptByScriptID(ctx context.Context, projectID int64, scriptID string) (*types.Script, error) {
	f.requestedID = scriptID
	if f.err != nil {
		return nil, f.err
	}
	return f.script, nil
}

func newTestReconciler(t *testing.T, conn *fakeWorkflowRepo) *Reconciler {
	t.Helper()
	initSnowflake(t)
	graph := newTestGraph(t)
	return NewReconciler(ReconcilerDeps{
		Analysis:  &types.Analysis{ID: 1, ProjectID: 7, OutputDir: t.TempDir()},
		Graph:     graph,
		Tracker:   NewDependencyTracker(graph),
		Builder:   NewNodeBuilder(),
		Workflows: conn,
	})
}

// Materialized nodes must carry the pipeline_components primary key, not the
// component id emitted by the compiler, otherwise the runtime preparer loads
// script_id=0 and fails with "record not found".
func TestMaterializePersistsResolvedScriptPrimaryKey(t *testing.T) {
	conn := &fakeWorkflowRepo{script: &types.Script{ID: 9001, ProjectID: 7, ScriptID: "script-a"}}
	reconciler := newTestReconciler(t, conn)

	spec, ok := reconciler.graph.Node("a1")
	if !ok {
		t.Fatal("expected fixture node a1 to exist")
	}

	node, err := reconciler.materialize(context.Background(), spec)
	if err != nil {
		t.Fatalf("materialize returned error: %v", err)
	}
	if conn.requestedID != "script-a" {
		t.Fatalf("script lookup used %q, want the component id %q", conn.requestedID, "script-a")
	}
	if node.ScriptID != 9001 {
		t.Fatalf("materialized node script_id = %d, want 9001", node.ScriptID)
	}
}

func TestMaterializeFailsWhenScriptCannotBeResolved(t *testing.T) {
	conn := &fakeWorkflowRepo{err: gorm.ErrRecordNotFound}
	reconciler := newTestReconciler(t, conn)

	spec, _ := reconciler.graph.Node("a1")
	_, err := reconciler.materialize(context.Background(), spec)
	if err == nil {
		t.Fatal("expected materialize to fail for an unresolvable script")
	}
	if !strings.Contains(err.Error(), "resolve node script failed") {
		t.Fatalf("unexpected error: %v", err)
	}
}
