package agentscript

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	agenttypes "github.com/biox-dev/gobrave/internal/agent/types"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/utils"
	"gorm.io/gorm"
)

// 本文件是 scriptCreator 的「更新脚本基本信息」能力（runtimecontext.ScriptUpdater 的实现）。
//
// 与创建（CreateScriptWithNode）共享同一份业务服务依赖，因此挂在同一个类型上：
// 两个接口的区分只体现在工具层（create_script / update_script），上层注入一次即可。
//
// 只改元数据，不碰脚本内容 / io_schema：
//   - 按 script_id（主键）定位脚本，并校验其属于当前激活项目；
//   - 以库中完整记录为基准，只覆盖工具给出的字段，再整体回写
//     （workflowRepository.UpdateScript 是「整体替换」语义，不能只构造部分字段的对象）。
//
// 容器模板（container_template_id）只改脚本绑定行：脚本节点运行期沿用脚本绑定的模板
// （见 dag/executor.ContainerExecutor），因此无需级联修改已创建的分析节点。
//
// 与创建脚本一样，更新不做 git 提交：脚本目录中的版本提交仍由用户通过发布入口触发。

// UpdateScriptInfo 更新脚本的 name / description / container_template_id，返回更新后的关键信息。
func (c *scriptCreator) UpdateScriptInfo(ctx context.Context, in runtimecontext.ScriptUpdateInput) (runtimecontext.ScriptUpdateResult, error) {
	if in.ProjectID <= 0 {
		return runtimecontext.ScriptUpdateResult{}, fmt.Errorf("update_script: project_id is required")
	}
	if c.workflowSvc == nil {
		return runtimecontext.ScriptUpdateResult{}, fmt.Errorf("update_script: required services are not configured")
	}
	fileEdit := in.ResourceFileEdit()
	if in.Name == nil && in.Description == nil && in.ContainerTemplateID == nil && fileEdit.IsZero() {
		return runtimecontext.ScriptUpdateResult{}, fmt.Errorf("update_script: at least one of name / description / container_template_id / file edit is required")
	}
	if in.Name != nil && strings.TrimSpace(*in.Name) == "" {
		return runtimecontext.ScriptUpdateResult{}, fmt.Errorf("update_script: name must not be empty")
	}
	if in.ContainerTemplateID != nil && *in.ContainerTemplateID <= 0 {
		return runtimecontext.ScriptUpdateResult{}, fmt.Errorf("update_script: container_template_id must be a positive integer")
	}

	// 1) 定位脚本：按主键定位，并与当前激活项目校验。
	script, err := c.locateScript(ctx, in)
	if err != nil {
		return runtimecontext.ScriptUpdateResult{}, err
	}

	// 2) 以库中完整记录为基准，只覆盖给出的字段（整体替换语义）。
	changed := make([]string, 0, 4)
	if in.Name != nil {
		name := strings.TrimSpace(*in.Name)
		if name != script.Name {
			changed = append(changed, "name")
		}
		script.Name = name
	}
	if in.Description != nil {
		description := strings.TrimSpace(*in.Description)
		if description != script.Description {
			changed = append(changed, "description")
		}
		script.Description = description
	}
	containerTemplateName := ""
	if in.ContainerTemplateID != nil {
		templateID := *in.ContainerTemplateID
		if err := c.ensureContainerTemplateExists(ctx, templateID); err != nil {
			return runtimecontext.ScriptUpdateResult{}, err
		}
		if templateID != script.ContainerTemplateID {
			changed = append(changed, "container_template_id")
		}
		script.ContainerTemplateID = templateID
	}
	// 回传容器模板展示名：仅在本次显式指定模板时查询，避免无谓的额外读库。
	if in.ContainerTemplateID != nil && c.containerRepo != nil {
		if tpl, err := c.containerRepo.GetContainerTemplateByID(ctx, script.ContainerTemplateID); err == nil {
			containerTemplateName = containerTemplateDisplayName(tpl)
		}
	}

	baseDir := c.storageBaseDir()
	// 2.1) 文件编辑（script_file / io_schema / readme）：字段覆盖或唯一文本替换。
	if !fileEdit.IsZero() {
		if baseDir == "" {
			return runtimecontext.ScriptUpdateResult{}, fmt.Errorf("update_script: storage.base_dir is empty")
		}
		if err := c.applyFileEdit(baseDir, script, fileEdit); err != nil {
			return runtimecontext.ScriptUpdateResult{}, err
		}
		changed = append(changed, "file:"+string(fileEdit.File))
	}

	if err := c.workflowSvc.UpdateScript(ctx, script); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return runtimecontext.ScriptUpdateResult{}, fmt.Errorf("update_script: script %d not found", script.ID)
		}
		return runtimecontext.ScriptUpdateResult{}, fmt.Errorf("update_script: failed to update script: %w", err)
	}

	// 记录「当前会话修改了该脚本」：别的会话修改同一脚本时会各记一条关联。
	if c.resources != nil {
		_ = c.resources.LinkFromContext(ctx, agenttypes.SessionResourceTypeScript, script.ID)
	}

	// 3) 回传脚本在磁盘上的关键路径，便于模型继续读写脚本内容。
	result := runtimecontext.ScriptUpdateResult{
		ScriptID:              script.ID,
		ScriptName:            script.Name,
		Description:           script.Description,
		ScriptType:            script.ScriptType,
		ContainerTemplateID:   script.ContainerTemplateID,
		ContainerTemplateName: containerTemplateName,
		Changed:               changed,
	}
	if baseDir != "" {
		files := script.Files(baseDir)
		result.ScriptFilePath = files.MainPath
		result.IOSchemaPath = files.IOSchemaFile
	}
	return result, nil
}

// ensureContainerTemplateExists 校验容器模板存在；未注入 containerRepo 时跳过校验。
//
// 与 update_analysis_node_agent 同口径：模板 ID 是全局资产，不属于项目，
// 因此只校验存在性，不做项目归属校验。
func (c *scriptCreator) ensureContainerTemplateExists(ctx context.Context, templateID int64) error {
	if c.containerRepo == nil {
		return nil
	}
	tpl, err := c.containerRepo.GetContainerTemplateByID(ctx, templateID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("update_script: container template %d not found", templateID)
		}
		return fmt.Errorf("update_script: failed to get container template %d: %w", templateID, err)
	}
	if tpl == nil || tpl.ID == 0 {
		return fmt.Errorf("update_script: container template %d not found", templateID)
	}
	return nil
}

// applyFileEdit 读取脚本目录下的目标文件、应用编辑并落盘。
func (c *scriptCreator) applyFileEdit(baseDir string, script *types.Script, edit runtimecontext.ResourceFileEdit) error {
	var (
		before []byte
		err    error
	)
	switch edit.File {
	case runtimecontext.ResourceFileScript:
		before, err = utils.ReadScriptFileContent(baseDir, script.ProjectID, script.ScriptType, script.ScriptID)
	case runtimecontext.ResourceFileIOSchema:
		before, err = utils.ReadScriptIOSchemaFile(baseDir, script.ProjectID, script.ScriptID)
	case runtimecontext.ResourceFileReadme:
		before, err = utils.ReadScriptReadme(baseDir, script.ProjectID, script.ScriptID)
	default:
		return fmt.Errorf("update_script: unsupported file %q (expected script_file / io_schema / readme)", edit.File)
	}
	if err != nil {
		return fmt.Errorf("update_script: failed to read %s: %w", edit.File, err)
	}

	after, err := edit.Apply(string(before))
	if err != nil {
		return fmt.Errorf("update_script: %w", err)
	}

	switch edit.File {
	case runtimecontext.ResourceFileScript:
		err = utils.WriteScriptFileContent(baseDir, script.ProjectID, script.ScriptType, script.ScriptID, after)
	case runtimecontext.ResourceFileIOSchema:
		err = utils.WriteScriptIOSchema(baseDir, script.ProjectID, script.ScriptID, after)
	case runtimecontext.ResourceFileReadme:
		err = utils.WriteScriptReadme(baseDir, script.ProjectID, script.ScriptID, after)
	}
	if err != nil {
		return fmt.Errorf("update_script: failed to write %s: %w", edit.File, err)
	}
	return nil
}

// locateScript 按主键定位脚本，并校验其属于当前激活项目。
//
// 项目校验是必须的：RuntimeContext.ProjectID 代表用户当前激活项目，若脚本属于别的项目，
// 直接报错而不是静默改写，避免模型越权修改其他项目的数据。
func (c *scriptCreator) locateScript(ctx context.Context, in runtimecontext.ScriptUpdateInput) (*types.Script, error) {
	if in.ScriptID <= 0 {
		return nil, fmt.Errorf("update_script: script_id is required")
	}
	script, err := c.workflowSvc.GetScriptByID(ctx, in.ScriptID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("update_script: script not found in the active project")
		}
		return nil, fmt.Errorf("update_script: failed to load script: %w", err)
	}
	if script == nil || script.ID == 0 {
		return nil, fmt.Errorf("update_script: script not found in the active project")
	}
	if script.ProjectID != in.ProjectID {
		return nil, fmt.Errorf("update_script: script %d does not belong to the active project %d", script.ID, in.ProjectID)
	}
	return script, nil
}
