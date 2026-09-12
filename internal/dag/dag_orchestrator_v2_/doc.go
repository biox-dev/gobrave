// Package orchestratorv2 implements the dynamic (Nextflow-like) DAG scheduler
// used by gobrave analyses.
//
// # Why a separate package
//
// The previous implementation lived in a single
// internal/application/service/dag_orchestrator_v2.go file that mixed graph
// compilation, persistence, cache policy, dispatch and lifecycle concerns.
// This package keeps the exact same public contract
// (the dynamic scheduler contract) and the exact same DI constructor
// signature, but splits the scheduler into small, single-responsibility
// collaborators that can be unit tested and replaced independently.
//
// # Scheduling model
//
// A run is a data-driven materialization loop over a compiled graph:
//
//	spec.Graph          immutable snapshot of the compiled workflow
//	DependencyTracker   in-memory readiness/blocking state per node
//	Reconciler          turns ready templates into analysis_node rows
//	RuntimeEngine       atomic ready-node claim + snapshot (internal/dag)
//	WorkerPool          bounded dispatch to the NodeDispatcher
//
// The loop is event driven: node completion/failure events coming from the
// runtime bus advance the DependencyTracker, which yields the only templates
// that need re-evaluation. A periodic watchdog repeats the same reconciliation
// so a dropped event can never stall a run.
//
// # Design patterns
//
//   - Facade:      Orchestrator hides the whole lifecycle from callers.
//   - Builder:     NodeBuilder assembles types.AnalysisNode rows.
//   - Strategy:    CachePolicy / RetryPolicy encapsulate swappable decisions.
//   - Registry:    CachePolicyRegistry resolves a policy per analysis cache type.
//   - Observer:    EventBridge fans runtime events out to at most one bus handler.
//   - State:       DependencyTracker + dag state machine drive node transitions.
//
// # Extension points
//
// All extension points are injected through Dependencies plus functional
// Options, so new behaviour (extra cache policies, retry strategies, stronger
// leases) can be added without touching the run loop.
package orchestratorv2
