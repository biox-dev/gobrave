package agentworkflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	agenttypes "github.com/biox-dev/gobrave/internal/agent/types"
	agentinterfaces "github.com/biox-dev/gobrave/internal/agent/types/interface"
	"github.com/biox-dev/gobrave/internal/config"
	"github.com/biox-dev/gobrave/internal/exportcodec"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"github.com/biox-dev/gobrave/internal/utils"
	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// workflowManager 实现 runtimecontext.WorkflowCreator（create_workflow）与
// runtimecontext.WorkflowUpdater（update_workflow）。
//
// 工作流目录（<base_dir>/data/<project>/pipeline/workflow/<relation_id>/）内的
// dag_definition.json / io_schema.json / README.md 是唯一数据源，库中只保存元数据；
// 因此创建与更新都必须同时落库 + 落盘，二者不可拆开。
//
// 与创建脚本一致：写完目录后不做 git 提交（发布 / 提交版本仍由用户触发），
// 但会像 SaveWorkflow 一样重新生成 workflow.json，保证导出文件与数据库一致。
type workflowManager struct {
	workflowSvc interfaces.WorkflowService
	projectRepo interfaces.ProjectRepository
	codecs      *exportcodec.Registry
	// resources 记录「当前会话 ↔ 工作流」的关联（创建 / 更新成功后写入）。
	resources agentinterfaces.SessionResourceService
	cfg       *config.Config
}

// NewManager 构建「创建工作流 / 更新工作流」执行器并注入内置工具体系。
//
// 返回值同时实现 runtimecontext.WorkflowCreator（create_workflow）与
// runtimecontext.WorkflowUpdater（update_workflow）：两者共享同一份工作流业务服务
// 依赖，因此由同一个对象承载（builtin.Deps 只注入一次即可）。
func NewManager(
	workflowSvc interfaces.WorkflowService,
	projectRepo interfaces.ProjectRepository,
	codecs *exportcodec.Registry,
	resources agentinterfaces.SessionResourceService,
	cfg *config.Config,
) runtimecontext.WorkflowCreator {
	return &workflowManager{
		workflowSvc: workflowSvc,
		projectRepo: projectRepo,
		codecs:      codecs,
		resources:   resources,
		cfg:         cfg,
	}
}

// CreateWorkflow 新建工作流：落库元数据 + 初始化工作流目录。
func (m *workflowManager) CreateWorkflow(ctx context.Context, in runtimecontext.WorkflowCreateInput) (runtimecontext.WorkflowCreateResult, error) {
	if in.ProjectID <= 0 {
		return runtimecontext.WorkflowCreateResult{}, fmt.Errorf("create_workflow: project_id is required")
	}
	if m.workflowSvc == nil || m.projectRepo == nil {
		return runtimecontext.WorkflowCreateResult{}, fmt.Errorf("create_workflow: required services are not configured")
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return runtimecontext.WorkflowCreateResult{}, fmt.Errorf("create_workflow: name is required")
	}
	dagDefinition := strings.TrimSpace(in.DagDefinition)
	if err := validateJSONObject("dag_definition", dagDefinition); err != nil {
		return runtimecontext.WorkflowCreateResult{}, fmt.Errorf("create_workflow: %w", err)
	}
	ioSchema := strings.TrimSpace(in.IOSchema)
	if err := validateJSONObject("io_schema", ioSchema); err != nil {
		return runtimecontext.WorkflowCreateResult{}, fmt.Errorf("create_workflow: %w", err)
	}

	baseDir := m.storageBaseDir()
	if baseDir == "" {
		return runtimecontext.WorkflowCreateResult{}, fmt.Errorf("create_workflow: storage.base_dir is empty")
	}
	project, err := m.projectRepo.GetProjectByID(ctx, in.ProjectID)
	if err != nil {
		return runtimecontext.WorkflowCreateResult{}, fmt.Errorf("create_workflow: failed to get project %d: %w", in.ProjectID, err)
	}
	if project == nil || project.ID == 0 {
		return runtimecontext.WorkflowCreateResult{}, fmt.Errorf("create_workflow: project %d not found", in.ProjectID)
	}

	category := strings.TrimSpace(in.Category)
	if category == "" {
		category = "default"
	}

	// 1) 落库：WorkflowID（relation_id）是工作流目录名，必须唯一，因此这里生成 UUID。
	workflow := &types.Workflow{
		ProjectID:          in.ProjectID,
		Name:               name,
		Description:        strings.TrimSpace(in.Description),
		Category:           category,
		Tags:               datatypes.JSON([]byte("[]")),
		WorkflowID:         uuid.NewString(),
		InputComponentIDs:  datatypes.JSON([]byte("[]")),
		OutputComponentIDs: datatypes.JSON([]byte("[]")),
	}
	if err := m.workflowSvc.CreateWorkflow(ctx, workflow); err != nil {
		return runtimecontext.WorkflowCreateResult{}, fmt.Errorf("create_workflow: failed to create workflow: %w", err)
	}

	// 记录「当前会话创建了该工作流」；关联失败不影响创建结果。
	if m.resources != nil {
		_ = m.resources.LinkFromContext(ctx, agenttypes.SessionResourceTypeWorkflow, workflow.ID)
	}

	// 2) 初始化工作流目录：dag_definition.json / io_schema.json / README.md。
	files := utils.WorkflowLayout(baseDir, workflow.ProjectID, workflow.WorkflowID)
	if err := os.MkdirAll(files.Dir, 0o755); err != nil {
		return runtimecontext.WorkflowCreateResult{}, fmt.Errorf("create_workflow: failed to prepare workflow dir: %w", err)
	}
	warnings := make([]string, 0, 1)
	if err := m.writeWorkflowFiles(ctx, workflow, baseDir, dagDefinition, ioSchema, strings.TrimSpace(in.Readme)); err != nil {
		return runtimecontext.WorkflowCreateResult{}, fmt.Errorf("create_workflow: %w", err)
	}
	if warn := m.regenerateWorkflowJSON(ctx, workflow, baseDir); warn != "" {
		warnings = append(warnings, warn)
	}

	result := runtimecontext.WorkflowCreateResult{
		WorkflowBrief: m.brief(baseDir, workflow),
		Warnings:      warnings,
	}
	return result, nil
}

// UpdateWorkflow 更新工作流的元数据与 DAG 定义 / io_schema。
//
// 读改写语义：以库中完整记录为基准，只覆盖本次给出的字段再整体回写
// （workflowRepository.UpdateWorkflow 是整体替换）。
func (m *workflowManager) UpdateWorkflow(ctx context.Context, in runtimecontext.WorkflowUpdateInput) (runtimecontext.WorkflowUpdateResult, error) {
	if in.ProjectID <= 0 {
		return runtimecontext.WorkflowUpdateResult{}, fmt.Errorf("update_workflow: project_id is required")
	}
	if m.workflowSvc == nil {
		return runtimecontext.WorkflowUpdateResult{}, fmt.Errorf("update_workflow: required services are not configured")
	}
	if in.Name == nil && in.Description == nil && in.Category == nil && in.DagDefinition == nil && in.IOSchema == nil {
		return runtimecontext.WorkflowUpdateResult{}, fmt.Errorf("update_workflow: at least one of name / description / category / dag_definition / io_schema is required")
	}
	if in.Name != nil && strings.TrimSpace(*in.Name) == "" {
		return runtimecontext.WorkflowUpdateResult{}, fmt.Errorf("update_workflow: name must not be empty")
	}
	dagDefinition := ""
	if in.DagDefinition != nil {
		dagDefinition = strings.TrimSpace(*in.DagDefinition)
		if err := validateJSONObject("dag_definition", dagDefinition); err != nil {
			return runtimecontext.WorkflowUpdateResult{}, fmt.Errorf("update_workflow: %w", err)
		}
	}
	ioSchema := ""
	if in.IOSchema != nil {
		ioSchema = strings.TrimSpace(*in.IOSchema)
		if err := validateJSONObject("io_schema", ioSchema); err != nil {
			return runtimecontext.WorkflowUpdateResult{}, fmt.Errorf("update_workflow: %w", err)
		}
	}

	workflow, err := m.locateWorkflow(ctx, in.ProjectID, in.WorkflowID)
	if err != nil {
		return runtimecontext.WorkflowUpdateResult{}, err
	}

	changed := make([]string, 0, 5)
	if in.Name != nil {
		name := strings.TrimSpace(*in.Name)
		if name != workflow.Name {
			changed = append(changed, "name")
		}
		workflow.Name = name
	}
	if in.Description != nil {
		description := strings.TrimSpace(*in.Description)
		if description != workflow.Description {
			changed = append(changed, "description")
		}
		workflow.Description = description
	}
	if in.Category != nil {
		category := strings.TrimSpace(*in.Category)
		if category != workflow.Category {
			changed = append(changed, "category")
		}
		workflow.Category = category
	}
	if dagDefinition != "" {
		changed = append(changed, "dag_definition")
	}
	if ioSchema != "" {
		changed = append(changed, "io_schema")
	}

	if err := m.workflowSvc.UpdateWorkflow(ctx, workflow); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return runtimecontext.WorkflowUpdateResult{}, fmt.Errorf("update_workflow: workflow %d not found", workflow.ID)
		}
		return runtimecontext.WorkflowUpdateResult{}, fmt.Errorf("update_workflow: failed to update workflow: %w", err)
	}

	baseDir := m.storageBaseDir()
	if baseDir == "" {
		return runtimecontext.WorkflowUpdateResult{}, fmt.Errorf("update_workflow: storage.base_dir is empty")
	}
	if err := m.writeWorkflowFiles(ctx, workflow, baseDir, dagDefinition, ioSchema, ""); err != nil {
		return runtimecontext.WorkflowUpdateResult{}, fmt.Errorf("update_workflow: %w", err)
	}
	warnings := make([]string, 0, 1)
	if warn := m.regenerateWorkflowJSON(ctx, workflow, baseDir); warn != "" {
		warnings = append(warnings, warn)
	}

	// 记录「当前会话修改了该工作流」：别的会话修改同一工作流时会各记一条关联。
	if m.resources != nil {
		_ = m.resources.LinkFromContext(ctx, agenttypes.SessionResourceTypeWorkflow, workflow.ID)
	}

	return runtimecontext.WorkflowUpdateResult{
		WorkflowBrief: m.brief(baseDir, workflow),
		Changed:       changed,
		Warnings:      warnings,
	}, nil
}

// locateWorkflow 按主键定位工作流，并校验其属于当前激活项目。
func (m *workflowManager) locateWorkflow(ctx context.Context, projectID, workflowID int64) (*types.Workflow, error) {
	if workflowID <= 0 {
		return nil, fmt.Errorf("update_workflow: workflow_id is required")
	}
	workflow, err := m.workflowSvc.GetWorkflowByID(ctx, workflowID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("update_workflow: workflow not found in the active project")
		}
		return nil, fmt.Errorf("update_workflow: failed to load workflow: %w", err)
	}
	if workflow == nil || workflow.ID == 0 {
		return nil, fmt.Errorf("update_workflow: workflow not found in the active project")
	}
	if workflow.ProjectID != projectID {
		return nil, fmt.Errorf("update_workflow: workflow %d does not belong to the active project %d", workflow.ID, projectID)
	}
	return workflow, nil
}

// writeWorkflowFiles 把 dag_definition / io_schema / README 写入工作流目录。
//
// 空内容一律不落盘（utils 层已按此语义处理），因此局部更新不会用空值覆盖历史文件。
func (m *workflowManager) writeWorkflowFiles(ctx context.Context, workflow *types.Workflow, baseDir, dagDefinition, ioSchema, readme string) error {
	if dagDefinition != "" {
		if err := utils.WriteWorkflowDagDefinition(baseDir, workflow.ProjectID, workflow.WorkflowID, dagDefinition); err != nil {
			return fmt.Errorf("failed to write dag_definition.json: %w", err)
		}
	}
	if ioSchema != "" {
		if err := utils.WriteWorkflowIOSchema(baseDir, workflow.ProjectID, workflow.WorkflowID, ioSchema); err != nil {
			return fmt.Errorf("failed to write io_schema.json: %w", err)
		}
	}
	if readme != "" {
		if err := utils.WriteWorkflowReadme(baseDir, workflow.ProjectID, workflow.WorkflowID, readme); err != nil {
			return fmt.Errorf("failed to write README.md: %w", err)
		}
	}
	return nil
}

// regenerateWorkflowJSON 重新生成 workflow.json（含脚本快照），保持导出文件与库一致。
//
// 失败不阻塞主流程：返回一条 warning 让模型知道需要用户在 UI 侧重新保存一次；
// 组件内容本身已经落盘 / 落库，不影响后续运行。
func (m *workflowManager) regenerateWorkflowJSON(ctx context.Context, workflow *types.Workflow, baseDir string) string {
	if m.codecs == nil {
		return "workflow.json 未重新生成：导出 codec 未装配，请在 UI 侧保存一次工作流"
	}
	codec := m.codecs.Get(exportcodec.CurrentVersion)
	if codec == nil {
		return "workflow.json 未重新生成：导出 codec 版本未注册，请在 UI 侧保存一次工作流"
	}
	request := exportcodec.WorkflowWriteRequest{
		WorkflowPK:  workflow.ID,
		ProjectID:   workflow.ProjectID,
		BaseDir:     baseDir,
		WorkflowDir: utils.GetWorkflowFileDir(baseDir, workflow.ProjectID, workflow.WorkflowID),
	}
	if _, err := codec.WriteWorkflowFiles(ctx, request); err != nil {
		return fmt.Sprintf("workflow.json 重新生成失败（组件已保存，可在 UI 侧再保存一次）：%v", err)
	}
	return ""
}

// brief 构造工作流的只读摘要（含 DAG 节点 / 连线数）。
func (m *workflowManager) brief(baseDir string, workflow *types.Workflow) runtimecontext.WorkflowBrief {
	files := utils.WorkflowLayout(baseDir, workflow.ProjectID, workflow.WorkflowID)
	brief := runtimecontext.WorkflowBrief{
		WorkflowID:        workflow.ID,
		WorkflowKey:       workflow.WorkflowID,
		Name:              workflow.Name,
		Description:       workflow.Description,
		Category:          workflow.Category,
		ProjectID:         workflow.ProjectID,
		WorkflowDir:       files.Dir,
		DagDefinitionPath: files.DagDefinition,
		IOSchemaPath:      files.IOSchemaFile,
		ReadmeFile:        files.ReadmeFile,
	}
	if dagDefinition, err := utils.ReadWorkflowDagDefinition(baseDir, workflow.ProjectID, workflow.WorkflowID); err == nil {
		nodes, _ := dagDefinition["nodes"].([]any)
		edges, _ := dagDefinition["edges"].([]any)
		brief.NodeCount = len(nodes)
		brief.EdgeCount = len(edges)
	}
	return brief
}

// storageBaseDir 返回存储根目录（与其它 agent 能力的口径一致）。
func (m *workflowManager) storageBaseDir() string {
	if m.cfg == nil || m.cfg.Storage == nil {
		return ""
	}
	return strings.TrimSpace(m.cfg.Storage.BaseDir)
}

// validateJSONObject 校验非空内容必须是合法 JSON（空串表示「不提供」）。
func validateJSONObject(field, content string) error {
	if content == "" {
		return nil
	}
	if !json.Valid([]byte(content)) {
		return fmt.Errorf("%s is not valid JSON", field)
	}
	return nil
}
