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

	agenttypes "github.com/biox-dev/gobrave/internal/agent/types"
	agentinterfaces "github.com/biox-dev/gobrave/internal/agent/types/interface"
	"github.com/biox-dev/gobrave/internal/config"
	dagruntime "github.com/biox-dev/gobrave/internal/dag"
	"github.com/biox-dev/gobrave/internal/logger"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"github.com/biox-dev/gobrave/internal/utils"
	"github.com/google/uuid"
)

type analysisService struct {
	analysisRepo     interfaces.AnalysisRepository
	workflowRepo     interfaces.WorkflowRepository
	projectRepo      interfaces.ProjectRepository
	containerService interfaces.ContainerService
	// sessionResources 用于删除资源后清理「会话 ↔ 资源」关联，避免会话结果里残留孤儿节点。
	sessionResources agentinterfaces.SessionResourceStore
	cfg              *config.Config
}

func NewAnalysisService(
	analysisRepo interfaces.AnalysisRepository,
	workflowRepo interfaces.WorkflowRepository,
	projectRepo interfaces.ProjectRepository,
	containerService interfaces.ContainerService,
	sessionResources agentinterfaces.SessionResourceStore,
	cfg *config.Config,
) interfaces.AnalysisService {
	return &analysisService{
		analysisRepo:     analysisRepo,
		workflowRepo:     workflowRepo,
		projectRepo:      projectRepo,
		containerService: containerService,
		sessionResources: sessionResources,
		cfg:              cfg,
	}
}

func (s *analysisService) GetAnalysisByAnalysisID(ctx context.Context, analysisID string) (*types.AnalysisWorkflow, error) {
	return s.analysisRepo.GetAnalysisByAnalysisID(ctx, analysisID)
}

func (s *analysisService) ListAnalysisByProjectID(ctx context.Context, projectID int64, query *types.AnalysisQuey) ([]*types.AnalysisWorkflow, error) {
	return s.analysisRepo.ListAnalysisByProjectID(ctx, projectID, query)
}

func (s *analysisService) PageAnalysisByProjectID(ctx context.Context, pagination *types.Pagination, projectID int64, query *types.AnalysisQuey) ([]*types.AnalysisWorkflow, int64, error) {
	if pagination == nil {
		pagination = &types.Pagination{}
	}

	return s.analysisRepo.PageAnalysisByProjectID(ctx, pagination, projectID, query)
}

func (s *analysisService) GetAnalysisByID(ctx context.Context, analysisID int64) (*types.AnalysisWorkflow, error) {
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

func (s *analysisService) SaveAnalysisController(ctx context.Context, input *types.AnalysisControllerSaveInput) (*types.AnalysisWorkflow, error) {
	if input == nil || input.RequestParam == nil {
		return nil, fmt.Errorf("request_param is required")
	}
	// workflow_id 是工作流 int64 主键（pipeline_components_relation.id）。
	workflowID := input.WorkflowID
	if workflowID <= 0 {
		return nil, fmt.Errorf("workflow_id is required")
	}
	workflow, err := s.workflowRepo.GetWorkflowByID(ctx, workflowID)
	if err != nil {
		return nil, fmt.Errorf("failed to get workflow by id: %v", err)
	}
	if workflow == nil {
		return nil, fmt.Errorf("workflow not found for id: %d", workflowID)
	}

	if input.Project == nil {
		return nil, fmt.Errorf("project is required")
	}

	analysisName := strings.TrimSpace(input.NodeName)
	if analysisName == "" {
		return nil, fmt.Errorf("node_name is required")
	}

	parseResult := input.ParseAnalysisResult
	if parseResult == nil {
		parseResult = map[string]any{}
	}
	analysisID := input.AnalysisID

	// cache_type 由请求顶层字段提供；未显式指定时回退到历史 request_param.is_cache。
	cacheType := input.CacheType
	if cacheType < types.CacheTypeRerunAll || cacheType > types.CacheTypeReuseWhenScriptAndParamsUnchanged {
		if toBool(input.RequestParam["is_cache"]) {
			cacheType = types.CacheTypeReuseExistingNode
		} else {
			cacheType = types.CacheTypeRerunAll
		}
	}
	dataComponentIDs := toString(input.RequestParam["data_component_ids"])

	var saved *types.AnalysisWorkflow
	saveErr := s.analysisRepo.WithTransaction(ctx, func(tx interfaces.AnalysisRepository) error {
		var existing *types.AnalysisWorkflow
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
		if existing != nil {
			analysisID = existing.ID
		}
		if analysisID == 0 {
			analysisID = utils.GenerateID()
		}

		// 分析根目录由 base_dir + 项目 / 工作流 / 分析三个主键推导，不落库
		// （见 types.AnalysisWorkflow.Dir），其余路径由它派生。
		workspaceDir := utils.GetAnalysisDir(baseDir, input.Project.ID, workflow.ID, analysisID)

		if err := os.MkdirAll(workspaceDir, 0o755); err != nil {
			return err
		}

		paramsPath := utils.AnalysisLayoutFor(workspaceDir).ParamsPath

		// request_param 只保存分析参数；主键 / 工作流 / 名称 / 缓存策略都是独立列。
		parseResult["tools_output_dir"] = workspaceDir

		if err := utils.WriteJSONFile(paramsPath, parseResult); err != nil {
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
				"workflow_id":   workflowID,
				// "analysis_type": analysisType,
				// "is_report":          input.IsReport,
				"data_component_ids": dataComponentIDs,
				"updated_at":         time.Now().UTC(),
			}
			if strings.TrimSpace(existing.JobStatus) != "running" {
				updateValues["job_status"] = "updated"
			}
			// 只有显式声明调度器时才覆写，避免普通保存把原调度器信息抹掉。
			if mode := strings.TrimSpace(input.SchedulerMode); mode != "" {
				updateValues["scheduler_mode"] = types.NormalizeSchedulerMode(mode)
			}
			if err := tx.UpdateAnalysisByID(ctx, analysisID, updateValues); err != nil {
				return err
			}
		} else {
			newAnalysis := &types.AnalysisWorkflow{
				ID:         analysisID,
				ProjectID:  input.Project.ID,
				AnalysisID: uuid.NewString(),
				WorkflowID: workflowID,
				// AnalysisType:    analysisType,
				AnalysisName: analysisName,
				RequestParam: string(requestParamJSON),
				JobStatus:    "created",
				// ServerStatus:     "",
				CacheType:        cacheType,
				DataComponentIDs: dataComponentIDs,
				Used:             true,
				// 未显式声明时落为 legacy，保证恢复入口能确定归属。
				SchedulerMode: types.NormalizeSchedulerMode(input.SchedulerMode),
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
	prefix := filepath.Join(s.cfg.Storage.BaseDir, "data", strconv.FormatInt(project.ID, 10))
	workspaceDir := node.Dir()
	//判断是否以prefix开头 如果不是就不删除
	if !strings.HasPrefix(workspaceDir, prefix) {
		logger.Warnf(ctx, "[NodeOrchestrator] workspace dir=%s is not under project data dir=%s, skip delete", workspaceDir, prefix)
		return fmt.Errorf("workspace dir is not under project data dir")
	}
	// 	删除工作目录
	if err := os.RemoveAll(workspaceDir); err != nil {
		logger.Warnf(ctx, "[NodeOrchestrator] failed to remove workspace dir=%s, err=%v", workspaceDir, err)
		return err
	}
	if id <= 0 {
		return fmt.Errorf("invalid analysis node id: %d", id)
	}
	// 节点对应的容器实例（OwnerType=dag_node）尽力清理，避免删除节点后容器继续占用资源。
	if s.containerService != nil {
		if err := s.containerService.DeleteContainerInstancesByOwnerTypeAndOwnerIDs(
			ctx, types.ContainerOwnerDagNode, []int64{id},
		); err != nil {
			logger.Warnf(ctx, "[AnalysisService] failed to delete containers for analysis node id=%d, err=%v", id, err)
		}
	}
	if err := s.analysisRepo.DeleteAnalysisNodeByID(ctx, id); err != nil {
		return err
	}
	// 节点记录删除后同步清理指向它的「会话 ↔ 资源」关联，避免会话结果面板残留孤儿节点。
	// 关联清理失败不影响删除结果，仅记录日志。
	s.deleteSessionResources(ctx, agenttypes.SessionResourceTypeAnalysisNode, []int64{id})
	return nil
}

// deleteSessionResources 清理指向已删除资源的会话关联；store 缺失时直接跳过（便于测试）。
func (s *analysisService) deleteSessionResources(ctx context.Context, resourceType agenttypes.SessionResourceType, resourceIDs []int64) {
	if s == nil || s.sessionResources == nil || len(resourceIDs) == 0 {
		return
	}
	if err := s.sessionResources.DeleteByResources(ctx, resourceType, resourceIDs); err != nil {
		logger.Warnf(ctx, "[AnalysisService] failed to delete session resources type=%s ids=%v, err=%v", resourceType, resourceIDs, err)
	}
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
	projectDataDir := filepath.Join(s.cfg.Storage.BaseDir, "data", strconv.FormatInt(project.ID, 10))

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
		if dir := node.Dir(); dir != "" {
			if strings.HasPrefix(dir, projectDataDir) {
				if err := os.RemoveAll(dir); err != nil {
					logger.Warnf(ctx, "[AnalysisService] failed to remove node workspace dir=%s, err=%v", dir, err)
				}
			} else {
				logger.Warnf(ctx, "[AnalysisService] node workspace dir=%s is not under project data dir=%s, skip delete", dir, projectDataDir)
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

	// 6.1 清理这些节点以及分析运行本身对应的「会话 ↔ 资源」关联。
	s.deleteSessionResources(ctx, agenttypes.SessionResourceTypeAnalysisNode, nodeIDs)
	s.deleteSessionResources(ctx, agenttypes.SessionResourceTypeAnalysis, []int64{id})

	// 7. Delete the analysis workspace
	if dir := analysis.Dir(); dir != "" {
		if strings.HasPrefix(dir, projectDataDir) {
			if err := os.RemoveAll(dir); err != nil {
				logger.Warnf(ctx, "[AnalysisService] failed to remove analysis workspace dir=%s, err=%v", dir, err)
			}
		} else {
			logger.Warnf(ctx, "[AnalysisService] analysis workspace dir=%s is not under project data dir=%s, skip delete", dir, projectDataDir)
		}
	}

	// 8. Delete the analysis record
	return s.analysisRepo.DeleteAnalysisByID(ctx, id)
}

func (s *analysisService) persistDagRuntime(ctx context.Context, repo interfaces.AnalysisRepository, analysis *types.AnalysisWorkflow, dagRuntime map[string]any) error {
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
		// 存在的 node 保持原有主键 / analysis_node_id，避免重复创建。
		existing := existingByNodeID[nodeID]
		analysisNodeID := strings.TrimSpace(toString(row["analysis_node_id"]))
		id := utils.GenerateID()
		if existing != nil {
			if strings.TrimSpace(existing.AnalysisNodeID) != "" {
				analysisNodeID = existing.AnalysisNodeID
			}
			if existing.ID != 0 {
				id = existing.ID
			}
		}
		if analysisNodeID == "" {
			analysisNodeID = "node-" + uuid.NewString()
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
			ID:              id,
			AnalysisNodeID:  analysisNodeID,
			AnalysisID:      analysis.ID,
			NodeID:          nodeID,
			NodeName:        toString(row["node_name"]),
			SampleID:        toString(row["sample_id"]),
			ScriptID:        script.ID,
			InputsPatterns:  toJSONMap(row["inputs_patterns"]),
			ResolvedInputs:  toJSONMap(row["resolved_inputs"]),
			OutputPatterns:  toJSONMap(row["output_patterns"]),
			ResolvedOutputs: toJSONMap(row["resolved_outputs"]),
			// request_param 由编译器从 analysis.request_param 派生；空值保持空串，
			// 避免用 "{}" 覆盖掉读取时的兼容回退。
			RequestParam:           toJSONString(row["request_param"]),
			Status:                 status,
			Executor:               toString(row["executor"]),
			Retry:                  intFromAny(row["retry"], 0),
			MaxRetry:               intFromAny(row["max_retry"], 3),
			CacheHit:               cacheHit,
			UpstreamIDs:            toJSONSlice(row["upstream_ids"]),
			DownstreamIDs:          toJSONSlice(row["downstream_ids"]),
			InputValidationErrors:  toJSONSlice(row["input_validation_errors"]),
			OutputValidationErrors: toJSONSlice(row["output_validation_errors"]),
			CreationSource:         types.AnalysisNodeCreationSourceScheduler,
		}
		// 目录由 base_dir + 项目 + 节点主键推导（见 types.AnalysisNode.Dir），这里只需建好输出目录。
		node.SetBaseDir(analysis.BaseDir())
		if err := os.MkdirAll(node.OutputDirectory(), 0o755); err != nil {
			return err
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

func (s *analysisService) ListAnalysisByWorkflowID(ctx context.Context, workflowID int64) ([]*types.AnalysisWorkflow, error) {
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

// toJSONString renders a map-like payload into the JSON string stored in a
// text column (for example analysis_nodes.request_param). A nil, blank or empty
// payload yields an empty string so callers never overwrite a column with "{}"
// when the compiler did not emit anything.
func toJSONString(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(v)
	case types.JSONMap:
		if len(v) == 0 {
			return ""
		}
	case map[string]any:
		if len(v) == 0 {
			return ""
		}
	}
	buf, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(buf)
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
