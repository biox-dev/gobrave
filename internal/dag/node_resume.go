package dag

import "strings"

// ResumeNodeStatusForRestart decides how an in-flight node should be treated after
// the owning process restarted.
//
// It is shared by every scheduler that resumes work from the analysis_nodes table
// (legacy DagOrchestrator and the dynamic V2 orchestrator) so that restart semantics
// stay identical across scheduling paths.
//
// Return values:
//   - (StatusReady, true): the node lost its runner, it must be re-dispatched.
//   - ("", false): keep the current status. This covers nodes that were never
//     in-flight, and running nodes whose container is still alive — those are
//     expected to be finalized by deferred reconciliation instead.
func ResumeNodeStatusForRestart(status string, hasContainer bool) (string, bool) {
	status = strings.TrimSpace(strings.ToLower(status))
	switch status {
	case StatusSubmitted:
		return StatusReady, true
	case StatusRunning:
		if hasContainer {
			return "", false
		}
		return StatusReady, true
	default:
		return "", false
	}
}
