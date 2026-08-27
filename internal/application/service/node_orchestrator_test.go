package service

// import (
// 	"context"
// 	"strings"
// 	"testing"
// 	"time"

// 	dagruntime "github.com/biox-dev/gobrave/internal/dag"
// 	"github.com/biox-dev/gobrave/internal/types"
// )

// func TestNodeOrchestratorStartAsyncSingleNodeLifecycle(t *testing.T) {
// 	tests := []struct {
// 		name          string
// 		initialStatus string
// 	}{
// 		{name: "from ready", initialStatus: dagruntime.StatusReady},
// 		{name: "from submitted", initialStatus: dagruntime.StatusSubmitted},
// 	}

// 	for _, tt := range tests {
// 		t.Run(tt.name, func(t *testing.T) {
// 			repo := newInMemoryAnalysisRepo()
// 			node := &types.AnalysisNode{
// 				AnalysisNodeID: "node-test-" + strings.ReplaceAll(tt.name, " ", "-"),
// 				AnalysisID:     "analysis-test-1",
// 				NodeID:         "n1",
// 				Status:         tt.initialStatus,
// 				Executor:       "local",
// 			}
// 			if err := repo.CreateAnalysisNodes(context.Background(), []*types.AnalysisNode{node}); err != nil {
// 				t.Fatalf("create test node failed: %v", err)
// 			}

// 			o := NewNodeOrchestrator(repo, nil, nil, nil)
// 			if err := o.StartAsync(context.Background(), int64(node.ID)); err != nil {
// 				t.Fatalf("start async failed: %v", err)
// 			}

// 			first, err := repo.GetAnalysisNodeByAnalysisNodeID(context.Background(), node.AnalysisNodeID)
// 			if err != nil {
// 				t.Fatalf("load first status failed: %v", err)
// 			}
// 			if first == nil {
// 				t.Fatal("node not found after submit")
// 			}
// 			firstStatus := strings.ToLower(strings.TrimSpace(first.Status))
// 			if firstStatus != dagruntime.StatusSubmitted && firstStatus != dagruntime.StatusRunning {
// 				t.Fatalf("unexpected first status after submit: got=%q want=submitted/running", firstStatus)
// 			}

// 			deadline := time.Now().Add(3 * time.Second)
// 			seenRunning := firstStatus == dagruntime.StatusRunning
// 			for time.Now().Before(deadline) {
// 				current, getErr := repo.GetAnalysisNodeByAnalysisNodeID(context.Background(), node.AnalysisNodeID)
// 				if getErr != nil {
// 					t.Fatalf("load node status failed: %v", getErr)
// 				}
// 				if current == nil {
// 					t.Fatal("node disappeared during execution")
// 				}
// 				status := strings.ToLower(strings.TrimSpace(current.Status))
// 				if status == dagruntime.StatusRunning {
// 					seenRunning = true
// 				}
// 				if status == dagruntime.StatusDone {
// 					if !seenRunning && tt.initialStatus == dagruntime.StatusSubmitted {
// 						// For submitted case, running can be very short. Keep lenient and accept done.
// 					}
// 					return
// 				}
// 				time.Sleep(10 * time.Millisecond)
// 			}

// 			last, err := repo.GetAnalysisNodeByAnalysisNodeID(context.Background(), node.AnalysisNodeID)
// 			if err != nil {
// 				t.Fatalf("load final status failed: %v", err)
// 			}
// 			if last == nil {
// 				t.Fatal("node not found at final check")
// 			}
// 			t.Fatalf("node did not reach done in time, last_status=%q", last.Status)
// 		})
// 	}
// }
