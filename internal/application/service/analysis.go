package service

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gobravedev/gobrave/internal/config"
	dagruntime "github.com/gobravedev/gobrave/internal/dag"
	"github.com/gobravedev/gobrave/internal/logger"
	"github.com/gobravedev/gobrave/internal/types"
	"github.com/gobravedev/gobrave/internal/types/interfaces"
	"github.com/gobravedev/gobrave/internal/utils"
	"github.com/google/uuid"
)

type analysisService struct {
	analysisRepo     interfaces.AnalysisRepository
	workflowRepo     interfaces.WorkflowRepository
	projectRepo      interfaces.ProjectRepository
	containerService interfaces.ContainerService
	cfg              *config.Config
}

func NewAnalysisService(
	analysisRepo interfaces.AnalysisRepository,
	workflowRepo interfaces.WorkflowRepository,
	projectRepo interfaces.ProjectRepository,
	containerService interfaces.ContainerService,
	cfg *config.Config,
) interfaces.AnalysisService {
	return &analysisService{
		analysisRepo:     analysisRepo,
		workflowRepo:     workflowRepo,
		projectRepo:      projectRepo,
		containerService: containerService,
		cfg:              cfg,
	}
}

func (s *analysisService) GetAnalysisByAnalysisID(ctx context.Context, analysisID string) (*types.Analysis, error) {
	return s.analysisRepo.GetAnalysisByAnalysisID(ctx, analysisID)
}

func (s *analysisService) PageAnalysisByProjectID(ctx context.Context, pagination *types.Pagination, projectID int64, query *types.AnalysisQuey) ([]*types.Analysis, int64, error) {
	if pagination == nil {
		pagination = &types.Pagination{}
	}

	return s.analysisRepo.PageAnalysisByProjectID(ctx, pagination, projectID, query)
}

func (s *analysisService) GetAnalysisByID(ctx context.Context, analysisID int64) (*types.Analysis, error) {
	return s.analysisRepo.GetAnalysisByID(ctx, analysisID)
}

func (s *analysisService) GetAnalysisNodeByID(ctx context.Context, id int64) (*types.AnalysisNode, error) {
	return s.analysisRepo.GetAnalysisNodeByID(ctx, id)
}
func (s *analysisService) UpdateAnalysisByID(ctx context.Context, analysisID int64, values map[string]any) error {
	return s.analysisRepo.UpdateAnalysisByID(ctx, analysisID, values)
}

func (s *analysisService) UpdateAnalysisNodeByID(ctx context.Context, analysisNodeID int64, values map[string]any) error {
	return s.analysisRepo.UpdateAnalysisNodeByID(ctx, analysisNodeID, values)
}

func (s *analysisService) GetAnalysisNodeByAnalysisNodeID(ctx context.Context, analysisNodeID string) (*types.AnalysisNode, error) {
	return s.analysisRepo.GetAnalysisNodeByAnalysisNodeID(ctx, analysisNodeID)
}

func (s *analysisService) ListAnalysisNodesByAnalysisID(ctx context.Context, analysisID int64) ([]*types.AnalysisNode, error) {
	return s.analysisRepo.ListAnalysisNodesByAnalysisID(ctx, analysisID)
}

func (s *analysisService) ListAnalysisNodesByProjectIDAndScriptID(ctx context.Context, projectID, scriptID int64) ([]*types.AnalysisNode, error) {
	return s.analysisRepo.ListAnalysisNodesByProjectIDAndScriptID(ctx, projectID, scriptID)
}

func (s *analysisService) SaveAnalysisController(ctx context.Context, input *types.AnalysisControllerSaveInput) (*types.Analysis, error) {
	if input == nil || input.RequestParam == nil {
		return nil, fmt.Errorf("request_param is required")
	}
	analysisType := strings.TrimSpace(toString(input.RequestParam["analysis_type"]))
	if analysisType == "" {
		return nil, fmt.Errorf("request_param.analysis_type is required")
	}
	workflowID := strings.TrimSpace(toString(input.RequestParam["relation_id"]))
	if workflowID == "" {
		return nil, fmt.Errorf("request_param.relation_id is required")
	}
	workflow, err := s.workflowRepo.GetWorkflowByWorkflowID(ctx, workflowID)
	if err != nil {
		return nil, fmt.Errorf("failed to get workflow by workflow_id: %v", err)
	}
	if workflow == nil {
		return nil, fmt.Errorf("workflow not found for workflow_id: %s", workflowID)
	}
	// var workflowID string
	// switch analysisType {
	// case "workflow":
	// 	workflowID = strings.TrimSpace(toString(input.RequestParam["relation_id"]))
	// 	if workflowID == "" {
	// 		return nil, fmt.Errorf("request_param.relation_id is required")
	// 	}
	// case "script":
	// 	workflowID = strings.TrimSpace(toString(input.RequestParam["script_id"]))
	// 	if workflowID == "" {
	// 		return nil, fmt.Errorf("request_param.script_id is required")
	// 	}
	// default:
	// 	return nil, fmt.Errorf("request_param.relation_id or script_id is required")
	// }

	// projectID := strings.TrimSpace(toString(input.RequestParam["project"]))
	if input.Project == nil {
		return nil, fmt.Errorf("request_param.project is required")
	}

	analysisName := strings.TrimSpace(toString(input.RequestParam["analysis_name"]))
	if analysisName == "" {
		return nil, fmt.Errorf("request_param.analysis_name is required")
	}

	parseResult := input.ParseAnalysisResult
	if parseResult == nil {
		parseResult = map[string]any{}
	}
	analysisIDStr := strings.TrimSpace(toString(input.RequestParam["analysis_id"]))
	// analysisIDStr 转 int64
	analysisID, err := strconv.ParseInt(analysisIDStr, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid analysis_id: %v", err)
	}
	// analysisID := 0
	// sessionID, err := strconv.ParseInt(sessionIDParam, 0, 64)

	cacheType := intFromAny(input.RequestParam["cache_type"], types.CacheTypeRerunAll)
	if _, ok := input.RequestParam["cache_type"]; !ok {
		if toBool(input.RequestParam["is_cache"]) {
			cacheType = types.CacheTypeReuseExistingNode
		}
	}
	if cacheType < types.CacheTypeRerunAll || cacheType > types.CacheTypeReuseWhenScriptAndParamsUnchanged {
		cacheType = types.CacheTypeRerunAll
	}
	input.RequestParam["cache_type"] = cacheType
	dataComponentIDs := toString(input.RequestParam["data_component_ids"])

	var saved *types.Analysis
	saveErr := s.analysisRepo.WithTransaction(ctx, func(tx interfaces.AnalysisRepository) error {
		var existing *types.Analysis
		if analysisID != 0 {
			item, _ := tx.GetAnalysisByID(ctx, analysisID)
			// if err != nil && !stderrs.Is(err, gorm.ErrRecordNotFound) {
			// 	return err
			// }

			if item != nil {
				existing = item
			}
		}

		baseDir := s.resolveStorageBaseDir()
		if existing == nil && analysisID == 0 {
			analysisID = utils.GenerateID()
		}

		analsyisDir := utils.GetAnalysisDir(baseDir, input.Project.ProjectID, workflow.ID)

		outputDir := filepath.Join(analsyisDir, fmt.Sprintf("%d", analysisID))
		workDir := filepath.Join(analsyisDir, fmt.Sprintf("%d", analysisID))
		if existing != nil && cacheType != types.CacheTypeRerunAll {
			analysisID = existing.ID
			if strings.TrimSpace(existing.OutputDir) != "" {
				outputDir = existing.OutputDir
			}
			if strings.TrimSpace(existing.WorkDir) != "" {
				workDir = existing.WorkDir
			}
		}

		if err := os.MkdirAll(outputDir, 0o755); err != nil {
			return err
		}
		if err := os.MkdirAll(workDir, 0o755); err != nil {
			return err
		}

		paramsPath := filepath.Join(outputDir, "params.json")
		commandPath := filepath.Join(outputDir, "run.sh")
		commandLogPath := filepath.Join(outputDir, "run.log")
		traceFile := filepath.Join(outputDir, "trace.log")
		workflowLogFile := filepath.Join(outputDir, "workflow.log")
		executorLogFile := filepath.Join(outputDir, ".nextflow.log")

		parseResult["tools_output_dir"] = outputDir
		input.RequestParam["analysis_id"] = fmt.Sprintf("%d", analysisID)

		if err := writeJSONFile(paramsPath, parseResult); err != nil {
			return err
		}

		requestParamJSON, err := json.Marshal(input.RequestParam)
		if err != nil {
			return err
		}

		if existing != nil {
			updateValues := map[string]any{
				"project":       input.Project.ID,
				"analysis_name": analysisName,
				"request_param": string(requestParamJSON),
				"cache_type":    cacheType,
				"relation_id":   workflowID,
				// "analysis_type": analysisType,
				// "is_report":          input.IsReport,
				"data_component_ids": dataComponentIDs,
				"output_dir":         outputDir,
				"work_dir":           workDir,
				"params_path":        paramsPath,
				"command_path":       commandPath,
				"command_log_path":   commandLogPath,
				"updated_at":         time.Now().UTC(),
			}
			if strings.TrimSpace(existing.JobStatus) != "running" {
				updateValues["job_status"] = "updated"
			}
			if err := tx.UpdateAnalysisByID(ctx, analysisID, updateValues); err != nil {
				return err
			}
		} else {
			newAnalysis := &types.Analysis{
				ID:         analysisID,
				ProjectID:  input.Project.ID,
				AnalysisID: uuid.NewString(),
				WorkflowID: workflowID,
				// AnalysisType:    analysisType,
				AnalysisName:    analysisName,
				WorkDir:         workDir,
				ParamsPath:      paramsPath,
				CommandPath:     commandPath,
				RequestParam:    string(requestParamJSON),
				OutputDir:       outputDir,
				TraceFile:       traceFile,
				WorkflowLogFile: workflowLogFile,
				ExecutorLogFile: executorLogFile,
				JobStatus:       "created",
				ServerStatus:    "",
				CommandLogPath:  commandLogPath,
				// IsReport:         input.IsReport,
				CacheType:        cacheType,
				DataComponentIDs: dataComponentIDs,
				Used:             true,
			}
			if err := tx.CreateAnalysis(ctx, newAnalysis); err != nil {
				return err
			}
		}

		savedAnalysis, err := tx.GetAnalysisByID(ctx, analysisID)
		if err != nil {
			return err
		}

		if input.DagRuntime != nil {
			if err := s.persistDagRuntime(ctx, tx, savedAnalysis, input.DagRuntime); err != nil {
				return err
			}
		}

		saved = savedAnalysis
		return nil
	})
	if saveErr != nil {
		return nil, saveErr
	}

	return saved, nil
}

func (s *analysisService) DeleteAnalysisNode(ctx context.Context, id int64) error {
	node, err := s.analysisRepo.GetAnalysisNodeByID(ctx, id)
	if err != nil {
		logger.Warnf(ctx, "[NodeOrchestrator] failed to get analysis node by id=%d, err=%v", id, err)
		return err
	}
	project, err := s.projectRepo.GetProjectByID(ctx, node.ProjectID)
	if err != nil {
		logger.Warnf(ctx, "[NodeOrchestrator] failed to get project by id=%d, err=%v", node.ProjectID, err)
		return err
	}
	prefix := filepath.Join(s.cfg.Storage.BaseDir, "data", project.ProjectID)
	//判断是否以prefix开头 如果不是就不删除
	if !strings.HasPrefix(node.WorkspaceDir, prefix) {
		logger.Warnf(ctx, "[NodeOrchestrator] workspace dir=%s is not under project data dir=%s, skip delete", node.WorkspaceDir, prefix)
		return fmt.Errorf("workspace dir is not under project data dir")
	}
	// 	删除工作目录
	if err := os.RemoveAll(node.WorkspaceDir); err != nil {
		logger.Warnf(ctx, "[NodeOrchestrator] failed to remove workspace dir=%s, err=%v", node.WorkspaceDir, err)
		return err
	}
	if id <= 0 {
		return fmt.Errorf("invalid analysis node id: %d", id)
	}
	return s.analysisRepo.DeleteAnalysisNodeByID(ctx, id)
}

func (s *analysisService) DeleteAnalysis(ctx context.Context, id int64) error {
	if id <= 0 {
		return fmt.Errorf("invalid analysis id: %d", id)
	}

	analysis, err := s.analysisRepo.GetAnalysisByID(ctx, id)
	if err != nil {
		logger.Warnf(ctx, "[AnalysisService] failed to get analysis by id=%d, err=%v", id, err)
		return err
	}

	project, err := s.projectRepo.GetProjectByID(ctx, analysis.ProjectID)
	if err != nil {
		logger.Warnf(ctx, "[AnalysisService] failed to get project by id=%d, err=%v", analysis.ProjectID, err)
		return err
	}
	projectDataDir := filepath.Join(s.cfg.Storage.BaseDir, "data", project.ProjectID)

	// 1. List all analysis nodes for this analysis
	nodes, err := s.analysisRepo.ListAnalysisNodesByAnalysisID(ctx, id)
	if err != nil {
		logger.Warnf(ctx, "[AnalysisService] failed to list analysis nodes for analysis id=%d, err=%v", id, err)
		return err
	}

	// 2. Collect node IDs for container cleanup
	nodeIDs := make([]int64, 0, len(nodes))
	for _, node := range nodes {
		if node == nil {
			continue
		}
		nodeIDs = append(nodeIDs, node.ID)

		// 3. Delete each node's workspace directory
		if strings.TrimSpace(node.WorkspaceDir) != "" {
			if strings.HasPrefix(node.WorkspaceDir, projectDataDir) {
				if err := os.RemoveAll(node.WorkspaceDir); err != nil {
					logger.Warnf(ctx, "[AnalysisService] failed to remove node workspace dir=%s, err=%v", node.WorkspaceDir, err)
				}
			} else {
				logger.Warnf(ctx, "[AnalysisService] node workspace dir=%s is not under project data dir=%s, skip delete", node.WorkspaceDir, projectDataDir)
			}
		}
	}

	// 4. Delete container instances associated with the analysis nodes
	if len(nodeIDs) > 0 {
		if err := s.containerService.DeleteContainerInstancesByOwnerTypeAndOwnerIDs(
			ctx,
			types.ContainerOwnerDagNode,
			nodeIDs,
		); err != nil {
			logger.Warnf(ctx, "[AnalysisService] failed to delete container instances for nodes, err=%v", err)
			// non-fatal, continue with deletion
		}
	}

	// 5. Delete analysis edges
	if err := s.analysisRepo.DeleteAnalysisEdgesByAnalysisID(ctx, id); err != nil {
		logger.Warnf(ctx, "[AnalysisService] failed to delete analysis edges for analysis id=%d, err=%v", id, err)
		return err
	}

	// 6. Delete analysis nodes
	if err := s.analysisRepo.DeleteAnalysisNodesByAnalysisID(ctx, id); err != nil {
		logger.Warnf(ctx, "[AnalysisService] failed to delete analysis nodes for analysis id=%d, err=%v", id, err)
		return err
	}

	// 7. Delete the analysis workspace/WorkDir
	if strings.TrimSpace(analysis.WorkDir) != "" {
		if strings.HasPrefix(analysis.WorkDir, projectDataDir) {
			if err := os.RemoveAll(analysis.WorkDir); err != nil {
				logger.Warnf(ctx, "[AnalysisService] failed to remove analysis work dir=%s, err=%v", analysis.WorkDir, err)
			}
		} else {
			logger.Warnf(ctx, "[AnalysisService] analysis work dir=%s is not under project data dir=%s, skip delete", analysis.WorkDir, projectDataDir)
		}
	}
	// Also delete OutputDir if different from WorkDir
	if strings.TrimSpace(analysis.OutputDir) != "" && analysis.OutputDir != analysis.WorkDir {
		if strings.HasPrefix(analysis.OutputDir, projectDataDir) {
			if err := os.RemoveAll(analysis.OutputDir); err != nil {
				logger.Warnf(ctx, "[AnalysisService] failed to remove analysis output dir=%s, err=%v", analysis.OutputDir, err)
			}
		}
	}

	// 8. Delete the analysis record
	return s.analysisRepo.DeleteAnalysisByID(ctx, id)
}

func (s *analysisService) persistDagRuntime(ctx context.Context, repo interfaces.AnalysisRepository, analysis *types.Analysis, dagRuntime map[string]any) error {
	nodeRows := toMapSlice(dagRuntime["analysis_nodes"])
	edgeRows := toMapSlice(dagRuntime["analysis_edges"])
	useCache := analysis != nil && analysis.CacheType != types.CacheTypeRerunAll

	existingNodes, err := repo.ListAnalysisNodesByAnalysisID(ctx, analysis.ID)
	if err != nil {
		return err
	}
	existingByNodeID := map[string]*types.AnalysisNode{}
	for _, item := range existingNodes {
		existingByNodeID[item.NodeID] = item
	}

	nodes := make([]*types.AnalysisNode, 0, len(nodeRows))
	for _, row := range nodeRows {
		nodeID := strings.TrimSpace(toString(row["node_id"]))
		if nodeID == "" {
			continue
		}
		// 存在的 node 保持原有的 analysis_node_id、workspace_dir、output_dir、params_path、command_path、log_path 等字段，避免重复创建和覆盖
		existing := existingByNodeID[nodeID]
		analysisNodeID := strings.TrimSpace(toString(row["analysis_node_id"]))
		id := utils.GenerateID()
		if existing != nil && strings.TrimSpace(existing.AnalysisNodeID) != "" {
			analysisNodeID = existing.AnalysisNodeID
		}
		if analysisNodeID == "" {
			analysisNodeID = "node-" + uuid.NewString()
		}

		workspaceDir := filepath.Join(analysis.OutputDir, fmt.Sprintf("%d", id))
		outputDir := filepath.Join(workspaceDir, "output")
		paramsPath := filepath.Join(workspaceDir, "params.json")
		commandPath := filepath.Join(workspaceDir, "run.sh")
		logPath := filepath.Join(workspaceDir, "command.log")

		if existing != nil {
			if strings.TrimSpace(existing.WorkspaceDir) != "" {
				workspaceDir = existing.WorkspaceDir
			}
			if strings.TrimSpace(existing.OutputDir) != "" {
				outputDir = existing.OutputDir
			}
			if strings.TrimSpace(existing.ParamsPath) != "" {
				paramsPath = existing.ParamsPath
			}
			if strings.TrimSpace(existing.CommandPath) != "" {
				commandPath = existing.CommandPath
			}
			if strings.TrimSpace(existing.LogPath) != "" {
				logPath = existing.LogPath
			}
			if existing.ID != 0 {
				id = existing.ID
			}
		}

		if err := os.MkdirAll(outputDir, 0o755); err != nil {
			return err
		}

		status := fallbackString(toString(row["status"]), dagruntime.StatusPending)
		cacheHit := toBool(row["cache_hit"])

		if useCache {
			if existing != nil && shouldKeepNodeStatusForCache(existing.Status) {
				status = strings.TrimSpace(existing.Status)
				cacheHit = true
			} else {
				status = dagruntime.StatusPending
				cacheHit = false
			}
		} else {
			status = dagruntime.StatusPending
			cacheHit = false
		}
		scriptID := toString(row["script_id"])

		script, err := s.workflowRepo.GetScriptByScriptID(ctx, analysis.ProjectID, scriptID)
		if err != nil {
			return err
		}
		node := &types.AnalysisNode{
			ID:                     id,
			AnalysisNodeID:         analysisNodeID,
			AnalysisID:             analysis.ID,
			NodeID:                 nodeID,
			NodeName:               toString(row["node_name"]),
			SampleID:               toString(row["sample_id"]),
			ScriptID:               script.ID,
			InputsPatterns:         toJSONMap(row["inputs_patterns"]),
			ResolvedInputs:         toJSONMap(row["resolved_inputs"]),
			OutputPatterns:         toJSONMap(row["output_patterns"]),
			ResolvedOutputs:        toJSONMap(row["resolved_outputs"]),
			Params:                 toJSONMap(row["params"]),
			Status:                 status,
			Executor:               toString(row["executor"]),
			Retry:                  intFromAny(row["retry"], 0),
			MaxRetry:               intFromAny(row["max_retry"], 3),
			CacheHit:               cacheHit,
			UpstreamIDs:            toJSONSlice(row["upstream_ids"]),
			DownstreamIDs:          toJSONSlice(row["downstream_ids"]),
			InputValidationErrors:  toJSONSlice(row["input_validation_errors"]),
			OutputValidationErrors: toJSONSlice(row["output_validation_errors"]),
			LogPath:                logPath,
			WorkspaceDir:           workspaceDir,
			OutputDir:              outputDir,
			CommandPath:            commandPath,
			ParamsPath:             paramsPath,
			CreationSource:         "scheduler",
		}
		if strings.TrimSpace(node.Status) == "" {
			node.Status = dagruntime.StatusPending
		}
		nodes = append(nodes, node)
	}

	edges := make([]*types.AnalysisEdge, 0, len(edgeRows))
	for _, row := range edgeRows {
		edge := &types.AnalysisEdge{
			AnalysisEdgeID: fallbackString(toString(row["analysis_edge_id"]), uuid.NewString()),
			AnalysisID:     analysis.ID,
			SourceNode:     toString(row["source_node"]),
			TargetNode:     toString(row["target_node"]),
			SourceHandle:   toString(row["source_handle"]),
			TargetHandle:   toString(row["target_handle"]),
		}
		edges = append(edges, edge)
	}

	if err := repo.DeleteAnalysisNodesByAnalysisID(ctx, analysis.ID); err != nil {
		return err
	}
	if err := repo.CreateAnalysisNodes(ctx, nodes); err != nil {
		return err
	}
	if err := repo.DeleteAnalysisEdgesByAnalysisID(ctx, analysis.ID); err != nil {
		return err
	}
	if err := repo.CreateAnalysisEdges(ctx, edges); err != nil {
		return err
	}

	return nil
}

func (s *analysisService) ListAnalysisByWorkflowID(ctx context.Context, workflowID string) ([]*types.Analysis, error) {
	return s.analysisRepo.ListAnalysisByWorkflowID(ctx, workflowID)
}
func (s *analysisService) resolveStorageBaseDir() string {
	if s.cfg != nil && s.cfg.Storage != nil {
		if base := strings.TrimSpace(s.cfg.Storage.BaseDir); base != "" {
			return base
		}
	}
	return "."
}

func writeJSONFile(path string, value any) error {
	content, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, content, 0o644)
}

func toMapSlice(value any) []map[string]any {
	if value == nil {
		return []map[string]any{}
	}
	if rows, ok := value.([]map[string]any); ok {
		return rows
	}
	raw, ok := value.([]any)
	if !ok {
		return []map[string]any{}
	}
	rows := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		if m, ok := item.(map[string]any); ok {
			rows = append(rows, m)
		}
	}
	return rows
}

func toString(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

func fallbackString(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func toBool(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return strings.EqualFold(strings.TrimSpace(t), "true") || strings.TrimSpace(t) == "1"
	case int:
		return t != 0
	case int64:
		return t != 0
	case float64:
		return t != 0
	default:
		return false
	}
}

func intFromAny(v any, fallback int) int {
	switch t := v.(type) {
	case int:
		return t
	case int64:
		return int(t)
	case float64:
		return int(t)
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(t))
		if err == nil {
			return n
		}
	}
	return fallback
}

func toJSONMap(value any) types.JSONMap {
	if value == nil {
		return types.JSONMap{}
	}
	switch v := value.(type) {
	case types.JSONMap:
		return v
	case map[string]any:
		return types.JSONMap(v)
	case string:
		return parseJSONMap([]byte(v))
	case []byte:
		return parseJSONMap(v)
	default:
		buf, err := json.Marshal(v)
		if err != nil {
			return types.JSONMap{}
		}
		return parseJSONMap(buf)
	}
}

func parseJSONMap(raw []byte) types.JSONMap {
	out := types.JSONMap{}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return out
	}
	if err := json.Unmarshal([]byte(trimmed), &out); err != nil {
		return types.JSONMap{}
	}
	return out
}

func toJSONSlice(value any) types.JSONSlice {
	if value == nil {
		return types.JSONSlice{}
	}
	switch v := value.(type) {
	case types.JSONSlice:
		return v
	case []any:
		return types.JSONSlice(v)
	case []string:
		out := make(types.JSONSlice, 0, len(v))
		for _, item := range v {
			out = append(out, item)
		}
		return out
	case string:
		return parseJSONSlice([]byte(v))
	case []byte:
		return parseJSONSlice(v)
	default:
		buf, err := json.Marshal(v)
		if err != nil {
			return types.JSONSlice{}
		}
		return parseJSONSlice(buf)
	}
}

func parseJSONSlice(raw []byte) types.JSONSlice {
	out := make(types.JSONSlice, 0)
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return out
	}
	if err := json.Unmarshal([]byte(trimmed), &out); err != nil {
		return types.JSONSlice{}
	}
	return out
}

func shouldKeepNodeStatusForCache(status string) bool {
	normalized := strings.TrimSpace(strings.ToLower(status))
	switch normalized {
	case dagruntime.StatusReady, dagruntime.StatusDone, dagruntime.StatusCached:
		return true
	default:
		return false
	}
}
