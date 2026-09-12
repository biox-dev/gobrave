package orchestratorv2

import (
	"sort"
	"strings"

	dagruntime "github.com/biox-dev/gobrave/internal/dag"
	"github.com/biox-dev/gobrave/internal/types"
)

type dynamicDependencyManager struct {
	waiting  map[string]map[string]struct{}
	blocked  map[string]bool
	outgoing map[string][]string
	// topoRank is the upstream-first position of every template, computed once from
	// the compiled topology. It is the ordering guard rail of a single reconcile
	// pass: a node must be decided only after its ancestors, otherwise a rerun
	// decision could be taken too late to hold its dependants back (see
	// OrderByTopology and ReArmTransitive).
	topoRank map[string]int
}

func newDynamicDependencyManager(nodeTemplateByID map[string]map[string]any, outgoing map[string][]string) *dynamicDependencyManager {
	waiting := make(map[string]map[string]struct{}, len(nodeTemplateByID))
	blocked := make(map[string]bool, len(nodeTemplateByID))
	upstream := make(map[string][]string, len(nodeTemplateByID))
	for nodeID, row := range nodeTemplateByID {
		upstreamIDs := dynamicToStringSlice(row["upstream_ids"])
		deps := make(map[string]struct{}, len(upstreamIDs))
		for _, id := range upstreamIDs {
			deps[id] = struct{}{}
		}
		waiting[nodeID] = deps
		blocked[nodeID] = false
		upstream[nodeID] = upstreamIDs
	}
	return &dynamicDependencyManager{
		waiting:  waiting,
		blocked:  blocked,
		outgoing: outgoing,
		topoRank: dynamicTopologicalRank(upstream),
	}
}

func (m *dynamicDependencyManager) SeedFromExisting(existing map[string]*types.AnalysisNode) {
	for nodeID, node := range existing {
		if node == nil {
			continue
		}
		status := strings.ToLower(strings.TrimSpace(node.Status))
		if status == dagruntime.StatusReady && node.CacheHit {
			_ = m.OnNodeSuccess(nodeID)
			continue
		}
		if dagruntime.IsSuccessStatus(status) {
			_ = m.OnNodeSuccess(nodeID)
			continue
		}
		if dagruntime.IsTerminalStatus(status) {
			_ = m.OnNodeFailure(nodeID)
		}
	}
}

func (m *dynamicDependencyManager) InitialCandidates() []string {
	out := make([]string, 0)
	for nodeID := range m.waiting {
		if m.IsReady(nodeID) || m.IsBlocked(nodeID) {
			out = append(out, nodeID)
		}
	}
	sort.Strings(out)
	return out
}

func (m *dynamicDependencyManager) OnNodeSuccess(nodeID string) []string {
	touched := map[string]struct{}{}
	for _, downstream := range m.outgoing[nodeID] {
		deps, ok := m.waiting[downstream]
		if !ok {
			continue
		}
		if _, exists := deps[nodeID]; exists {
			delete(deps, nodeID)
			touched[downstream] = struct{}{}
		}
	}
	return dynamicSortedKeys(touched)
}

func (m *dynamicDependencyManager) OnNodeFailure(nodeID string) []string {
	queue := []string{nodeID}
	visited := map[string]struct{}{nodeID: {}}
	touched := map[string]struct{}{}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for _, downstream := range m.outgoing[current] {
			touched[downstream] = struct{}{}
			if !m.blocked[downstream] {
				m.blocked[downstream] = true
			}
			if _, seen := visited[downstream]; seen {
				continue
			}
			visited[downstream] = struct{}{}
			queue = append(queue, downstream)
		}
	}
	return dynamicSortedKeys(touched)
}

func (m *dynamicDependencyManager) IsReady(nodeID string) bool {
	deps, ok := m.waiting[nodeID]
	if !ok {
		return false
	}
	if m.blocked[nodeID] {
		return false
	}
	return len(deps) == 0
}

func (m *dynamicDependencyManager) IsBlocked(nodeID string) bool {
	return m.blocked[nodeID]
}

// OrderByTopology sorts candidate node ids upstream-first.
//
// A reconcile pass decides several nodes in a row, and a rerun decision taken for one
// node changes the readiness of everything below it. Deciding a node before its
// ancestors would therefore let a dependant be marked ready in the same pass that its
// upstream is queued for a rerun - exactly the "every node is submitted at once"
// failure. Nodes that are not part of the compiled graph keep the highest rank.
func (m *dynamicDependencyManager) OrderByTopology(candidates []string) []string {
	if m == nil || len(candidates) == 0 {
		return candidates
	}
	ordered := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		ordered = append(ordered, strings.TrimSpace(candidate))
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		left := m.rankOf(ordered[i])
		right := m.rankOf(ordered[j])
		if left == right {
			return ordered[i] < ordered[j]
		}
		return left < right
	})
	return ordered
}

// rankOf returns the topological position of nodeID, or the "unknown" rank (past
// every known node) when the identity is not part of the compiled graph.
func (m *dynamicDependencyManager) rankOf(nodeID string) int {
	if rank, ok := m.topoRank[nodeID]; ok {
		return rank
	}
	return len(m.topoRank)
}

// ReArmTransitive restores nodeID as a pending dependency for every transitive
// downstream node and returns the affected node ids.
//
// Seeding a previously successful node as "satisfied" removed it from every
// dependant's waiting set, so flipping that node back to ready must also put it back
// into those waiting sets. Without this, a dependant that was already satisfied would
// be dispatched before the upstream it consumes even started, and the input files it
// expects would not exist yet. The operation is idempotent: it only ever adds pending
// dependencies and never removes one.
func (m *dynamicDependencyManager) ReArmTransitive(nodeID string) []string {
	if m == nil {
		return nil
	}
	nodeID = strings.TrimSpace(nodeID)
	if nodeID == "" {
		return nil
	}

	affected := make([]string, 0)
	visited := map[string]struct{}{nodeID: {}}
	queue := []string{nodeID}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for _, downstream := range m.outgoing[current] {
			downstream = strings.TrimSpace(downstream)
			if downstream == "" {
				continue
			}
			if deps, ok := m.waiting[downstream]; ok {
				deps[current] = struct{}{}
			}
			if _, seen := visited[downstream]; seen {
				continue
			}
			visited[downstream] = struct{}{}
			affected = append(affected, downstream)
			queue = append(queue, downstream)
		}
	}
	return affected
}

// dynamicTopologicalRank assigns every node an upstream-first position with Kahn's
// algorithm over the compiled upstream relations.
//
// A node left unranked belongs to a cycle; it is placed after every ranked node so it
// is still reconciled, just last. Correctness does not depend on that fallback: the
// dependency manager itself reports a node in a cycle as never ready.
func dynamicTopologicalRank(upstream map[string][]string) map[string]int {
	indegree := make(map[string]int, len(upstream))
	children := make(map[string][]string, len(upstream))
	for nodeID := range upstream {
		indegree[nodeID] = 0
	}
	for nodeID, deps := range upstream {
		for _, dep := range deps {
			dep = strings.TrimSpace(dep)
			if _, known := upstream[dep]; !known {
				continue
			}
			children[dep] = append(children[dep], nodeID)
			indegree[nodeID]++
		}
	}

	roots := make([]string, 0, len(indegree))
	for nodeID, degree := range indegree {
		if degree == 0 {
			roots = append(roots, nodeID)
		}
	}
	sort.Strings(roots)

	rank := make(map[string]int, len(indegree))
	next := 0
	for len(roots) > 0 {
		current := roots[0]
		roots = roots[1:]
		rank[current] = next
		next++
		for _, child := range children[current] {
			indegree[child]--
			if indegree[child] == 0 {
				roots = append(roots, child)
			}
		}
	}

	for nodeID := range upstream {
		if _, ranked := rank[nodeID]; !ranked {
			rank[nodeID] = next
		}
	}
	return rank
}
