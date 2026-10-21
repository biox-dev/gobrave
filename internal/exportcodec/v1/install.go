package v1

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/biox-dev/gobrave/internal/exportcodec"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/utils"
)

// InstallScript 解析 req.Raw（内部调用 DecodeScript）并把导出内容持久化到数据库。
//
// 迁移自 handler.InstallScript 的落库部分，职责边界与 DecodeScript 一致：
// DecodeScript 只解析，本方法负责安装特有的校验（script_id 非空）与 upsert。
// 返回的 payload 只用于取字段，不再回传给调用方做二次解析。
func (c *Codec) InstallScript(ctx context.Context, req exportcodec.ScriptInstallRequest) (*exportcodec.ScriptInstallResult, error) {
	if c.workflowService == nil {
		return nil, fmt.Errorf("exportcodec/v1: workflow service is not configured")
	}

	payload, err := c.DecodeScript(req.Raw)
	if err != nil {
		return nil, fmt.Errorf("invalid %s: %w", exportcodec.ScriptJSONFileName, err)
	}
	if strings.TrimSpace(payload.ScriptID) == "" {
		return nil, exportcodec.ErrScriptIDRequired
	}

	// 先导入容器运行配置与绑定行：脚本通过 container_template_id 引用绑定行，
	// 绑定行的 spec_id 引用运行配置，按主键 upsert 后这些引用在安装到当前 project 后依旧成立
	// （见 installContainerAssets）。
	installedSpecCount, installedDefinitionCount, assetErr := c.installContainerAssets(
		ctx, payload.ContainerTemplateSpecs, payload.ContainerTemplateDefinitions)
	if assetErr != nil {
		return nil, fmt.Errorf("failed to install container templates: %w", assetErr)
	}

	scriptBytes, err := json.Marshal(payload.Script)
	if err != nil {
		return nil, fmt.Errorf("failed to decode script body: %w", err)
	}
	installScript := &types.Script{}
	if err := json.Unmarshal(scriptBytes, installScript); err != nil {
		return nil, fmt.Errorf("failed to parse script body: %w", err)
	}
	// 兼容历史导出文件：旧版 script.json 的名称键是 component_name，新版是 name。
	// 不兜底的话，重装旧产物会把已有脚本的名称写成空串。
	if installScript.Name == "" {
		installScript.Name = scriptNameFromExportMap(payload.Script)
	}

	installScript.ID = 0
	installScript.ScriptID = req.ScriptID
	installScript.ProjectID = req.ProjectID
	installScript.StoreID = req.StoreID
	if installScript.ComponentType == "" {
		installScript.ComponentType = "script"
	}
	// if strings.TrimSpace(req.StoreMessage) != "" {
	// 	installScript.Message = req.StoreMessage
	// }
	installScript.CreatedAt = utils.GetCurrentTime()
	installScript.UpdatedAt = utils.GetCurrentTime()

	if req.CreateMode {
		installScript.Name = fmt.Sprintf("%s_Copy", installScript.Name)
		installScript.StoreID = 0
		if err := c.workflowService.CreateScript(ctx, installScript); err != nil {
			return nil, fmt.Errorf("failed to install script: %w", err)
		}
	} else {
		existingScript, err := c.workflowService.ExistsScriptInProjectByScriptID(ctx, req.ProjectID, req.ScriptID)
		if err != nil {
			return nil, fmt.Errorf("failed to check existing script: %w", err)
		}

		if existingScript != nil {
			installScript.ID = existingScript.ID
			if err := c.workflowService.UpdateScript(ctx, installScript); err != nil {
				return nil, fmt.Errorf("failed to update installed script: %w", err)
			}
		} else {
			if err := c.workflowService.CreateScript(ctx, installScript); err != nil {
				return nil, fmt.Errorf("failed to install script: %w", err)
			}
		}
	}

	return &exportcodec.ScriptInstallResult{
		ScriptID:                         installScript.ScriptID,
		InstalledScriptID:                installScript.ID,
		ContainerTemplateSpecCount:       installedSpecCount,
		ContainerTemplateDefinitionCount: installedDefinitionCount,
	}, nil
}

// InstallWorkflow 解析 req.Raw（内部调用 DecodeWorkflow）并把导出内容持久化到数据库与文件系统。
//
// 迁移自 handler.InstallWorkflow 的落库与脚本还原部分：
//  1. 先按主键 upsert 容器资产（运行配置 → 绑定行，见 installContainerAssets）；
//  2. upsert workflow 行；
//  3. 逐条 upsert workflow.json 里的 script 行；
//  4. 把 workflow 目录内的脚本快照还原到本地脚本目录。
//
// workflow_id 缺失（导出文件损坏）返回 ErrWorkflowIDRequired，由调用方转 400。
func (c *Codec) InstallWorkflow(ctx context.Context, req exportcodec.WorkflowInstallRequest) (*exportcodec.WorkflowInstallResult, error) {
	if c.workflowService == nil {
		return nil, fmt.Errorf("exportcodec/v1: workflow service is not configured")
	}

	payload, err := c.DecodeWorkflow(req.Raw)
	if err != nil {
		return nil, fmt.Errorf("invalid %s: %w", exportcodec.WorkflowJSONFileName, err)
	}
	if strings.TrimSpace(payload.WorkflowID) == "" {
		return nil, exportcodec.ErrWorkflowIDRequired
	}

	// 先导入容器运行配置与绑定行（顺序见 installContainerAssets）。
	installedSpecCount, installedDefinitionCount, assetErr := c.installContainerAssets(
		ctx, payload.ContainerTemplateSpecs, payload.ContainerTemplateDefinitions)
	if assetErr != nil {
		return nil, fmt.Errorf("failed to install container templates: %w", assetErr)
	}

	wfBytes, err := json.Marshal(payload.Workflow)
	if err != nil {
		return nil, fmt.Errorf("failed to decode workflow body: %w", err)
	}
	installWorkflow := &types.Workflow{}
	if err := json.Unmarshal(wfBytes, installWorkflow); err != nil {
		return nil, fmt.Errorf("failed to parse workflow body: %w", err)
	}

	installWorkflow.ID = 0
	installWorkflow.ProjectID = req.ProjectID
	installWorkflow.StoreID = req.StoreID
	// if strings.TrimSpace(req.StoreMessage) != "" {
	// 	installWorkflow.Message = req.StoreMessage
	// }
	// 修改创建时间为当前时间，避免覆盖原有的创建时间
	installWorkflow.CreatedAt = utils.GetCurrentTime()
	installWorkflow.UpdatedAt = utils.GetCurrentTime()

	existingWorkflow, err := c.workflowService.ExistsWorkflowInProjectByWorkflowID(ctx, req.ProjectID, payload.WorkflowID)
	if err != nil {
		return nil, fmt.Errorf("failed to check existing workflow: %w", err)
	}
	if existingWorkflow != nil {
		installWorkflow.ID = existingWorkflow.ID
		if err := c.workflowService.UpdateWorkflow(ctx, installWorkflow); err != nil {
			return nil, fmt.Errorf("failed to update installed workflow: %w", err)
		}
	} else {
		if err := c.workflowService.CreateWorkflow(ctx, installWorkflow); err != nil {
			return nil, fmt.Errorf("failed to install workflow: %w", err)
		}
	}

	// dag_definition 不再是数据库字段：安装时把它写回工作流目录的 dag_definition.json，
	// 与导出侧（utils.WriteWorkflowDagDefinition）口径一致 —— DecodeWorkflow 已把对象规整为字符串。
	if rawDag, ok := payload.Workflow["dag_definition"]; ok {
		if dagStr, ok := rawDag.(string); ok && strings.TrimSpace(dagStr) != "" {
			if err := utils.WriteWorkflowDagDefinition(req.BaseDir, req.ProjectID, payload.WorkflowID, dagStr); err != nil {
				return nil, fmt.Errorf("failed to write workflow dag_definition file: %w", err)
			}
		}
	}

	installedScriptCount := 0
	for _, scriptMap := range payload.Scripts {
		scriptBytes, marshalErr := json.Marshal(scriptMap)
		if marshalErr != nil {
			return nil, fmt.Errorf("failed to decode script body: %w", marshalErr)
		}
		installScript := &types.Script{}
		if unmarshalErr := json.Unmarshal(scriptBytes, installScript); unmarshalErr != nil {
			return nil, fmt.Errorf("failed to parse script body: %w", unmarshalErr)
		}
		// 同 InstallScript：兼容旧版 workflow.json 快照里的 component_name 键。
		if installScript.Name == "" {
			installScript.Name = scriptNameFromExportMap(scriptMap)
		}

		installScript.ID = 0
		installScript.ProjectID = req.ProjectID
		installScript.StoreID = req.StoreID
		if installScript.ComponentType == "" {
			installScript.ComponentType = "script"
		}

		existingScript, err := c.workflowService.ExistsScriptInProjectByScriptID(ctx, req.ProjectID, installScript.ScriptID)
		if err != nil {
			return nil, fmt.Errorf("failed to check existing script: %w", err)
		}

		if existingScript != nil {
			installScript.ID = existingScript.ID
			if err := c.workflowService.UpdateScript(ctx, installScript); err != nil {
				return nil, fmt.Errorf("failed to update installed script: %w", err)
			}
		} else {
			if err := c.workflowService.CreateScript(ctx, installScript); err != nil {
				return nil, fmt.Errorf("failed to install script: %w", err)
			}
		}

		scriptID := strings.TrimSpace(installScript.ScriptID)
		if scriptID == "" {
			scriptID = c.ScriptIDFromExportScript(scriptMap)
		}
		if scriptID != "" {
			// 脚本文件随 workflow 目录一起从 store 同步到该版本约定的快照目录
			// （v1 为 <workflowDir>/script/<scriptID>），这里再原样还原到脚本目录。
			sourceScriptDir := c.ScriptSnapshotDir(req.WorkflowDir, scriptID)
			targetScriptDir := utils.GetScriptFileDir(req.BaseDir, req.ProjectID, scriptID)
			if copyErr := utils.CopyDirReplace(sourceScriptDir, targetScriptDir); copyErr != nil {
				return nil, fmt.Errorf("failed to install script files: %w", copyErr)
			}
		}

		installedScriptCount++
	}

	return &exportcodec.WorkflowInstallResult{
		WorkflowID:                       payload.WorkflowID,
		InstalledWorkflowID:              installWorkflow.ID,
		InstalledScriptCount:             installedScriptCount,
		ContainerTemplateSpecCount:       installedSpecCount,
		ContainerTemplateDefinitionCount: installedDefinitionCount,
	}, nil
}

// installContainerAssets 按导出内容创建/更新容器运行配置与绑定行，返回各自处理的数量。
//
// 导出格式把容器资产拆成两个平铺列表（container_template_specs /
// container_template_definitions），两者通过主键互相引用：绑定行 spec_id 指向运行配置，
// 而绑定行主键就是脚本 container_template_id 引用的值；镜像名与拉取策略直接存在绑定行上。
// 具体安装逻辑（upsert 顺序、id 校验、按主键新增/更新）由 exportcodec.InstallContainerAssets
// 统一实现，script.json 与 workflow.json 共用同一份代码。
func (c *Codec) installContainerAssets(ctx context.Context, specs []types.ContainerTemplateSpec, definitions []types.ContainerTemplateDefinition) (int, int, error) {
	result, err := exportcodec.InstallContainerAssets(ctx, c.containerService, specs, definitions)
	if err != nil {
		return 0, 0, err
	}
	return result.SpecCount, result.DefinitionCount, nil
}

// scriptNameFromExportMap 从导出文件的 script map 里取名称。
// 新版导出写 name（types.Script 的 json tag），旧版写 component_name（数据库列名），
// 两个键都尝试，保证重装历史产物时不会把脚本名称清空。
func scriptNameFromExportMap(script map[string]any) string {
	for _, key := range []string{"name", "component_name"} {
		if value, ok := script[key].(string); ok && strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
