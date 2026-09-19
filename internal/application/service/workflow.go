package service

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/biox-dev/gobrave/internal/config"
	"github.com/biox-dev/gobrave/internal/errors"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"github.com/biox-dev/gobrave/internal/utils"
)

type workflowService struct {
	workflowRepo  interfaces.WorkflowRepository
	containerRepo interfaces.ContainerRepository
	projectRepo   interfaces.ProjectRepository
	analysisRepo  interfaces.AnalysisRepository
	cfg           *config.Config
}

func NewWorkflowService(
	workflowRepo interfaces.WorkflowRepository,
	containerRepo interfaces.ContainerRepository,
	projectRepo interfaces.ProjectRepository,
	analysisRepo interfaces.AnalysisRepository,
	cfg *config.Config,
) interfaces.WorkflowService {
	return &workflowService{
		workflowRepo:  workflowRepo,
		containerRepo: containerRepo,
		projectRepo:   projectRepo,
		analysisRepo:  analysisRepo,
		cfg:           cfg,
	}
}

// projectStringID 把 project 的 int64 主键解析为磁盘目录使用的 project_id 字符串。
func (s *workflowService) projectStringID(ctx context.Context, projectPK int64) (string, error) {
	project, err := s.projectRepo.GetProjectByID(ctx, projectPK)
	if err != nil {
		return "", err
	}
	if project == nil {
		return "", nil
	}
	return project.ProjectID, nil
}

func (s *workflowService) GetWorkflowByID(ctx context.Context, id int64) (*types.Workflow, error) {
	return s.workflowRepo.GetWorkflowByID(ctx, id)
}

func (s *workflowService) GetWorkflowByWorkflowID(ctx context.Context, workflowID string) (*types.Workflow, error) {
	return s.workflowRepo.GetWorkflowByWorkflowID(ctx, workflowID)
}

func (s *workflowService) PageWorkflow(ctx context.Context, pagination *types.Pagination, query *types.WorkflowPageQuery) ([]*types.Workflow, int64, error) {
	return s.workflowRepo.PageWorkflow(ctx, pagination, query)
}

func (s *workflowService) ExistsWorkflowInProjectByWorkflowID(ctx context.Context, projectID int64, workflowID string) (*types.Workflow, error) {
	return s.workflowRepo.ExistsWorkflowInProjectByWorkflowID(ctx, projectID, workflowID)
}

func (s *workflowService) PageScript(ctx context.Context, pagination *types.Pagination, query *types.ScriptPageQuery) ([]*types.Script, int64, error) {
	return s.workflowRepo.PageScript(ctx, pagination, query)
}

func (s *workflowService) GetScriptByID(ctx context.Context, id int64) (*types.Script, error) {
	return s.workflowRepo.GetScriptByID(ctx, id)
}

func (s *workflowService) ExistsScriptInProjectByScriptID(ctx context.Context, projectID int64, scriptID string) (*types.Script, error) {
	return s.workflowRepo.ExistsScriptInProjectByScriptID(ctx, projectID, scriptID)
}
func (s *workflowService) GetWorkflowVisByWorkflowID(ctx context.Context, workflowID string) (map[string]any, error) {
	findWorkflow, err := s.workflowRepo.GetWorkflowByWorkflowID(ctx, workflowID)
	if err != nil {
		return nil, err
	}
	return s.GetWorkflowVisByWorkflow(ctx, findWorkflow)
}
func (s *workflowService) GetWorkflowVisByWorkflow(ctx context.Context, findWorkflow *types.Workflow) (map[string]any, error) {
	// findWorkflow, err := s.workflowRepo.GetWorkflowByWorkflowID(ctx, workflowID)
	// if err != nil {
	// 	return nil, err
	// }

	dagDefinition := make(map[string]any)
	if findWorkflow.DagDefinition == "" {
		return dagDefinition, nil
	}
	if err := json.Unmarshal([]byte(findWorkflow.DagDefinition), &dagDefinition); err != nil {
		return dagDefinition, nil
	}

	nodesRaw, ok := dagDefinition["nodes"].([]any)
	if !ok || len(nodesRaw) == 0 {
		return dagDefinition, nil
	}

	scriptIDs := make([]string, 0, len(nodesRaw))
	seen := make(map[string]struct{})
	for _, nodeAny := range nodesRaw {
		node, ok := nodeAny.(map[string]any)
		if !ok {
			continue
		}
		scriptID, _ := node["script_id"].(string)
		if scriptID == "" {
			continue
		}
		if _, exists := seen[scriptID]; exists {
			continue
		}
		seen[scriptID] = struct{}{}
		scriptIDs = append(scriptIDs, scriptID)
	}

	scriptNodeMap := make(map[string]map[string]any)
	if len(scriptIDs) > 0 {
		scripts, err := s.workflowRepo.FindScriptsByScriptIDs(ctx, findWorkflow.ProjectID, scriptIDs)
		if err != nil {
			return nil, err
		}
		// io_schema 以脚本目录下的 io_schema.json 为准，这里只需解析一次项目目录字符串。
		projectID, _ := s.projectStringID(ctx, findWorkflow.ProjectID)
		for _, script := range scripts {
			scriptNodeMap[script.ScriptID] = buildScriptVisItem(s.cfg.Storage.BaseDir, projectID, script)
		}
	}

	nodesRes := make([]any, 0, len(nodesRaw))
	for _, nodeAny := range nodesRaw {
		node, ok := nodeAny.(map[string]any)
		if !ok {
			continue
		}

		scriptID, _ := node["script_id"].(string)
		scriptNode, exists := scriptNodeMap[scriptID]
		if exists {
			merged := cloneAnyMap(scriptNode)
			for k, v := range node {
				merged[k] = v
			}
			if nid := strings.TrimSpace(fmt.Sprintf("%v", merged["node_id"])); nid != "" {
				merged["id"] = nid
			}
			nodesRes = append(nodesRes, merged)
			continue
		}

		merged := cloneAnyMap(node)
		merged["name"] = "unknown"
		if nid := strings.TrimSpace(fmt.Sprintf("%v", merged["node_id"])); nid != "" {
			merged["id"] = nid
		}
		nodesRes = append(nodesRes, merged)
	}

	dagDefinition["nodes"] = nodesRes
	return dagDefinition, nil
}

func (s *workflowService) GetScriptByScriptID(ctx context.Context, projectID int64, scriptID string) (*types.Script, error) {
	return s.workflowRepo.GetScriptByScriptID(ctx, projectID, scriptID)
}

// ScriptToNode 对应 python 版 pipeline_service.script_to_node：
// 按主键查询 script，并复用 buildScriptVisItem（即 GetWorkflowVisByWorkflow 中构造 script 可视化节点的逻辑），
// 再结合已有 dag_definition 计算唯一 node_id，返回可直接被前端画布 addNode 使用的节点数据。
func (s *workflowService) ScriptToNode(ctx context.Context, workflowID int64, scriptID int64) (map[string]any, error) {
	if scriptID == 0 {
		return nil, fmt.Errorf("invalid script id: %d", scriptID)
	}

	findWorkflow, err := s.workflowRepo.GetWorkflowByID(ctx, workflowID)
	if err != nil {
		return nil, err
	}

	script, err := s.workflowRepo.GetScriptByID(ctx, scriptID)
	if err != nil {
		return nil, err
	}

	// get_script_item: 复用 script 可视化节点构造逻辑
	projectID, _ := s.projectStringID(ctx, script.ProjectID)
	node := buildScriptVisItem(s.cfg.Storage.BaseDir, projectID, script)

	// script_to_node: 统计 DAG 中引用同一 script 的节点数量，生成唯一 node_id（script_id_N）
	suffix := countDagNodesByScriptID(findWorkflow.DagDefinition, script.ScriptID) + 1
	node["node_id"] = fmt.Sprintf("%s_%d", script.ScriptID, suffix)

	return node, nil
}

// countDagNodesByScriptID 统计 dag_definition.nodes 中 script_id 等于给定值的节点数量。
func countDagNodesByScriptID(dagDefinition string, scriptID string) int {
	if strings.TrimSpace(dagDefinition) == "" {
		return 0
	}

	var dag map[string]any
	if err := json.Unmarshal([]byte(dagDefinition), &dag); err != nil {
		return 0
	}

	nodesRaw, ok := dag["nodes"].([]any)
	if !ok {
		return 0
	}

	count := 0
	for _, nodeAny := range nodesRaw {
		node, ok := nodeAny.(map[string]any)
		if !ok {
			continue
		}
		if sid, _ := node["script_id"].(string); sid == scriptID {
			count++
		}
	}
	return count
}

func (s *workflowService) GetScriptFileByScriptID(ctx context.Context, scriptID int64) (string, string, error) {
	script, err := s.workflowRepo.GetScriptByID(ctx, scriptID)
	if err != nil {
		return "", "", err
	}
	if script == nil {
		return "", "", nil
	}
	project, err := s.projectRepo.GetProjectByID(ctx, script.ProjectID)
	if err != nil {
		return "", "", err
	}
	if project == nil {
		return "", "", nil
	}
	return utils.GetScriptFile(s.cfg.Storage.BaseDir, project.ProjectID, script.ScriptType, script.ScriptID)
}

// func (s *workflowService) GetScriptMainFileByScriptID(ctx context.Context, scriptID string) (string, string, error) {
// 	script, err := s.workflowRepo.GetScriptByScriptID(ctx, scriptID)
// 	if err != nil {
// 		return "", "", err
// 	}
// 	if script == nil {
// 		return "", "", nil
// 	}

// 	// return utils.GetScriptFile(script.ScriptType, scriptID)
// 	project, err := s.projectRepo.GetProjectByID(ctx, script.ProjectID)
// 	if err != nil {
// 		return "", "", err
// 	}
// 	if project == nil {
// 		return "", "", nil
// 	}
// 	return utils.GetScriptFile(s.cfg.Storage.BaseDir, project.ProjectID, script.ScriptType, script.ScriptID)
// }

func (s *workflowService) GetScriptContainerSnapshotByScriptID(ctx context.Context, scriptID int64) (*types.ScriptContainerSnapshot, error) {
	return s.workflowRepo.GetScriptContainerSnapshotByScriptID(ctx, scriptID)
}

func (s *workflowService) GenerateWorkflowJSONByWorkflowID(ctx context.Context, workflowID int64, storageBaseDir string) (*types.WorkflowJSONExportResponse, error) {
	if strings.TrimSpace(storageBaseDir) == "" {
		return nil, os.ErrInvalid
	}

	workflow, err := s.workflowRepo.GetWorkflowByID(ctx, workflowID)
	if err != nil {
		return nil, err
	}

	workflowMap, err := structToMap(workflow)
	if err != nil {
		return nil, err
	}
	omitExportFields(workflowMap, exportOmitFields)

	var dagDefinition map[string]any
	if workflow.DagDefinition != "" {
		if !json.Valid([]byte(workflow.DagDefinition)) {
			return nil, interfaces.ErrInvalidDagDefinitionJSON
		}
		if err := json.Unmarshal([]byte(workflow.DagDefinition), &dagDefinition); err != nil {
			return nil, err
		}
		workflowMap["dag_definition"] = dagDefinition
	}

	nodeItems, _ := dagDefinition["nodes"].([]any)
	scriptIDs := make([]string, 0, len(nodeItems))
	seenScriptIDs := make(map[string]struct{})
	for _, nodeAny := range nodeItems {
		node, ok := nodeAny.(map[string]any)
		if !ok {
			continue
		}
		scriptID, _ := node["script_id"].(string)
		if scriptID == "" {
			continue
		}
		if _, exists := seenScriptIDs[scriptID]; exists {
			continue
		}
		seenScriptIDs[scriptID] = struct{}{}
		scriptIDs = append(scriptIDs, scriptID)
	}

	scripts := make([]map[string]any, 0, len(scriptIDs))
	containerTemplateSpecs := make([]map[string]any, 0)
	containerTemplateDefinitions := make([]map[string]any, 0)
	containerImages := make([]map[string]any, 0)
	seenSpecIDs := make(map[int64]struct{})
	seenDefinitionIDs := make(map[int64]struct{})
	seenImageIDs := make(map[int64]struct{})
	for _, scriptID := range scriptIDs {
		script, scriptErr := s.workflowRepo.GetScriptByScriptID(ctx, workflow.ProjectID, scriptID)
		if scriptErr != nil {
			// if stderrs.Is(scriptErr, gorm.ErrRecordNotFound) {
			// 	continue
			// }
			return nil, errors.NewInternalServerError(fmt.Sprintf("failed to get script by script_id: %s and project_id: %d", scriptID, workflow.ProjectID)).WithDetails(scriptErr.Error())
		}

		scriptMap, scriptMapErr := structToMap(script)
		if scriptMapErr != nil {
			return nil, scriptMapErr
		}
		omitExportFields(scriptMap, exportOmitFields)

		// 脚本只保存 container_template_id（绑定行主键），导出的容器资产由绑定行反查：
		// 绑定行 → 运行配置（spec_id）→ 镜像（image_id），三者各自按主键去重。
		if assetErr := s.collectScriptContainerAssets(ctx, script.ContainerTemplateID,
			&containerTemplateSpecs, &containerTemplateDefinitions, &containerImages,
			seenSpecIDs, seenDefinitionIDs, seenImageIDs); assetErr != nil {
			return nil, assetErr
		}

		scripts = append(scripts, scriptMap)
	}

	exportPayload := &types.WorkflowJSONExportResponse{
		WorkflowID:                   workflow.WorkflowID,
		Workflow:                     workflowMap,
		Scripts:                      scripts,
		ContainerTemplateSpecs:       containerTemplateSpecs,
		ContainerTemplateDefinitions: containerTemplateDefinitions,
		ContainerImages:              containerImages,
	}

	exportDir := filepath.Join(storageBaseDir, "pipeline", "tools", workflow.WorkflowID)
	if err := os.MkdirAll(exportDir, 0o755); err != nil {
		return nil, err
	}

	// exportPath := filepath.Join(exportDir, "workflow.json")
	// exportBytes, err := json.MarshalIndent(exportPayload, "", "  ")
	// if err != nil {
	// 	return nil, err
	// }
	// if err := os.WriteFile(exportPath, exportBytes, 0o644); err != nil {
	// 	return nil, err
	// }

	// exportPayload.Path = exportPath
	return exportPayload, nil
}

func (s *workflowService) GenerateScriptJSONByScriptID(ctx context.Context, scriptID int64) (*types.ScriptJSONExportResponse, error) {
	script, err := s.workflowRepo.GetScriptByID(ctx, scriptID)
	if err != nil {
		return nil, err
	}

	scriptMap, err := structToMap(script)
	if err != nil {
		return nil, err
	}
	omitExportFields(scriptMap, exportOmitFields)

	containerTemplateSpecs := make([]map[string]any, 0)
	containerTemplateDefinitions := make([]map[string]any, 0)
	containerImages := make([]map[string]any, 0)
	// 脚本只保存 container_template_id（绑定行主键），导出的容器资产由绑定行反查：
	// 绑定行 → 运行配置（spec_id）→ 镜像（image_id）。
	if assetErr := s.collectScriptContainerAssets(ctx, script.ContainerTemplateID,
		&containerTemplateSpecs, &containerTemplateDefinitions, &containerImages,
		make(map[int64]struct{}), make(map[int64]struct{}), make(map[int64]struct{})); assetErr != nil {
		return nil, assetErr
	}

	return &types.ScriptJSONExportResponse{
		ScriptID:                     script.ScriptID,
		Script:                       scriptMap,
		ContainerTemplateSpecs:       containerTemplateSpecs,
		ContainerTemplateDefinitions: containerTemplateDefinitions,
		ContainerImages:              containerImages,
	}, nil
}

// collectScriptContainerAssets 收集脚本引用的一整套容器资产：绑定行 → 运行配置 → 镜像。
//
// 三个列表各自按主键去重（同一绑定行被多个脚本引用、同一运行配置被多个绑定行引用、
// 同一镜像被多个绑定行引用时都不会重复导出），顺序固定为「先绑定行、再配置、最后镜像」，
// 与安装侧的依赖顺序一致（绑定行引用配置与镜像）。
//
// templateID 是脚本的 container_template_id，即 go_container_template_definition 的主键；
// 绑定行已被删除等取不到的情况只跳过，不阻断导出。
func (s *workflowService) collectScriptContainerAssets(
	ctx context.Context,
	templateID int64,
	specs, definitions, images *[]map[string]any,
	seenSpecIDs, seenDefinitionIDs, seenImageIDs map[int64]struct{},
) error {
	if templateID == 0 {
		return nil
	}

	definition, err := s.containerRepo.GetContainerTemplateDefinitionByID(ctx, templateID)
	if err != nil || definition == nil {
		return nil
	}

	if _, exists := seenDefinitionIDs[definition.ID]; !exists {
		definitionMap, mapErr := buildContainerTemplateDefinitionExportMap(definition)
		if mapErr != nil {
			return mapErr
		}
		seenDefinitionIDs[definition.ID] = struct{}{}
		*definitions = append(*definitions, definitionMap)
	}

	if specErr := s.collectContainerTemplateSpecExportMap(ctx, definition.SpecID, specs, seenSpecIDs); specErr != nil {
		return specErr
	}

	return s.collectContainerImageExportMap(ctx, definition.ImageID, images, seenImageIDs)
}

// buildContainerTemplateSpecExportMap 把容器运行配置转成导出用的 JSON map。
// 运行配置只描述「怎么跑」，不含任何镜像引用（镜像由绑定行通过 image_id 承载）。
func buildContainerTemplateSpecExportMap(spec *types.ContainerTemplateSpec) (map[string]any, error) {
	if spec == nil {
		return nil, nil
	}
	specMap, err := structToMap(spec)
	if err != nil {
		return nil, err
	}
	omitExportFields(specMap, exportOmitFields)
	return specMap, nil
}

// buildContainerTemplateDefinitionExportMap 把「运行配置 × 镜像」绑定行转成导出用的 JSON map。
// 绑定行是脚本 container_template_id 指向的实体，主键必须原样导出，spec_id / image_id 保持引用关系。
func buildContainerTemplateDefinitionExportMap(definition *types.ContainerTemplateDefinition) (map[string]any, error) {
	if definition == nil {
		return nil, nil
	}
	definitionMap, err := structToMap(definition)
	if err != nil {
		return nil, err
	}
	omitExportFields(definitionMap, exportOmitFields)
	return definitionMap, nil
}

// buildContainerImageExportMap 把容器镜像转成导出用的 JSON map。
//
// 镜像使用 ContainerImageExport（本就不含 created_at / updated_at），
// 这里仍统一走 exportOmitFields，与运行配置、绑定行、脚本的导出口径保持一致：
// 任何「每次保存都会变化」的字段（目前是 updated_at）都不进导出文件，
// 避免无意义的 git diff；后续若 ContainerImageExport 加回时间字段也能自动被剔除。
func buildContainerImageExportMap(image *types.ContainerImage) (map[string]any, error) {
	if image == nil {
		return nil, nil
	}

	imageMap, err := structToMap(image.ToExport())
	if err != nil {
		return nil, err
	}
	omitExportFields(imageMap, exportOmitFields)
	return imageMap, nil
}

// collectContainerImageExportMap 按主键去重地收集导出用的容器镜像：
// 首次遇到 imageID 时查询镜像并追加到 images，已收集过则直接返回（去重）。
// 镜像已被删除等取不到镜像的情况只跳过，不阻断导出。
func (s *workflowService) collectContainerImageExportMap(ctx context.Context, imageID int64, images *[]map[string]any, seen map[int64]struct{}) error {
	if imageID == 0 {
		return nil
	}
	if _, exists := seen[imageID]; exists {
		return nil
	}

	image, err := s.containerRepo.GetContainerImageByID(ctx, imageID)
	if err != nil || image == nil {
		return nil
	}

	imageMap, err := buildContainerImageExportMap(image)
	if err != nil {
		return err
	}
	seen[imageID] = struct{}{}
	*images = append(*images, imageMap)
	return nil
}

// collectContainerTemplateSpecExportMap 按主键去重地收集导出用的容器运行配置：
// 首次遇到 specID 时查询并追加到 specs，已收集过则直接返回（去重）。
// 运行配置已被删除等取不到的情况只跳过，不阻断导出。
func (s *workflowService) collectContainerTemplateSpecExportMap(ctx context.Context, specID int64, specs *[]map[string]any, seen map[int64]struct{}) error {
	if specID == 0 {
		return nil
	}
	if _, exists := seen[specID]; exists {
		return nil
	}

	spec, err := s.containerRepo.GetContainerTemplateSpecByID(ctx, specID)
	if err != nil || spec == nil {
		return nil
	}

	specMap, err := buildContainerTemplateSpecExportMap(spec)
	if err != nil {
		return err
	}
	seen[specID] = struct{}{}
	*specs = append(*specs, specMap)
	return nil
}

func (s *workflowService) CreateWorkflow(ctx context.Context, workflow *types.Workflow) error {
	return s.workflowRepo.CreateWorkflow(ctx, workflow)
}

func (s *workflowService) UpdateWorkflow(ctx context.Context, workflow *types.Workflow) error {
	return s.workflowRepo.UpdateWorkflow(ctx, workflow)
}

// UpdateWorkflowDagDefinition 仅更新 workflow 的 dag_definition，不动其他字段。
func (s *workflowService) UpdateWorkflowDagDefinition(ctx context.Context, workflowID int64, dagDefinition string) error {
	if workflowID <= 0 {
		return fmt.Errorf("invalid workflow id: %d", workflowID)
	}
	return s.workflowRepo.UpdateWorkflowDagDefinition(ctx, workflowID, dagDefinition)
}

func (s *workflowService) DeleteWorkflow(ctx context.Context, id int64) error {
	if id <= 0 {
		return fmt.Errorf("invalid workflow id: %d", id)
	}

	workflow, err := s.workflowRepo.GetWorkflowByID(ctx, id)
	if err != nil {
		return err
	}

	analyses, err := s.analysisRepo.ListAnalysisByWorkflowID(ctx, workflow.WorkflowID)
	if err != nil {
		return fmt.Errorf("failed to check existing analyses: %w", err)
	}
	if len(analyses) > 0 {
		return fmt.Errorf("cannot delete workflow: %d associated analysis record(s) exist, delete them first", len(analyses))
	}

	return s.workflowRepo.DeleteWorkflowByID(ctx, id)
}

func (s *workflowService) CreateScript(ctx context.Context, script *types.Script) error {
	return s.workflowRepo.CreateScript(ctx, script)
}

func (s *workflowService) UpdateScript(ctx context.Context, script *types.Script) error {
	return s.workflowRepo.UpdateScript(ctx, script)
}

func (s *workflowService) DeleteScript(ctx context.Context, id int64) error {
	if id <= 0 {
		return fmt.Errorf("invalid script id: %d", id)
	}

	script, err := s.workflowRepo.GetScriptByID(ctx, id)
	if err != nil {
		return err
	}

	// 1. Check: no AnalysisNodes reference this script
	nodes, err := s.analysisRepo.ListAnalysisNodesByProjectIDAndScriptID(ctx, script.ProjectID, script.ID)
	if err != nil {
		return fmt.Errorf("failed to check existing analysis nodes: %w", err)
	}
	if len(nodes) > 0 {
		return fmt.Errorf("cannot delete script: %d associated analysis node(s) exist, delete them first", len(nodes))
	}

	// 2. Check: script is not referenced in any Workflow's DagDefinition
	workflows, err := s.workflowRepo.ListWorkflowsByProjectID(ctx, script.ProjectID)
	if err != nil {
		return fmt.Errorf("failed to list workflows for project: %w", err)
	}
	for _, wf := range workflows {
		if strings.TrimSpace(wf.DagDefinition) == "" {
			continue
		}
		var dag map[string]any
		if err := json.Unmarshal([]byte(wf.DagDefinition), &dag); err != nil {
			continue
		}
		nodesAny, _ := dag["nodes"].([]any)
		for _, n := range nodesAny {
			node, ok := n.(map[string]any)
			if !ok {
				continue
			}
			nodeScriptID := fmt.Sprintf("%v", node["script_id"])
			if nodeScriptID == script.ScriptID {
				return fmt.Errorf("cannot delete script: it is referenced in workflow \"%s\" (id=%d), remove it from the dag_definition first", wf.Name, wf.ID)
			}
		}
	}

	// 3. Delete the script's file directory on disk
	project, err := s.projectRepo.GetProjectByID(ctx, script.ProjectID)
	if err != nil {
		return fmt.Errorf("failed to get project: %w", err)
	}
	scriptDir := utils.GetScriptFileDir(s.cfg.Storage.BaseDir, project.ProjectID, script.ScriptID)
	projectDataPrefix := filepath.Join(s.cfg.Storage.BaseDir, "data", project.ProjectID)
	if strings.HasPrefix(scriptDir, projectDataPrefix) {
		if err := os.RemoveAll(scriptDir); err != nil {
			return fmt.Errorf("failed to remove script directory %s: %w", scriptDir, err)
		}
	}

	return s.workflowRepo.DeleteScriptByID(ctx, id)
}

func (s *workflowService) GetScriptFormJSONByID(ctx context.Context, scriptID int64) ([]any, error) {
	script, err := s.workflowRepo.GetScriptByID(ctx, scriptID)
	if err != nil {
		return nil, err
	}

	formJSONWrap := make([]interface{}, 0)

	// io_schema 不再是 script 的数据库字段，统一从脚本目录的 io_schema.json 读取。
	projectID, err := s.projectStringID(ctx, script.ProjectID)
	if err != nil {
		return nil, err
	}
	ioSchema, err := utils.ReadScriptIOSchema(s.cfg.Storage.BaseDir, projectID, script.ScriptID)
	if err != nil {
		return nil, err
	}
	if inputs, ok := ioSchema["inputs"].([]interface{}); ok {
		formJSONWrap = append(formJSONWrap, inputs...)
	}
	if params, ok := ioSchema["params"].([]interface{}); ok {
		formJSONWrap = append(formJSONWrap, params...)
	}

	// if script.Content != "" {
	// 	content := make(map[string]interface{})
	// 	if err := json.Unmarshal([]byte(script.Content), &content); err != nil {
	// 		return nil, err
	// 	}
	// 	if contentFormJSON, ok := content["formJson"].([]interface{}); ok {
	// 		formJSONWrap = append(formJSONWrap, contentFormJSON...)
	// 	}
	// }
	return formJSONWrap, err
}

func (s *workflowService) GetFormJSONByWorkflowID(ctx context.Context, workflowID string) ([]any, error) {
	findWorkflow, err := s.workflowRepo.GetWorkflowByWorkflowID(ctx, workflowID)
	if err != nil {
		return nil, err
	}

	formJSONWrap := make([]any, 0)
	if findWorkflow.DagDefinition == "" {
		return formJSONWrap, nil
	}

	var dagDefinition map[string]any
	if err := json.Unmarshal([]byte(findWorkflow.DagDefinition), &dagDefinition); err != nil {
		return formJSONWrap, nil
	}

	nodesRaw, ok := dagDefinition["nodes"].([]any)
	if !ok {
		return formJSONWrap, nil
	}

	nodesMap := make(map[string]map[string]any)
	inputScriptIDs := make(map[string]struct{})
	nodeIncomingHandles := make(map[string]map[string]struct{})
	nodeIDsByModuleID := make(map[string][]string)

	edgesRaw, _ := dagDefinition["edges"].([]any)
	for _, edgeAny := range edgesRaw {
		edge, ok := edgeAny.(map[string]any)
		if !ok {
			continue
		}
		targetNodeID, _ := edge["target"].(string)
		targetHandle, _ := edge["targetHandle"].(string)
		if targetNodeID == "" || targetHandle == "" {
			continue
		}
		if _, exists := nodeIncomingHandles[targetNodeID]; !exists {
			nodeIncomingHandles[targetNodeID] = make(map[string]struct{})
		}
		nodeIncomingHandles[targetNodeID][targetHandle] = struct{}{}
	}

	moduleIDs := make([]string, 0)
	for _, nodeAny := range nodesRaw {
		node, ok := nodeAny.(map[string]any)
		if !ok {
			continue
		}
		moduleID, _ := node["script_id"].(string)
		if moduleID == "" {
			continue
		}
		nodeID, _ := node["node_id"].(string)

		nodesMap[moduleID] = node
		moduleIDs = append(moduleIDs, moduleID)
		if nodeID != "" {
			nodeIDsByModuleID[moduleID] = append(nodeIDsByModuleID[moduleID], nodeID)
		}
	}

	if len(moduleIDs) == 0 {
		return formJSONWrap, nil
	}

	scripts, err := s.workflowRepo.FindScriptsByScriptIDs(ctx, findWorkflow.ProjectID, moduleIDs)
	if err != nil {
		return nil, err
	}

	// io_schema 以脚本目录下的 io_schema.json 为准，解析一次项目目录字符串复用。
	projectID, _ := s.projectStringID(ctx, findWorkflow.ProjectID)
	for _, script := range scripts {
		scriptID := script.ScriptID
		ioSchema, _ := utils.ReadScriptIOSchema(s.cfg.Storage.BaseDir, projectID, script.ScriptID)

		inputNames := getInputNames(ioSchema)
		nodeIDs := nodeIDsByModuleID[scriptID]
		missingInputNames := make(map[string]struct{})
		for _, nodeID := range nodeIDs {
			incomingHandles := nodeIncomingHandles[nodeID]
			for inputName := range inputNames {
				if _, ok := incomingHandles[inputName]; !ok {
					missingInputNames[inputName] = struct{}{}
				}
			}
		}
		if len(missingInputNames) > 0 {
			inputScriptIDs[scriptID] = struct{}{}
		}

		if _, isInputScript := inputScriptIDs[scriptID]; isInputScript && len(ioSchema) > 0 {
			merged := make(map[string]any, len(ioSchema)+4)
			for k, v := range ioSchema {
				merged[k] = v
			}
			if node, exists := nodesMap[scriptID]; exists {
				for k, v := range node {
					merged[k] = v
				}
			}
			buildInputScriptFormJSON(merged, &formJSONWrap, missingInputNames)
		}

		if params, ok := ioSchema["params"].([]any); ok {
			formJSONWrap = append(formJSONWrap, params...)
		}

		// if script.Content != "" {
		// 	var content map[string]any
		// 	if err := json.Unmarshal([]byte(script.Content), &content); err == nil {
		// 		if contentFormJSON, ok := content["formJson"].([]any); ok {
		// 			formJSONWrap = append(formJSONWrap, contentFormJSON...)
		// 		}
		// 	}
		// }
	}

	return formJSONWrap, nil
}

func getInputNames(ioSchema map[string]any) map[string]struct{} {
	result := make(map[string]struct{})
	inputs, ok := ioSchema["inputs"].([]any)
	if !ok {
		return result
	}

	for _, itemAny := range inputs {
		item, ok := itemAny.(map[string]any)
		if !ok {
			continue
		}
		name, _ := item["name"].(string)
		if name == "" {
			continue
		}
		result[name] = struct{}{}
	}

	return result
}

func structToMap(value any) (map[string]any, error) {
	content, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}

	result := make(map[string]any)
	if err := json.Unmarshal(content, &result); err != nil {
		return nil, err
	}

	return result, nil
}

// exportOmitFields 是导出 JSON（script.json / workflow.json）时需要剔除的字段。
// updated_at 由数据库自动维护，每次保存都会变化，保留它会产生无意义的 git diff；
// 后续如需剔除其他字段，直接追加到该列表即可。
var exportOmitFields = []string{"updated_at"}

// omitExportFields 从导出 JSON 的顶层 map 中剔除指定字段（keys 为 nil 时不做任何处理）。
func omitExportFields(m map[string]any, keys []string) {
	for _, k := range keys {
		delete(m, k)
	}
}

func filterInputs(items []any, inputNames map[string]struct{}) []any {
	if inputNames == nil {
		return items
	}

	filtered := make([]any, 0, len(items))
	for _, itemAny := range items {
		item, ok := itemAny.(map[string]any)
		if !ok {
			continue
		}
		name, _ := item["name"].(string)
		if name == "" {
			continue
		}
		if _, exists := inputNames[name]; exists {
			filtered = append(filtered, itemAny)
		}
	}

	return filtered
}

func buildInputScriptFormJSON(ioSchema map[string]any, formJSONWrap *[]any, inputNames map[string]struct{}) {
	if scatterAny, ok := ioSchema["scatter"]; ok {
		scatter, ok := scatterAny.(map[string]any)
		if !ok {
			return
		}
		if mode, _ := scatter["mode"].(string); mode == "each" {
			if workflow, ok := ioSchema["workflow"].([]any); ok {
				*formJSONWrap = append(*formJSONWrap, workflow...)
			}
			return
		}
		if inputs, ok := ioSchema["inputs"].([]any); ok {
			*formJSONWrap = append(*formJSONWrap, filterInputs(inputs, inputNames)...)
		}
		return
	}

	if inputs, ok := ioSchema["inputs"].([]any); ok {
		*formJSONWrap = append(*formJSONWrap, filterInputs(inputs, inputNames)...)
	}
}

func buildScriptVisItem(baseDir, projectID string, script *types.Script) map[string]any {
	node := map[string]any{
		"name":         script.ComponentName,
		"id":           script.ScriptID,
		"script_id":    script.ScriptID,
		"node_id":      script.ScriptID + "_1",
		"script_db_id": fmt.Sprintf("%d", script.ID),
		"inputs":       map[string]any{},
		"outputs":      map[string]any{},
	}

	// io_schema 以脚本目录下的 io_schema.json 为准，读取/解析失败时退化为空节点。
	ioSchema, err := utils.ReadScriptIOSchema(baseDir, projectID, script.ScriptID)
	if err != nil || len(ioSchema) == 0 {
		return node
	}

	node["inputs"] = formatIOSchemaItems(ioSchema["inputs"])
	node["outputs"] = formatIOSchemaItems(ioSchema["outputs"])

	if scatter, ok := ioSchema["scatter"]; ok {
		node["scatter"] = scatter
	}
	if gather, ok := ioSchema["gather"]; ok {
		node["gather"] = gather
	}
	if ui, ok := ioSchema["ui"].(map[string]any); ok {
		if color, exists := ui["color"]; exists {
			node["color"] = color
		}
		if icon, exists := ui["icon"]; exists {
			node["icon"] = icon
		}
	}

	return node
}

func formatIOSchemaItems(raw any) map[string]any {
	items, ok := raw.([]any)
	if !ok {
		return map[string]any{}
	}

	result := make(map[string]any)
	for _, itemAny := range items {
		item, ok := itemAny.(map[string]any)
		if !ok {
			continue
		}
		name, _ := item["name"].(string)
		if name == "" {
			continue
		}

		formatted := make(map[string]any)
		for k, v := range item {
			if k == "name" {
				continue
			}
			formatted[k] = v
		}
		result[name] = formatted
	}

	return result
}

func cloneAnyMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
