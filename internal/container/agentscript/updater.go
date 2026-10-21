package container

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/tool"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/utils"
	"gorm.io/gorm"
)

// 本文件是 scriptCreator 的「更新脚本基本信息」能力（tool.ScriptUpdater 的实现）。
//
// 与创建（CreateScriptWithNode）共享同一份业务服务依赖，因此挂在同一个类型上：
// 两个接口的区分只体现在工具层（create_script / update_script），上层注入一次即可。
//
// 只改元数据，不碰脚本内容 / io_schema / 容器模板：
//   - 按 script_id（主键）定位脚本，并校验其属于当前激活项目；
//   - 以库中完整记录为基准，只覆盖工具给出的字段，再整体回写
//     （workflowRepository.UpdateScript 是「整体替换」语义，不能只构造部分字段的对象）。
//
// 与创建脚本一样，更新不做 git 提交：脚本目录中的版本提交仍由用户通过发布入口触发。

// UpdateScriptInfo 更新脚本的 name / description，返回更新后的关键信息。
func (c *scriptCreator) UpdateScriptInfo(ctx context.Context, in tool.ScriptUpdateInput) (tool.ScriptUpdateResult, error) {
	if in.ProjectID <= 0 {
		return tool.ScriptUpdateResult{}, fmt.Errorf("update_script: project_id is required")
	}
	if c.workflowSvc == nil {
		return tool.ScriptUpdateResult{}, fmt.Errorf("update_script: required services are not configured")
	}
	if in.Name == nil && in.Description == nil {
		return tool.ScriptUpdateResult{}, fmt.Errorf("update_script: at least one of name / description is required")
	}
	if in.Name != nil && strings.TrimSpace(*in.Name) == "" {
		return tool.ScriptUpdateResult{}, fmt.Errorf("update_script: name must not be empty")
	}

	// 1) 定位脚本：按主键定位，并与当前激活项目校验。
	script, err := c.locateScript(ctx, in)
	if err != nil {
		return tool.ScriptUpdateResult{}, err
	}

	// 2) 以库中完整记录为基准，只覆盖给出的字段（整体替换语义）。
	changed := make([]string, 0, 2)
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

	if err := c.workflowSvc.UpdateScript(ctx, script); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return tool.ScriptUpdateResult{}, fmt.Errorf("update_script: script %d not found", script.ID)
		}
		return tool.ScriptUpdateResult{}, fmt.Errorf("update_script: failed to update script: %w", err)
	}

	// 3) 回传脚本在磁盘上的关键路径，便于模型继续读写脚本内容。
	result := tool.ScriptUpdateResult{
		ScriptID:    script.ID,
		ScriptName:  script.Name,
		Description: script.Description,
		ScriptType:  script.ScriptType,
		Changed:     changed,
	}
	baseDir := c.storageBaseDir()
	if baseDir != "" {
		scriptDir, scriptFile, _ := utils.GetScriptFile(baseDir, script.ProjectID, script.ScriptType, script.ScriptID)
		result.ScriptFilePath = filepath.Join(scriptDir, scriptFile)
		result.IOSchemaPath = utils.ScriptIOSchemaPath(baseDir, script.ProjectID, script.ScriptID)
	}
	return result, nil
}

// locateScript 按主键定位脚本，并校验其属于当前激活项目。
//
// 项目校验是必须的：RuntimeContext.ProjectID 代表用户当前激活项目，若脚本属于别的项目，
// 直接报错而不是静默改写，避免模型越权修改其他项目的数据。
func (c *scriptCreator) locateScript(ctx context.Context, in tool.ScriptUpdateInput) (*types.Script, error) {
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
