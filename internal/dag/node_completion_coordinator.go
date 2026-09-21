package dag

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/biox-dev/gobrave/internal/config"
	"github.com/biox-dev/gobrave/internal/event"
	"github.com/biox-dev/gobrave/internal/logger"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"gorm.io/gorm"
)

var _ event.Handler = (*NodeCompletionCoordinator)(nil)

type NodeCompletionCoordinator struct {
	analysisRepo   interfaces.AnalysisRepository
	containerRepo  interfaces.ContainerRepository
	containerOps   NodeContainerOperator
	outputResolver nodeOutputResolver
	runtime        *RuntimeEngine
	bus            event.Bus
	// cleanup         NodeFailureCleanupFunc
	deleteOnSuccess bool
	pollInterval    time.Duration
	pollBatchLimit  int

	// graceWait/graceInterval bound the wait for a node's declared outputs to
	// become visible after its container exited; see resolveOutputsWithGrace.
	// Kept as fields so the policy is exercised in tests at a scale of milliseconds.
	graceWait     time.Duration
	graceInterval time.Duration

	reconcileMu sync.Mutex
	inFlight    map[int64]struct{}
}

type NodeContainerOperator interface {
	Delete(ctx context.Context, id int64) error
}

func NewNodeCompletionCoordinator(
	analysisRepo interfaces.AnalysisRepository,
	containerRepo interfaces.ContainerRepository,
	containerOps NodeContainerOperator,
	bus event.Bus,
	cfg *config.Config,
) *NodeCompletionCoordinator {
	runtime := NewRuntimeEngine(analysisRepo)
	pollInterval := 2 * time.Second
	deleteOnSuccess := false
	// cleanup := buildNodeFailureCleanup(containerRepo, containerOps, cfg)

	if cfg != nil && cfg.Container != nil {
		deleteOnSuccess = cfg.Container.DeleteContainerOnNodeSuccess
	}
	if pollInterval <= 0 {
		pollInterval = 2 * time.Second
	}
	return &NodeCompletionCoordinator{
		analysisRepo:   analysisRepo,
		containerRepo:  containerRepo,
		containerOps:   containerOps,
		outputResolver: newFileSystemNodeOutputResolver(),
		runtime:        runtime,
		bus:            bus,
		// cleanup:         cleanup,
		deleteOnSuccess: deleteOnSuccess,
		pollInterval:    pollInterval,
		pollBatchLimit:  0,
		graceWait:       nodeOutputsGraceWait,
		graceInterval:   nodeOutputsGraceInterval,
		inFlight:        make(map[int64]struct{}),
	}
}

// func buildNodeFailureCleanup(
// 	containerRepo interfaces.ContainerRepository,
// 	containerOps NodeContainerOperator,
// 	cfg *config.Config,
// ) NodeFailureCleanupFunc {
// 	if containerRepo == nil || containerOps == nil {
// 		return nil
// 	}
// 	if cfg == nil || cfg.Container == nil {
// 		return nil
// 	}
// 	if !strings.EqualFold(strings.TrimSpace(cfg.Container.DagNodeCleanupOnFailed), "delete") {
// 		return nil
// 	}

// 	return func(ctx context.Context, node *types.AnalysisNode) {
// 		if node == nil || node.ID == 0 {
// 			return
// 		}
// 		instances, err := containerRepo.ListContainerInstanceByOwnerTypeAndOwnerIDs(ctx, types.ContainerOwnerDagNode, []int64{int64(node.ID)})
// 		if err != nil {
// 			logger.Warnf(ctx, "[NodeCompletionCoordinator] list container instances for failed node cleanup failed, node_id=%s err=%v", node.NodeID, err)
// 			return
// 		}
// 		for _, inst := range instances {
// 			if inst == nil || inst.OwnerType != types.ContainerOwnerDagNode || inst.OwnerID != int64(node.ID) {
// 				continue
// 			}
// 			if err := containerOps.Delete(ctx, inst.ID); err != nil {
// 				logger.Warnf(ctx, "[NodeCompletionCoordinator] failed node cleanup delete failed, instance_id=%d runtime_id=%s err=%v", inst.ID, inst.RuntimeID, err)
// 			}
// 		}
// 	}
// }

func (c *NodeCompletionCoordinator) Handle(evt event.Event) {
	ce, ok := evt.(types.ContainerEvent)
	if !ok {
		return
	}

	eventName := strings.TrimSpace(ce.Event)
	switch eventName {
	case "ContainerStopped", "ContainerFailed", "ContainerDeleted":
		c.reconcileContainerByID(context.Background(), ce.ContainerInstanceID, eventName)
	default:
	}
}

// func (c *NodeCompletionCoordinator) Start(ctx context.Context) {
// 	if c == nil || c.containerRepo == nil || c.runtime == nil {
// 		return
// 	}

// 	ticker := time.NewTicker(c.pollInterval)
// 	defer ticker.Stop()

// 	for {
// 		select {
// 		case <-ctx.Done():
// 			return
// 		case <-ticker.C:
// 			c.pollOnce(ctx)
// 		}
// 	}
// }

// func (c *NodeCompletionCoordinator) pollOnce(ctx context.Context) {
// 	instances, err := c.containerRepo.ListContainerInstance(ctx)
// 	if err != nil {
// 		logger.Warnf(ctx, "[NodeCompletionCoordinator] list container instances failed: %v", err)
// 		return
// 	}

// 	processed := 0
// 	for _, inst := range instances {
// 		if inst == nil || inst.OwnerType != types.ContainerOwnerDagNode {
// 			continue
// 		}
// 		if !c.isContainerTerminal(inst.Status) {
// 			continue
// 		}
// 		if c.pollBatchLimit > 0 && processed >= c.pollBatchLimit {
// 			break
// 		}
// 		processed++
// 		c.reconcileContainerByID(ctx, inst.ID, "poll")
// 	}
// }

func (c *NodeCompletionCoordinator) reconcileContainerByID(ctx context.Context, containerInstanceID int64, source string) {
	if !c.beginReconcile(containerInstanceID) {
		return
	}
	defer c.endReconcile(containerInstanceID)

	inst, err := c.containerRepo.GetContainerInstanceByID(ctx, containerInstanceID)
	if err != nil {
		logger.Warnf(ctx, "[NodeCompletionCoordinator] load container instance failed, source=%s instance_id=%d err=%v", source, containerInstanceID, err)
		return
	}
	c.reconcileContainer(ctx, inst, source)
}

func (c *NodeCompletionCoordinator) beginReconcile(containerInstanceID int64) bool {
	if c == nil || containerInstanceID <= 0 {
		return false
	}
	c.reconcileMu.Lock()
	defer c.reconcileMu.Unlock()
	if _, exists := c.inFlight[containerInstanceID]; exists {
		return false
	}
	c.inFlight[containerInstanceID] = struct{}{}
	return true
}

func (c *NodeCompletionCoordinator) endReconcile(containerInstanceID int64) {
	if c == nil || containerInstanceID <= 0 {
		return
	}
	c.reconcileMu.Lock()
	delete(c.inFlight, containerInstanceID)
	c.reconcileMu.Unlock()
}

func (c *NodeCompletionCoordinator) reconcileContainer(ctx context.Context, inst *types.ContainerInstance, source string) {
	if inst == nil || inst.OwnerType != types.ContainerOwnerDagNode || inst.OwnerID <= 0 {
		return
	}

	node, err := c.analysisRepo.GetAnalysisNodeByID(ctx, inst.OwnerID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.cleanupOrphanContainer(ctx, inst, source, "analysis node not found")
			return
		}
		logger.Warnf(ctx, "[NodeCompletionCoordinator] load analysis node failed, source=%s owner_id=%d err=%v", source, inst.OwnerID, err)
		return
	}
	if node == nil {
		c.cleanupOrphanContainer(ctx, inst, source, "analysis node is nil")
		return
	}

	nodeStatus := strings.TrimSpace(strings.ToLower(node.Status))
	if IsTerminalStatus(nodeStatus) {
		c.cleanupOrphanContainer(ctx, inst, source, "analysis node terminal")
		return
	}
	if nodeStatus != StatusRunning && nodeStatus != StatusSubmitted && nodeStatus != StatusStopping {
		return
	}

	finalStatus, exitCode, errorMessage, shouldComplete := c.resolveNodeStatus(node, inst)
	if !shouldComplete {
		return
	}

	// The completion decision is logged with the container facts that decide whether
	// the terminal event could have come from the container finishing its work.
	// container_ran_for is the strongest signal available at this point: a terminal
	// event with start_observed=false, or with a run time of (almost) zero, cannot
	// come from a container that ran its script and wrote its outputs. Combined with
	// the runtime-side evidence line, this separates "the container really finished"
	// from "the exit event was premature and the node is being failed for a file the
	// container never got the chance to write".
	//
	// Kept at info on purpose: it is one line per node and is this side's only record
	// of why the node was completed the way it was, so it must survive a log level of
	// info. Purely mechanical steps around it are logged at debug.
	logger.Infof(ctx, "[NodeCompletionCoordinator] completing node from container terminal event, source=%s analysis_id=%d node_id=%s node_status=%s instance_id=%d runtime_id=%s container_status=%s container_exit_code=%s container_started_at=%v container_finished_at=%v container_ran_for=%s start_observed=%t resolved_status=%s",
		source, node.AnalysisID, node.NodeID, nodeStatus, inst.ID, inst.RuntimeID, inst.Status,
		formatContainerExitCode(inst), inst.StartedAt, inst.FinishedAt, containerRanFor(inst),
		inst.StartedAt != nil, finalStatus)

	outputs, outputErrors := c.buildResolvedOutputs(ctx, node, inst)
	outputValidationFailed := false
	if finalStatus == StatusDone && len(outputErrors) > 0 {
		// One line per affected node, carrying the container facts and the outcome.
		// The container facts cannot decide on their own whether this is a real
		// failure (the container exited and produced nothing) or a visibility problem
		// (the outputs are on the shared mount but this process could not read them
		// yet): the exit code is 0 in both cases. What separates them is logged
		// immediately after, by logOutputValidationDiagnosis, from the one pair of
		// reads that disagree. The per-retry reads are logged at debug level, so this
		// warning is the only place the outcome is stated.
		logger.Warnf(ctx, "[NodeCompletionCoordinator] declared outputs missing after grace wait, source=%s analysis_id=%d node_id=%s instance_id=%d runtime_id=%s container_status=%s container_exit_code=%s container_started_at=%v container_finished_at=%v output_dir=%s missing=%s",
			source, node.AnalysisID, node.NodeID, inst.ID, inst.RuntimeID, inst.Status, formatContainerExitCode(inst), inst.StartedAt, inst.FinishedAt, node.OutputDir, strings.Join(outputErrors, "; "))
		finalStatus = StatusFailed
		if exitCode == 0 {
			exitCode = 1
		}
		errorMessage = fmt.Sprintf("output validation failed: %s", strings.Join(outputErrors, "; "))
		outputValidationFailed = true
	}
	if outputValidationFailed {
		// One complete statement of what this process could see while it decided the
		// outputs were missing, plus a bounded re-check of the same path afterwards.
		// Together they separate "the container wrote nothing" from "the file is on
		// the server and we could not read it in time", which is the only way to tell
		// a real node failure from a read-visibility race on the shared workspace.
		c.logOutputValidationDiagnosis(ctx, node, inst, outputErrors)
		c.scheduleOutputVisibilityPostMortem(node, inst)
	}
	if _, err := c.runtime.CompleteNode(ctx, node.ID, finalStatus, outputs, exitCode, errorMessage, outputErrors); err != nil {
		latest, latestErr := c.analysisRepo.GetAnalysisNodeByAnalysisNodeID(ctx, node.AnalysisNodeID)
		if latestErr == nil && latest != nil && IsTerminalStatus(latest.Status) {
			return
		}
		logger.Warnf(ctx, "[NodeCompletionCoordinator] complete node failed, source=%s analysis_id=%d node_id=%s status=%s err=%v", source, node.AnalysisID, node.NodeID, finalStatus, err)
		return
	}
	// TODO 目前容器失败直接删除, 后续需要可配置, 方便查看失败日志
	// if finalStatus == StatusFailed {
	// 	c.runCleanup(ctx, node)
	// } else if finalStatus == StatusDone {

	// }
	c.cleanupSuccessfulContainer(ctx, inst, source)
	c.publishNodeResult(node, finalStatus, exitCode, errorMessage)
}

func (c *NodeCompletionCoordinator) cleanupSuccessfulContainer(ctx context.Context, inst *types.ContainerInstance, source string) {
	if !c.deleteOnSuccess || inst == nil || c.containerOps == nil {
		return
	}
	// Logged before the call: the deletion is what makes a late or premature exit
	// event unrecoverable, so the log has to show whether the container was deleted
	// from the completion path and at what time.
	logger.Debugf(ctx, "[NodeCompletionCoordinator] deleting container after node completion, source=%s instance_id=%d runtime_id=%s owner_id=%d container_status=%s container_exit_code=%s",
		source, inst.ID, inst.RuntimeID, inst.OwnerID, inst.Status, formatContainerExitCode(inst))
	if err := c.containerOps.Delete(ctx, inst.ID); err != nil {
		logger.Warnf(ctx, "[NodeCompletionCoordinator] cleanup successful container failed, source=%s instance_id=%d runtime_id=%s owner_id=%d err=%v", source, inst.ID, inst.RuntimeID, inst.OwnerID, err)
		return
	}
	logger.Infof(ctx, "[NodeCompletionCoordinator] cleaned successful container, source=%s instance_id=%d runtime_id=%s owner_id=%d", source, inst.ID, inst.RuntimeID, inst.OwnerID)
}

func (c *NodeCompletionCoordinator) buildResolvedOutputs(ctx context.Context, node *types.AnalysisNode, inst *types.ContainerInstance) (map[string]any, []string) {
	outputs := defaultContainerOutputs(inst)
	if node == nil {
		return outputs, nil
	}

	resolver := c.outputResolver
	if resolver == nil {
		resolver = newFileSystemNodeOutputResolver()
	}
	resolved, hardErrs := c.resolveOutputsWithGrace(ctx, resolver, node)
	errs := append(hardErrs, validateOutputPatterns(node, resolved)...)
	return resolved, errs
}

// nodeOutputsGraceWait bounds how long a node's completion waits for its
// outputs.json to become visible after the container reached a terminal state.
//
// The container writes the file as its last action, but neither the component
// that reports the exit (runtime/informer) nor this process's view of the
// workspace observes that write at the same instant, and the file can stay
// invisible here well after it exists on the shared mount (see refreshDirView).
// Waiting a bounded window is what keeps a file that lands late from being
// reported as a missing output and flipping the node from done to failed.
//
// The window is deliberately several times the observed lag (about one second
// between the container's last write and its exit being reported): the cost of
// waiting is one node staying running a little longer, while the cost of giving up
// early is a node that succeeded being recorded as failed and taking the whole run
// down with it.
const (
	nodeOutputsGraceWait     = 15 * time.Second
	nodeOutputsGraceInterval = 1 * time.Second
)

// resolveOutputsWithGrace resolves a node's outputs, refreshing this process's
// view of the output directory before each read so a stale cached lookup cannot
// hide a file the container has already written. Read/parse errors (hard failures)
// are returned immediately, and the final attempt is always returned as-is so a
// genuinely absent file still surfaces through validateOutputPatterns.
//
// The refresh runs before the first read as well as before every retry. The first
// read is the common case and the one that matters most: it decides both the node's
// verdict and the outputs recorded for its downstream nodes, and on a failing run
// three of fourteen first reads reported a missing file that had been on the shared
// mount for seconds. It is also the cheap case - the first read is still the only
// read on the happy path, and the retry loop below runs only for nodes whose
// declared outputs are not visible yet.
//
// Waiting is limited to nodes that declare output_patterns and are still missing a
// handle, so a node that legitimately produces no outputs.json does not pay the
// grace window.
func (c *NodeCompletionCoordinator) resolveOutputsWithGrace(ctx context.Context, resolver nodeOutputResolver, node *types.AnalysisNode) (map[string]any, []string) {
	dir, hasDir := nodeOutputDir(node)
	if hasDir {
		refreshDirView(ctx, dir)
	}

	resolved, missing, errs := resolver.Resolve(node, map[string]any{})
	if len(errs) > 0 || !missing || len(missingOutputHandles(node, resolved)) == 0 {
		return resolved, errs
	}

	started := time.Now()
	deadline := started.Add(c.graceWait)
	attempt := 1
	for time.Now().Before(deadline) {
		// A declared handle is still missing. Record what this process can see before
		// waiting, so the run keeps a per-attempt trace even when the final warning is
		// the only line an operator sees: it is what shows whether the file was never
		// produced, or was produced but not yet visible from here.
		if hasDir {
			detail, hidden := outputProbeDetail(dir, nodeOutputsFileName)
			logger.Debugf(ctx, "[NodeCompletionCoordinator] output probe attempt=%d elapsed=%s node_id=%s missing_handles=%s stat_vs_readdir_disagree_on=%v %s",
				attempt, time.Since(started).Round(time.Millisecond), node.NodeID,
				strings.Join(missingOutputHandles(node, resolved), ","), hidden, detail)
		}

		time.Sleep(c.graceInterval)
		if hasDir {
			// Only the probe above is a diagnosis; this refresh is the fix, so it is
			// called explicitly rather than relied on as a side effect of logging.
			refreshDirView(ctx, dir)
		}
		resolved, missing, errs = resolver.Resolve(node, map[string]any{})
		if len(errs) > 0 || !missing || len(missingOutputHandles(node, resolved)) == 0 {
			logger.Debugf(ctx, "[NodeCompletionCoordinator] declared outputs became readable after retry node_id=%s attempts=%d elapsed=%s",
				node.NodeID, attempt+1, time.Since(started).Round(time.Millisecond))
			return resolved, errs
		}
		attempt++
	}
	return resolved, errs
}

// refreshDirView re-reads a directory before the next attempt to read a file
// inside it, so that attempt is not answered from a stale cached lookup.
//
// This is not an optimisation, it is the fix for the failure the retry loop around
// it exists for. The analysis workspace is an NFS mount shared by this process and
// by the DAG node containers, and Prepare deletes a node's previous outputs just
// before it runs. That delete leaves this process holding a cached "does not
// exist" for outputs.json, and it keeps answering ENOENT from it after the
// container has recreated the file: the failing runs show six reads in a row
// reporting a file that had been on the server for seconds. Listing the directory
// refreshes the directory's attributes, which is what invalidates that cached
// answer, so the following open() reaches the server.
//
// A failure to list is only logged: the caller is already in the retry path and
// the resolution result is what it acts on.
func refreshDirView(ctx context.Context, dir string) {
	if _, err := os.ReadDir(dir); err != nil {
		logger.Debugf(ctx, "[NodeCompletionCoordinator] refresh output dir view failed, dir=%s err=%v", dir, err)
	}
}

// logOutputValidationDiagnosis states, once and in full, what the process could
// see when it decided that a node's declared outputs were missing.
//
// The verdict is drawn from the one fact that cannot be explained by a slow
// write: a name that readdir returns while stat answers ENOENT is present on the
// shared mount, and this process is answering from a cached negative lookup for
// it. Because Prepare deletes the previous outputs just before a node runs, that
// cache entry is created by this very process, which is why the failure is
// intermittent - it depends on whether the container's write is visible before
// the first probe caches the miss.
func (c *NodeCompletionCoordinator) logOutputValidationDiagnosis(ctx context.Context, node *types.AnalysisNode, inst *types.ContainerInstance, outputErrors []string) {
	if node == nil {
		return
	}
	dir, ok := nodeOutputDir(node)
	if !ok {
		logger.Warnf(ctx, "[NodeCompletionCoordinator] output validation diagnosis: node has no output dir analysis_id=%d node_id=%s missing=%s",
			node.AnalysisID, node.NodeID, strings.Join(outputErrors, "; "))
		return
	}

	detail, hidden := outputProbeDetail(dir, nodeOutputsFileName)
	logger.Warnf(ctx, "[NodeCompletionCoordinator] output validation diagnosis: analysis_id=%d node_id=%s instance_id=%d output_dir=%s missing=%s %s %s",
		node.AnalysisID, node.NodeID, containerInstanceID(inst), dir, strings.Join(outputErrors, "; "), detail, outputDirMountInfo(dir))
	if len(hidden) > 0 {
		logger.Warnf(ctx, "[NodeCompletionCoordinator] output validation diagnosis verdict: %v exist on the shared mount but were served from a cached negative lookup on this host (stat fails before the directory listing and succeeds after it); the container did produce them and the node was failed by a read-visibility race, not by a missing output", hidden)
	}
}

// outputVisibilityPostMortemOffsets are the moments after a failed completion at
// which outputs.json is re-checked. They straddle the default Linux NFS
// readdir/attribute cache windows (acdirmin 30s, acdirmax 60s), so a file that
// shows up in this range was on the server all along and only became visible to
// this process once its cached "does not exist" expired.
var outputVisibilityPostMortemOffsets = []time.Duration{
	1 * time.Second,
	5 * time.Second,
	15 * time.Second,
	30 * time.Second,
	45 * time.Second,
	60 * time.Second,
}

// scheduleOutputVisibilityPostMortem re-checks a failed node's outputs.json for a
// bounded window and logs the first moment it becomes readable, so the delay is
// measured instead of inferred. A file that appears here was never missing, which
// is the difference between a retry that works and a node that has to be fixed.
func (c *NodeCompletionCoordinator) scheduleOutputVisibilityPostMortem(node *types.AnalysisNode, inst *types.ContainerInstance) {
	if node == nil {
		return
	}
	path, ok := nodeOutputsPath(node)
	if !ok {
		return
	}
	finishedAt := "unknown"
	if inst != nil && inst.FinishedAt != nil {
		finishedAt = inst.FinishedAt.Format(time.RFC3339Nano)
	}

	go func() {
		ctx := context.Background()
		base := time.Now()
		for _, offset := range outputVisibilityPostMortemOffsets {
			time.Sleep(time.Until(base.Add(offset)))
			info, err := os.Stat(path)
			if err != nil {
				logger.Debugf(ctx, "[NodeCompletionCoordinator] output visibility post-mortem: still not readable analysis_id=%d node_id=%s path=%s after=%s err=%v",
					node.AnalysisID, node.NodeID, path, offset, err)
				continue
			}
			logger.Infof(ctx, "[NodeCompletionCoordinator] output visibility post-mortem: outputs became readable after the node was failed analysis_id=%d node_id=%s instance_id=%d path=%s container_finished_at=%s after_failure=%s size=%d mtime=%s mtime_vs_local_clock=%s",
				node.AnalysisID, node.NodeID, containerInstanceID(inst), path, finishedAt, offset,
				info.Size(), info.ModTime().Format(time.RFC3339Nano), time.Since(info.ModTime()).Round(time.Millisecond))
			return
		}
		logger.Warnf(ctx, "[NodeCompletionCoordinator] output visibility post-mortem: outputs never became readable analysis_id=%d node_id=%s path=%s container_finished_at=%s waited=%s",
			node.AnalysisID, node.NodeID, path, finishedAt, outputVisibilityPostMortemOffsets[len(outputVisibilityPostMortemOffsets)-1])
	}()
}

func containerInstanceID(inst *types.ContainerInstance) int64 {
	if inst == nil {
		return 0
	}
	return inst.ID
}

// containerRanFor renders how long the container was observed running, for logs.
//
// "unobserved" (no started_at) and a near-zero duration are the fingerprints of a
// terminal event that cannot have come from the container executing its script: the
// instance either never reached running state, or the exit was reported at the same
// instant its start was compensated. Both point at the runtime event, not at the
// node's outputs.
func containerRanFor(inst *types.ContainerInstance) string {
	if inst == nil || inst.StartedAt == nil {
		return "unobserved"
	}
	if inst.FinishedAt == nil {
		return "unknown(finished_at not set)"
	}
	return inst.FinishedAt.Sub(*inst.StartedAt).Round(time.Millisecond).String()
}

// formatContainerExitCode renders a container instance's exit code for logs.
// "none" means the runtime never reported one, which is itself a useful fact: a
// terminal event that arrived without an exit code (for example a stop request or
// a workload that was deleted) looks very different from a real process exit.
func formatContainerExitCode(inst *types.ContainerInstance) string {
	if inst == nil || inst.ExitCode == nil {
		return "none"
	}
	return strconv.Itoa(*inst.ExitCode)
}

func defaultContainerOutputs(inst *types.ContainerInstance) map[string]any {
	outputs := map[string]any{}
	if inst == nil {
		return outputs
	}
	outputs["container_instance_id"] = inst.ID
	outputs["container_runtime_id"] = inst.RuntimeID
	outputs["container_ip"] = inst.IPAddress
	outputs["container_status"] = string(inst.Status)
	outputs["container_owner_type"] = string(inst.OwnerType)
	outputs["container_owner_id"] = inst.OwnerID
	if inst.ExitCode != nil {
		outputs["container_exit_code"] = *inst.ExitCode
	}
	return outputs
}

func (c *NodeCompletionCoordinator) resolveNodeStatus(node *types.AnalysisNode, inst *types.ContainerInstance) (string, int, string, bool) {
	if inst == nil {
		return "", 0, "", false
	}

	exitCode := 0
	if inst.ExitCode != nil {
		exitCode = *inst.ExitCode
	}

	switch strings.TrimSpace(strings.ToLower(string(inst.Status))) {
	case string(types.ContainerFailed):
		if exitCode == 0 {
			exitCode = 1
		}
		return StatusFailed, exitCode, fmt.Sprintf("container execution failed (exit_code=%d)", exitCode), true
	case string(types.ContainerStopped), string(types.ContainerExited):
		if node != nil && strings.EqualFold(strings.TrimSpace(node.Status), StatusStopping) {
			return StatusStopped, 0, "node stopped by user", true
		}
		if exitCode == 0 {
			return StatusDone, 0, "", true
		}
		return StatusFailed, exitCode, fmt.Sprintf("container exited with non-zero code (%d)", exitCode), true
	default:
		return "", 0, "", false
	}
}

func (c *NodeCompletionCoordinator) isContainerTerminal(status types.ContainerStatus) bool {
	switch strings.TrimSpace(strings.ToLower(string(status))) {
	case string(types.ContainerStopped), string(types.ContainerFailed), string(types.ContainerExited):
		return true
	default:
		return false
	}
}

// func (c *NodeCompletionCoordinator) runCleanup(ctx context.Context, node *types.AnalysisNode) {
// 	if c.cleanup == nil || node == nil {
// 		return
// 	}
// 	c.cleanup(ctx, node)
// }

func (c *NodeCompletionCoordinator) publishNodeResult(node *types.AnalysisNode, status string, exitCode int, errorMessage string) {
	if node == nil {
		return
	}
	eventName := EventNodeCompleted
	if strings.EqualFold(status, StatusFailed) {
		eventName = EventNodeFailed
	}
	payload := map[string]any{
		"status":    status,
		"exit_code": exitCode,
	}
	if strings.TrimSpace(errorMessage) != "" {
		payload["error"] = errorMessage
	}
	if c.bus != nil {
		c.bus.Publish(RuntimeEvent{
			Name:           eventName,
			AnalysisID:     node.AnalysisID,
			AnalysisNodeID: node.ID,
			NodeID:         node.NodeID,
			OccurredAt:     time.Now().UTC(),
			Payload:        payload,
		})
	}
}

func (c *NodeCompletionCoordinator) cleanupOrphanContainer(ctx context.Context, inst *types.ContainerInstance, source string, reason string) {
	if inst == nil {
		return
	}
	if c.containerOps == nil {
		logger.Warnf(ctx, "[NodeCompletionCoordinator] orphan container detected but container cleanup is disabled, source=%s instance_id=%d runtime_id=%s owner_id=%d reason=%s", source, inst.ID, inst.RuntimeID, inst.OwnerID, reason)
		return
	}
	if err := c.containerOps.Delete(ctx, inst.ID); err != nil {
		logger.Warnf(ctx, "[NodeCompletionCoordinator] cleanup orphan container failed, source=%s instance_id=%d runtime_id=%s owner_id=%d reason=%s err=%v", source, inst.ID, inst.RuntimeID, inst.OwnerID, reason, err)
		return
	}
	logger.Infof(ctx, "[NodeCompletionCoordinator] cleaned orphan container, source=%s instance_id=%d runtime_id=%s owner_id=%d reason=%s", source, inst.ID, inst.RuntimeID, inst.OwnerID, reason)
}
