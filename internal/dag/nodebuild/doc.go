package nodebuild

// Package nodebuild owns the "compiled template -> persisted analysis_node"
// translation shared by every dynamic DAG scheduler.
//
// # Why this package exists
//
// The V2 (dynamic materialization) and V3 (dataflow) schedulers materialize
// analysis_node rows at runtime. Historically each scheduler carried its own
// copy of that logic, which meant:
//
//   - the on-disk workspace contract could drift between schedulers, so a node
//     produced by one scheduler was invisible to the other, and
//   - the instance identity (node id + resolved inputs/params) hashed
//     differently, so one scheduler could not recognize the other's nodes as a
//     cache hit.
//
// By moving the translation here - workspace layout, JSON normalization,
// upstream input bootstrapping and instance identity - a node persisted by V2 is
// byte-for-byte the same shape as one persisted by V3. That is what makes the
// cache bidirectional: V3 can reuse V2 nodes and vice versa.
//
// # Contract
//
// A node is identified by the pair (NodeID, InstanceInputHash):
//
//   - NodeID is the compiler-supplied runtime identity used by edges.
//   - InstanceInputHash is a stable digest of NodeID + resolved inputs + params
//     and distinguishes the several instances a scattered process can produce.
//
// The workspace layout is analysis.output_dir/<node record id>/ with run.sh,
// params.json, command.log, output/ and cached/ below it. Both schedulers derive
// it through ResolveLayout, so the artifact paths stored on the row always point
// at the same files no matter which scheduler created the node.
