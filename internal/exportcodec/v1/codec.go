// Package v1 实现导出文件格式 v1（exportcodec.VersionV1）的 Codec 策略。
//
// v1 的格式约定：
//
//   - script.json   = version + script_id + script + container_template_specs
//   - container_template_definitions + container_images；
//   - workflow.json = version + workflow_id + workflow + scripts + container_template_specs
//   - container_template_definitions + container_images；
//     （三个容器资产列表均按主键去重：绑定行通过 spec_id 引用运行配置、通过 image_id 引用镜像，
//     绑定行主键就是脚本 container_template_id 引用的值）
//   - workflow 目录同时把引用的脚本目录快照到 <workflowDir>/script/<scriptID>，
//     且排除脚本目录自身的 .git（否则会把脚本仓库塞进 workflow 仓库）。
//
// 本包同时负责落盘后的 git 提交：WriteXxxFiles 写完文件（以及 workflow 的脚本快照）后
// 调 utils.CommitDirChanges 把目录改动提交为一个 commit，提交身份由装配时注入。
// Codec 自身无状态（只持有无状态的 service 依赖与不变的提交身份），可并发使用。
package v1

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/biox-dev/gobrave/internal/exportcodec"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"github.com/biox-dev/gobrave/internal/utils"
)

// ScriptSnapshotDirName 是 v1 布局下 workflow 目录内快照脚本的子目录名：
// <workflowDir>/script/<scriptID>。安装侧（InstallWorkflow）从同一位置还原脚本目录，
// 因此两边必须用本常量而不是各自写字面量。
const ScriptSnapshotDirName = "script"

// Codec 是 v1 格式的策略实现。
//
// 直接持有 WorkflowService：写侧用它按主键生成导出 payload，安装侧用它 upsert
// workflow / script 行（读侧的 DecodeXxx 不会用到）。
// containerService 只在安装侧使用：按主键 upsert 容器镜像 / 运行配置 / 绑定行。
// gitIdentity 是落盘后提交使用的身份，装配时由 config.ResolveGitIdentity 解析后注入。
type Codec struct {
	workflowService  interfaces.WorkflowService
	containerService interfaces.ContainerService
	gitIdentity      utils.GitIdentity
}

var _ exportcodec.Codec = (*Codec)(nil)

// NewCodec 构造 v1 Codec。gitIdentity 用于 WriteXxxFiles 落盘后的 git 提交；
// containerService 用于 InstallXxx 落库容器资产（不需要时可为 nil）。
func NewCodec(workflowService interfaces.WorkflowService, containerService interfaces.ContainerService, gitIdentity utils.GitIdentity) *Codec {
	return &Codec{workflowService: workflowService, containerService: containerService, gitIdentity: gitIdentity}
}

// Version 返回 v1 的格式版本号。
func (c *Codec) Version() string { return exportcodec.VersionV1 }

// ScriptSnapshotDir 返回 v1 布局下 workflow 目录内承载某个脚本快照的目录。
func ScriptSnapshotDir(workflowDir, scriptID string) string {
	return filepath.Join(workflowDir, ScriptSnapshotDirName, scriptID)
}

// ScriptSnapshotDir 让 v1 的目录布局可以通过 Codec 接口访问
// （安装侧从 Registry 取到 Codec 后据此还原脚本目录）。
func (c *Codec) ScriptSnapshotDir(workflowDir, scriptID string) string {
	return ScriptSnapshotDir(workflowDir, scriptID)
}

// ScriptIDFromExportScript 从导出 payload 的脚本条目里取脚本 ID。
//
// v1 落库列是 component_id（types.Script.ScriptID 的 JSON tag），
// 同时兼容 script_id 是为了让后续格式演进时读侧不必立刻改。
func ScriptIDFromExportScript(item map[string]any) string {
	if item == nil {
		return ""
	}
	if v, ok := item["component_id"].(string); ok {
		return strings.TrimSpace(v)
	}
	if v, ok := item["script_id"].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

// ScriptIDFromExportScript 让 v1 的脚本条目字段含义可以通过 Codec 接口访问。
func (c *Codec) ScriptIDFromExportScript(item map[string]any) string {
	return ScriptIDFromExportScript(item)
}

// WriteScriptFiles 生成 v1 格式的 script.json 落盘到 req.ScriptDir，并把目录改动提交为一个 commit。
func (c *Codec) WriteCommitScriptFiles(ctx context.Context, req exportcodec.ScriptWriteRequest) (*types.ScriptJSONExportResponse, error) {
	if c.workflowService == nil {
		return nil, fmt.Errorf("exportcodec/v1: workflow service is not configured")
	}

	payload, err := c.workflowService.GenerateScriptJSONByScriptID(ctx, req.ScriptPK)
	if err != nil {
		return nil, fmt.Errorf("failed to generate script json: %w", err)
	}
	payload.Version = c.Version()

	if err := utils.WriteJSONFile(filepath.Join(req.ScriptDir, exportcodec.ScriptJSONFileName), payload); err != nil {
		return nil, fmt.Errorf("failed to write script json: %w", err)
	}
	if err := utils.CommitDirChanges(req.ScriptDir, req.CommitMessage, c.gitIdentity); err != nil {
		return nil, fmt.Errorf("failed to commit script files: %w", err)
	}
	return payload, nil
}

// WriteCommmitWorkflowFiles 生成 v1 格式的 workflow.json 落盘到 req.WorkflowDir，
// 把 workflow 引用的脚本目录快照到 <workflowDir>/script/<scriptID>（排除脚本自身的 .git），
// 最后把目录改动提交为一个 commit。
func (c *Codec) WriteCommmitWorkflowFiles(ctx context.Context, req exportcodec.WorkflowWriteRequest) (*types.WorkflowJSONExportResponse, error) {
	if c.workflowService == nil {
		return nil, fmt.Errorf("exportcodec/v1: workflow service is not configured")
	}
	if strings.TrimSpace(req.BaseDir) == "" {
		return nil, fmt.Errorf("exportcodec/v1: storage base dir is empty")
	}

	payload, err := c.workflowService.GenerateWorkflowJSONByWorkflowID(ctx, req.WorkflowPK, req.BaseDir)
	if err != nil {
		return nil, fmt.Errorf("failed to generate workflow json: %w", err)
	}
	payload.Version = c.Version()

	if err := utils.WriteJSONFile(filepath.Join(req.WorkflowDir, exportcodec.WorkflowJSONFileName), payload); err != nil {
		return nil, fmt.Errorf("failed to write workflow json: %w", err)
	}

	// workflow.json 只描述脚本元数据，脚本文件本身快照到 script/<scriptID>，
	// 这样 store 仓库同时携带 workflow 与它引用的脚本，安装时可直接还原。
	for _, scriptItem := range payload.Scripts {
		scriptID := ScriptIDFromExportScript(scriptItem)
		if scriptID == "" {
			continue
		}
		sourceScriptDir := utils.GetScriptFileDir(req.BaseDir, req.ProjectID, scriptID)
		targetScriptDir := ScriptSnapshotDir(req.WorkflowDir, scriptID)
		if err := utils.CopyDirReplace(sourceScriptDir, targetScriptDir, ".git"); err != nil {
			return nil, fmt.Errorf("failed to snapshot script %s: %w", scriptID, err)
		}
	}

	if err := utils.CommitDirChanges(req.WorkflowDir, req.CommitMessage, c.gitIdentity); err != nil {
		return nil, fmt.Errorf("failed to commit workflow files: %w", err)
	}
	return payload, nil
}

// DecodeScript 解析 v1 格式的 script.json。
func (c *Codec) DecodeScript(raw []byte) (*types.ScriptJSONExportResponse, error) {
	payload := &types.ScriptJSONExportResponse{}
	if err := json.Unmarshal(raw, payload); err != nil {
		return nil, err
	}
	return payload, nil
}

// DecodeWorkflow 解析 v1 格式的 workflow.json。
//
// v1 在此把 workflow.dag_definition 从对象规整成字符串：导出侧会把它展开成对象，
// 而落库列是字符串，直接反序列化到 types.Workflow 会失败。
func (c *Codec) DecodeWorkflow(raw []byte) (*types.WorkflowJSONExportResponse, error) {
	payload := &types.WorkflowJSONExportResponse{}
	if err := json.Unmarshal(raw, payload); err != nil {
		return nil, err
	}
	if err := normalizeWorkflowDagDefinition(payload.Workflow); err != nil {
		return nil, err
	}
	return payload, nil
}

// normalizeWorkflowDagDefinition 把 workflow map 里的 dag_definition 规整为字符串。
//
// 已经是字符串（或字段缺失/为 nil）时保持不变。
func normalizeWorkflowDagDefinition(workflow map[string]any) error {
	if workflow == nil {
		return nil
	}

	dagDefinition, exists := workflow["dag_definition"]
	if !exists || dagDefinition == nil {
		return nil
	}
	if _, ok := dagDefinition.(string); ok {
		return nil
	}

	b, err := json.Marshal(dagDefinition)
	if err != nil {
		return err
	}
	workflow["dag_definition"] = string(b)
	return nil
}
