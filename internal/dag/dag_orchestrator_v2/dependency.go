package orchestratorv2

import (
	"strings"

	dagruntime "github.com/biox-dev/gobrave/internal/dag"
	"github.com/biox-dev/gobrave/internal/types"
)

// dynamicExecutionPlan is the compiled, static schedule of one dynamic DAG run.
//
// It replaces the old mutable dependency manager. Readiness is no longer maintained
// by pushing success/failure through the graph: it is *derived* from the single
// source of truth - the persisted analysis_node rows plus the decisions this run has
// already taken - so a cache-invalidated node that is flipped back to ready simply
// stops being "satisfied", and every dependant stops being runnable in the same pass.
// There is nothing to re-arm and nothing to hold back.
//
// order is the compiler's topological order (see runDynamicLoop). Walking it
// upstream-first is the whole ordering guarantee: by the time a node is examined its
// ancestors have already been settled and cannot change again within the pass.
type dynamicExecutionPlan struct {
	order []string
	nodes map[string]dynamicPlanNode
}

// dynamicPlanNode is one compiled template plus its compiled relations.
type dynamicPlanNode struct {
	template   map[string]any
	upstream   []string
	downstream []string
	incoming   []*types.AnalysisEdge
}

// newDynamicExecutionPlan indexes the compiled node templates and edges.
//
// upstream comes from the compiler emitted upstream_ids rather than from the edge
// rows: it is the authoritative dependency list of the template, and the edges are
// only needed to merge an upstream's outputs into a dependant's inputs.
func newDynamicExecutionPlan(nodeTemplates []map[string]any, edges []*types.AnalysisEdge) *dynamicExecutionPlan {
	outgoing := buildOutgoingNodeMap(edges)
	incoming := buildIncomingEdgeMap(edges)

	plan := &dynamicExecutionPlan{
		order: make([]string, 0, len(nodeTemplates)),
		nodes: make(map[string]dynamicPlanNode, len(nodeTemplates)),
	}
	for _, row := range nodeTemplates {
		nodeID := strings.TrimSpace(dynamicToString(row["node_id"]))
		if nodeID == "" {
			continue
		}
		if _, exists := plan.nodes[nodeID]; exists {
			continue
		}
		plan.order = append(plan.order, nodeID)
		plan.nodes[nodeID] = dynamicPlanNode{
			template:   row,
			upstream:   dynamicToStringSlice(row["upstream_ids"]),
			downstream: outgoing[nodeID],
			incoming:   incoming[nodeID],
		}
	}
	return plan
}

// dynamicState is the derived readiness view over persisted nodes for one reconcile
// pass. Every readiness question is a pure query over it, never a maintained set.
type dynamicState struct {
	plan  *dynamicExecutionPlan
	nodes map[string]*types.AnalysisNode

	// satisfiedCache and blocked memoise the derived queries. Memoisation is safe because
	// a query only ever walks upstream and the reconcile pass visits nodes in topological
	// order, so an ancestor is always final before a dependant asks.
	satisfiedCache map[string]bool
	blocked        map[string]bool
	// satisfying and visiting are the recursion guards for the two derived queries.
	satisfying map[string]bool
	visiting   map[string]bool
}

// newDynamicState builds the view from the persisted node rows of one analysis.
func newDynamicState(plan *dynamicExecutionPlan, existing []*types.AnalysisNode) *dynamicState {
	nodes := make(map[string]*types.AnalysisNode, len(existing))
	for _, node := range existing {
		if node == nil {
			continue
		}
		nodes[strings.TrimSpace(node.NodeID)] = node
	}
	return &dynamicState{
		plan:           plan,
		nodes:          nodes,
		satisfiedCache: make(map[string]bool),
		blocked:        make(map[string]bool),
		satisfying:     make(map[string]bool),
		visiting:       make(map[string]bool),
	}
}

// satisfied reports whether nodeID has a result a dependant may consume right now.
//
// Satisfaction is transitive on purpose: the node itself must be reusable (successful,
// or a ready cache hit) AND every upstream must be satisfied too. When an ancestor is
// queued for a rerun, its cached descendants are stale as well, so this one derived
// query is what replaces the old re-arm: flipping a node back to ready invalidates the
// whole transitive subtree automatically.
func (s *dynamicState) satisfied(nodeID string) bool {
	nodeID = strings.TrimSpace(nodeID)
	if value, ok := s.satisfiedCache[nodeID]; ok {
		return value
	}
	if !isSuccessNode(s.nodes[nodeID]) {
		s.satisfiedCache[nodeID] = false
		return false
	}
	if s.satisfying[nodeID] {
		// Defensive: a malformed cyclic graph is treated as not satisfied.
		return false
	}
	s.satisfying[nodeID] = true
	defer delete(s.satisfying, nodeID)

	for _, upstream := range s.plan.nodes[nodeID].upstream {
		if !s.satisfied(upstream) {
			s.satisfiedCache[nodeID] = false
			return false
		}
	}
	s.satisfiedCache[nodeID] = true
	return true
}

// isBlocked reports whether any upstream of nodeID failed (directly or transitively),
// in which case the node can never run and must be materialized as skipped.
func (s *dynamicState) isBlocked(nodeID string) bool {
	nodeID = strings.TrimSpace(nodeID)
	if value, ok := s.blocked[nodeID]; ok {
		return value
	}
	if s.visiting[nodeID] {
		// Defensive: a malformed cyclic graph is treated as not blocked.
		return false
	}
	s.visiting[nodeID] = true
	defer delete(s.visiting, nodeID)

	blocked := false
	for _, upstream := range s.plan.nodes[nodeID].upstream {
		if s.failedOrBlocked(upstream) {
			blocked = true
			break
		}
	}
	s.blocked[nodeID] = blocked
	return blocked
}

// failedOrBlocked reports whether nodeID itself failed or is blocked by an upstream.
func (s *dynamicState) failedOrBlocked(nodeID string) bool {
	nodeID = strings.TrimSpace(nodeID)
	if isFailedNode(s.nodes[nodeID]) {
		return true
	}
	return s.isBlocked(nodeID)
}

// canRun reports whether every upstream of nodeID is satisfied and none is blocked.
// This is the derived definition of "ready": nothing is stored, so a rerun decision
// taken on an ancestor is visible here immediately.
func (s *dynamicState) canRun(nodeID string) bool {
	nodeID = strings.TrimSpace(nodeID)
	if s.isBlocked(nodeID) {
		return false
	}
	for _, upstream := range s.plan.nodes[nodeID].upstream {
		if !s.satisfied(upstream) {
			return false
		}
	}
	return true
}

// isFailedNode reports whether a node ended without a reusable result. Terminal
// non-success statuses (failed / skipped / stopped) are exactly the ones that must
// block every transitive dependant.
func isFailedNode(node *types.AnalysisNode) bool {
	if node == nil {
		return false
	}
	status := normaliseNodeStatus(node)
	if status == "" || status == dagruntime.StatusPending {
		return false
	}
	return dagruntime.IsTerminalStatus(status) && !dagruntime.IsSuccessStatus(status)
}
