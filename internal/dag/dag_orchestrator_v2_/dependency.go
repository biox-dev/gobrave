package orchestratorv2

import (
	"sort"
	"strings"

	"github.com/biox-dev/gobrave/internal/types"
)

// DependencyTracker keeps the per-node readiness state of one run in memory.
//
// It is the single source of truth for "may this template run now?" and is
// advanced only by runtime events (plus an idempotent seed at run start).
//
// Blocking is derived, not stored: a node is blocked when any of its remaining
// upstreams is known to have failed. Deriving it makes retries correct - once a
// failed upstream succeeds it is removed from the failed set and the block
// disappears automatically.
type DependencyTracker struct {
	// waiting holds the upstreams that have not succeeded yet.
	waiting map[string]map[string]struct{}
	// failed holds the nodes known to have ended without a reusable result.
	failed map[string]struct{}
	// outgoing mirrors the graph adjacency so success can push downstream.
	outgoing map[string][]string

	blockedCache map[string]bool
}

// NewDependencyTracker builds a tracker for the given graph. Every node starts
// with its full upstream set pending.
func NewDependencyTracker(graph *Graph) *DependencyTracker {
	tracker := &DependencyTracker{
		waiting:      make(map[string]map[string]struct{}, graph.Len()),
		failed:       make(map[string]struct{}),
		outgoing:     make(map[string][]string, graph.Len()),
		blockedCache: make(map[string]bool),
	}

	for _, nodeID := range graph.order {
		deps := make(map[string]struct{})
		for _, upstream := range graph.UpstreamDependencies(nodeID) {
			deps[upstream] = struct{}{}
		}
		tracker.waiting[nodeID] = deps
	}

	for _, nodeID := range graph.order {
		tracker.outgoing[nodeID] = graph.DownstreamNodes(nodeID)
	}

	return tracker
}

// Seed initialises the tracker from nodes that already exist for this analysis,
// so a rerun resumes from previously finished work.
func (t *DependencyTracker) Seed(existing map[string]*types.AnalysisNode) {
	nodeIDs := make([]string, 0, len(existing))
	for nodeID := range existing {
		if _, ok := t.waiting[nodeID]; ok {
			nodeIDs = append(nodeIDs, nodeID)
		}
	}
	// Deterministic order keeps the cascade reproducible in tests.
	sort.Strings(nodeIDs)

	for _, nodeID := range nodeIDs {
		node := existing[nodeID]
		if node == nil {
			continue
		}
		switch {
		case isSuccessNode(node):
			t.OnSuccess(nodeID)
		case isTerminalNode(node):
			t.OnFailure(nodeID)
		}
	}
}

// OnSuccess records that nodeID produced a reusable result and returns the
// immediately affected downstream identities.
func (t *DependencyTracker) OnSuccess(nodeID string) []string {
	nodeID = strings.TrimSpace(nodeID)
	if _, ok := t.waiting[nodeID]; !ok {
		return nil
	}

	// A late success cancels a previous failure cascade rooted at this node.
	delete(t.failed, nodeID)
	t.invalidate()

	touched := make([]string, 0, len(t.outgoing[nodeID]))
	for _, downstream := range t.outgoing[nodeID] {
		deps, ok := t.waiting[downstream]
		if !ok {
			continue
		}
		if _, pending := deps[nodeID]; pending {
			delete(deps, nodeID)
			touched = append(touched, downstream)
		}
	}
	return compactStrings(touched)
}

// OnFailure records that nodeID will never produce a reusable result and
// returns every transitively affected downstream identity.
func (t *DependencyTracker) OnFailure(nodeID string) []string {
	nodeID = strings.TrimSpace(nodeID)
	if _, ok := t.waiting[nodeID]; !ok {
		return nil
	}

	t.failed[nodeID] = struct{}{}
	t.invalidate()

	touched := make([]string, 0)
	visited := map[string]struct{}{nodeID: {}}
	queue := []string{nodeID}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for _, downstream := range t.outgoing[current] {
			touched = append(touched, downstream)
			if _, seen := visited[downstream]; seen {
				continue
			}
			visited[downstream] = struct{}{}
			queue = append(queue, downstream)
		}
	}
	return compactStrings(touched)
}

// IsReady reports whether every upstream of nodeID has succeeded and the node
// is not blocked.
func (t *DependencyTracker) IsReady(nodeID string) bool {
	deps, ok := t.waiting[strings.TrimSpace(nodeID)]
	if !ok {
		return false
	}
	if t.IsBlocked(nodeID) {
		return false
	}
	return len(deps) == 0
}

// IsBlocked reports whether any remaining upstream of nodeID has failed.
func (t *DependencyTracker) IsBlocked(nodeID string) bool {
	nodeID = strings.TrimSpace(nodeID)
	if _, ok := t.waiting[nodeID]; !ok {
		return false
	}
	for upstream := range t.waiting[nodeID] {
		if t.isFailedOrBlocked(upstream, map[string]struct{}{}) {
			return true
		}
	}
	return false
}

// Runnable returns every node that is currently ready or blocked, in a stable
// order. It is used by the watchdog as a full reconciliation hint.
func (t *DependencyTracker) Runnable() []string {
	out := make([]string, 0, len(t.waiting))
	for nodeID := range t.waiting {
		if t.IsReady(nodeID) || t.IsBlocked(nodeID) {
			out = append(out, nodeID)
		}
	}
	sort.Strings(out)
	return out
}

// PendingUpstreams returns how many upstreams of nodeID are still unresolved.
// It is used for diagnostics and logging only.
func (t *DependencyTracker) PendingUpstreams(nodeID string) int {
	return len(t.waiting[strings.TrimSpace(nodeID)])
}

// isFailedOrBlocked walks upstream until it finds a failed ancestor.
func (t *DependencyTracker) isFailedOrBlocked(nodeID string, visiting map[string]struct{}) bool {
	if cached, ok := t.blockedCache[nodeID]; ok {
		return cached
	}
	if _, ok := t.failed[nodeID]; ok {
		t.blockedCache[nodeID] = true
		return true
	}
	if _, ok := visiting[nodeID]; ok {
		// Defensive: malformed cyclic graph, treat as not blocked.
		return false
	}
	visiting[nodeID] = struct{}{}
	defer delete(visiting, nodeID)

	blocked := false
	for upstream := range t.waiting[nodeID] {
		if t.isFailedOrBlocked(upstream, visiting) {
			blocked = true
			break
		}
	}
	t.blockedCache[nodeID] = blocked
	return blocked
}

// invalidate drops the derived blocking cache after any state mutation.
func (t *DependencyTracker) invalidate() {
	if len(t.blockedCache) == 0 {
		return
	}
	t.blockedCache = make(map[string]bool)
}
