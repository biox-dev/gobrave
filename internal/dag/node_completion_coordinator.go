package dag

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gobravedev/gobrave/internal/config"
	"github.com/gobravedev/gobrave/internal/event"
	"github.com/gobravedev/gobrave/internal/logger"
	"github.com/gobravedev/gobrave/internal/types"
	"github.com/gobravedev/gobrave/internal/types/interfaces"
	"gorm.io/gorm"
)

var _ event.Handler = (*NodeCompletionCoordinator)(nil)

type NodeCompletionCoordinator struct {
	analysisRepo    interfaces.AnalysisRepository
	containerRepo   interfaces.ContainerRepository
	containerOps    NodeContainerOperator
	outputResolver  nodeOutputResolver
	runtime         *RuntimeEngine
	bus             event.Bus
	cleanup         NodeFailureCleanupFunc
	deleteOnSuccess bool
	pollInterval    time.Duration
	pollBatchLimit  int
	reconcileMu     sync.Mutex
	inFlight        map[int64]struct{}
}

type nodeOutputResolver interface {
	Resolve(node *types.AnalysisNode, candidateOutputs map[string]any) (map[string]any, []string)
}

type fileSystemNodeOutputResolver struct{}

func newFileSystemNodeOutputResolver() nodeOutputResolver {
	return &fileSystemNodeOutputResolver{}
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
	cleanup := buildNodeFailureCleanup(containerRepo, containerOps, cfg)

	if cfg != nil && cfg.Container != nil {
		deleteOnSuccess = cfg.Container.DeleteContainerOnNodeSuccess
	}
	if pollInterval <= 0 {
		pollInterval = 2 * time.Second
	}
	return &NodeCompletionCoordinator{
		analysisRepo:    analysisRepo,
		containerRepo:   containerRepo,
		containerOps:    containerOps,
		outputResolver:  newFileSystemNodeOutputResolver(),
		runtime:         runtime,
		bus:             bus,
		cleanup:         cleanup,
		deleteOnSuccess: deleteOnSuccess,
		pollInterval:    pollInterval,
		pollBatchLimit:  0,
		inFlight:        make(map[int64]struct{}),
	}
}

func buildNodeFailureCleanup(
	containerRepo interfaces.ContainerRepository,
	containerOps NodeContainerOperator,
	cfg *config.Config,
) NodeFailureCleanupFunc {
	if containerRepo == nil || containerOps == nil {
		return nil
	}
	if cfg == nil || cfg.Container == nil {
		return nil
	}
	if !strings.EqualFold(strings.TrimSpace(cfg.Container.DagNodeCleanupOnFailed), "delete") {
		return nil
	}

	return func(ctx context.Context, node *types.AnalysisNode) {
		if node == nil || node.ID == 0 {
			return
		}
		instances, err := containerRepo.ListContainerInstanceByOwnerTypeAndOwnerIDs(ctx, types.ContainerOwnerDagNode, []int64{int64(node.ID)})
		if err != nil {
			logger.Warnf(ctx, "[NodeCompletionCoordinator] list container instances for failed node cleanup failed, node_id=%s err=%v", node.NodeID, err)
			return
		}
		for _, inst := range instances {
			if inst == nil || inst.OwnerType != types.ContainerOwnerDagNode || inst.OwnerID != int64(node.ID) {
				continue
			}
			if err := containerOps.Delete(ctx, inst.ID); err != nil {
				logger.Warnf(ctx, "[NodeCompletionCoordinator] failed node cleanup delete failed, instance_id=%d runtime_id=%s err=%v", inst.ID, inst.RuntimeID, err)
			}
		}
	}
}

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

	outputs, outputErrors := c.buildResolvedOutputs(node, inst)
	if finalStatus == StatusDone && len(outputErrors) > 0 {
		finalStatus = StatusFailed
		if exitCode == 0 {
			exitCode = 1
		}
		errorMessage = fmt.Sprintf("output validation failed: %s", strings.Join(outputErrors, "; "))
	}
	if _, err := c.runtime.CompleteNode(ctx, node.ID, finalStatus, outputs, exitCode, errorMessage); err != nil {
		latest, latestErr := c.analysisRepo.GetAnalysisNodeByAnalysisNodeID(ctx, node.AnalysisNodeID)
		if latestErr == nil && latest != nil && IsTerminalStatus(latest.Status) {
			return
		}
		logger.Warnf(ctx, "[NodeCompletionCoordinator] complete node failed, source=%s analysis_id=%d node_id=%s status=%s err=%v", source, node.AnalysisID, node.NodeID, finalStatus, err)
		return
	}
	// TODO 目前容器失败直接删除, 后续需要可配置, 方便查看失败日志
	if finalStatus == StatusFailed {
		c.runCleanup(ctx, node)
	} else if finalStatus == StatusDone {
		c.cleanupSuccessfulContainer(ctx, inst, source)
	}
	c.publishNodeResult(node, finalStatus, exitCode, errorMessage)
}

func (c *NodeCompletionCoordinator) cleanupSuccessfulContainer(ctx context.Context, inst *types.ContainerInstance, source string) {
	if !c.deleteOnSuccess || inst == nil || c.containerOps == nil {
		return
	}
	if err := c.containerOps.Delete(ctx, inst.ID); err != nil {
		logger.Warnf(ctx, "[NodeCompletionCoordinator] cleanup successful container failed, source=%s instance_id=%d runtime_id=%s owner_id=%d err=%v", source, inst.ID, inst.RuntimeID, inst.OwnerID, err)
		return
	}
	logger.Infof(ctx, "[NodeCompletionCoordinator] cleaned successful container, source=%s instance_id=%d runtime_id=%s owner_id=%d", source, inst.ID, inst.RuntimeID, inst.OwnerID)
}

func (c *NodeCompletionCoordinator) buildResolvedOutputs(node *types.AnalysisNode, inst *types.ContainerInstance) (map[string]any, []string) {
	outputs := defaultContainerOutputs(inst)
	if node == nil {
		return outputs, nil
	}

	resolver := c.outputResolver
	if resolver == nil {
		resolver = newFileSystemNodeOutputResolver()
	}
	// 传入空函数
	resolved, errs := resolver.Resolve(node, map[string]any{})
	return resolved, errs
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

func (r *fileSystemNodeOutputResolver) Resolve(node *types.AnalysisNode, candidateOutputs map[string]any) (map[string]any, []string) {
	verified := map[string]any{}
	errorsList := make([]string, 0)
	if node == nil {
		return cloneMap(candidateOutputs), errorsList
	}

	outputPatterns := map[string]any(node.OutputPatterns)
	if len(outputPatterns) == 0 {
		return cloneMap(candidateOutputs), errorsList
	}

	outputsPayload := loadOutputsJSON(node.OutputDir)
	outputDir, hasOutputDir := resolveNodeOutputDir(node)

	for handle, rawCfg := range outputPatterns {
		cfg, isConfigMap := rawCfg.(map[string]any)
		if !isConfigMap {
			if value, ok := candidateOutputs[handle]; ok {
				verified[handle] = value
			}
			continue
		}

		outType := strings.ToLower(strings.TrimSpace(asString(cfg["type"])))
		pattern := strings.TrimSpace(asString(cfg["pattern"]))
		multiple := asBool(cfg["multiple"])
		required := asBoolWithDefault(cfg["required"], true)

		if outType != "file" {
			if value, ok := candidateOutputs[handle]; ok {
				verified[handle] = value
				continue
			}
			if named, ok := resolveOutputValueByName(outputsPayload, handle, multiple); ok {
				verified[handle] = named
			}
			continue
		}

		if pattern == "" {
			if named, ok := resolveOutputValueByName(outputsPayload, handle, multiple); ok {
				verified[handle] = named
			} else if value, ok := candidateOutputs[handle]; ok {
				verified[handle] = value
			} else if required {
				errorsList = append(errorsList, fmt.Sprintf("missing output pattern or named output: %s", handle))
			}
			continue
		}

		renderedPattern := renderPattern(pattern, node)
		if !hasOutputDir {
			errorsList = append(errorsList, fmt.Sprintf("missing output_dir for handle: %s", handle))
			continue
		}

		stat, err := os.Stat(outputDir)
		if err != nil || !stat.IsDir() {
			errorsList = append(errorsList, fmt.Sprintf("output_dir not found: %s", outputDir))
			continue
		}

		matches, err := globFiles(outputDir, renderedPattern)
		if err != nil {
			if required {
				errorsList = append(errorsList, fmt.Sprintf("invalid output pattern: %s pattern=%s", handle, renderedPattern))
			}
			continue
		}
		if len(matches) == 0 {
			if required {
				errorsList = append(errorsList, fmt.Sprintf("missing output file: %s pattern=%s", handle, renderedPattern))
			}
			continue
		}

		if multiple {
			arr := make([]any, 0, len(matches))
			for _, match := range matches {
				arr = append(arr, match)
			}
			verified[handle] = arr
		} else {
			verified[handle] = matches[0]
		}
	}

	for handle, value := range candidateOutputs {
		if _, exists := verified[handle]; exists {
			continue
		}
		if _, declared := outputPatterns[handle]; declared {
			continue
		}
		verified[handle] = value
	}

	return verified, errorsList
}

func renderPattern(pattern string, node *types.AnalysisNode) string {
	if node == nil {
		return pattern
	}
	sampleToken := strings.TrimSpace(node.SampleID)
	if sampleToken == "" {
		sampleToken = strings.TrimSpace(node.NodeID)
	}
	if sampleToken == "" {
		sampleToken = "sample"
	}
	return strings.ReplaceAll(pattern, "{sample}", sampleToken)
}

func resolveNodeOutputDir(node *types.AnalysisNode) (string, bool) {
	if node == nil {
		return "", false
	}
	if outputDir := strings.TrimSpace(node.OutputDir); outputDir != "" {
		return outputDir, true
	}
	if workspaceDir := strings.TrimSpace(node.WorkspaceDir); workspaceDir != "" {
		return filepath.Join(workspaceDir, "output"), true
	}
	return "", false
}

func globFiles(outputDir string, pattern string) ([]string, error) {
	searchPattern := pattern
	if !filepath.IsAbs(pattern) {
		searchPattern = filepath.Join(outputDir, pattern)
	}

	matches, err := filepath.Glob(searchPattern)
	if err != nil {
		return nil, err
	}

	set := map[string]struct{}{}
	resolved := make([]string, 0, len(matches))
	for _, item := range matches {
		stat, statErr := os.Stat(item)
		if statErr != nil || !stat.Mode().IsRegular() {
			continue
		}
		absPath, absErr := filepath.Abs(item)
		if absErr != nil {
			continue
		}
		if _, exists := set[absPath]; exists {
			continue
		}
		set[absPath] = struct{}{}
		resolved = append(resolved, absPath)
	}
	sort.Strings(resolved)
	return resolved, nil
}

func loadOutputsJSON(outputDir string) map[string]any {
	cleanDir := strings.TrimSpace(outputDir)
	if cleanDir == "" {
		return map[string]any{}
	}

	path := filepath.Join(cleanDir, "outputs.json")
	buf, err := os.ReadFile(path)
	if err != nil {
		return map[string]any{}
	}

	payload := map[string]any{}
	if err := json.Unmarshal(buf, &payload); err != nil {
		return map[string]any{}
	}
	return payload
}

func resolveOutputValueByName(outputsPayload map[string]any, handle string, multiple bool) (any, bool) {
	value, ok := outputsPayload[handle]
	if !ok {
		return nil, false
	}
	if !multiple {
		return value, true
	}
	if arr, ok := value.([]any); ok {
		return arr, true
	}
	if arr, ok := value.([]interface{}); ok {
		return arr, true
	}
	return []any{value}, true
}

func asString(v any) string {
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return s
}

func asBoolWithDefault(v any, defaultValue bool) bool {
	if v == nil {
		return defaultValue
	}
	b, ok := v.(bool)
	if ok {
		return b
	}
	s, ok := v.(string)
	if !ok {
		return defaultValue
	}
	normalized := strings.TrimSpace(strings.ToLower(s))
	if normalized == "true" {
		return true
	}
	if normalized == "false" {
		return false
	}
	return defaultValue
}

func cloneMap(src map[string]any) map[string]any {
	if len(src) == 0 {
		return map[string]any{}
	}
	cloned := make(map[string]any, len(src))
	for k, v := range src {
		cloned[k] = v
	}
	return cloned
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

func (c *NodeCompletionCoordinator) runCleanup(ctx context.Context, node *types.AnalysisNode) {
	if c.cleanup == nil || node == nil {
		return
	}
	c.cleanup(ctx, node)
}

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
