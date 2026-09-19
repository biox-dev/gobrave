package v1

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/biox-dev/gobrave/internal/exportcodec"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/utils"
	"gorm.io/gorm"
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

	// 先导入容器镜像、运行配置与绑定行：脚本通过 container_template_id 引用绑定行，
	// 绑定行的 spec_id / image_id 分别引用运行配置与镜像，按主键 upsert 后这些引用在
	// 安装到当前 project 后依旧成立（见 installContainerAssets）。
	installedImageCount, installedSpecCount, installedDefinitionCount, assetErr := c.installContainerAssets(
		ctx, payload.ContainerImages, payload.ContainerTemplateSpecs, payload.ContainerTemplateDefinitions)
	if assetErr != nil {
		return nil, fmt.Errorf("failed to install container images or templates: %w", assetErr)
	}

	scriptBytes, err := json.Marshal(payload.Script)
	if err != nil {
		return nil, fmt.Errorf("failed to decode script body: %w", err)
	}
	installScript := &types.Script{}
	if err := json.Unmarshal(scriptBytes, installScript); err != nil {
		return nil, fmt.Errorf("failed to parse script body: %w", err)
	}

	installScript.ID = 0
	installScript.ScriptID = req.ScriptID
	installScript.ProjectID = req.ProjectID
	installScript.StoreID = req.StoreID
	if installScript.ComponentType == "" {
		installScript.ComponentType = "script"
	}
	// if strings.TrimSpace(req.StoreURL) != "" {
	// 	installScript.URL = req.StoreURL
	// }
	// if strings.TrimSpace(req.StoreMessage) != "" {
	// 	installScript.Message = req.StoreMessage
	// }
	installScript.CreatedAt = utils.GetCurrentTime()
	installScript.UpdatedAt = utils.GetCurrentTime()

	if req.CreateMode {
		installScript.ComponentName = fmt.Sprintf("%s_Copy", installScript.ComponentName)
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
		ContainerImageCount:              installedImageCount,
		ContainerTemplateSpecCount:       installedSpecCount,
		ContainerTemplateDefinitionCount: installedDefinitionCount,
	}, nil
}

// InstallWorkflow 解析 req.Raw（内部调用 DecodeWorkflow）并把导出内容持久化到数据库与文件系统。
//
// 迁移自 handler.InstallWorkflow 的落库与脚本还原部分：
//  1. 先按主键 upsert 容器资产（镜像 → 运行配置 → 绑定行，见 installContainerAssets）；
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

	// 先导入容器镜像、运行配置与绑定行：脚本通过 container_template_id 引用绑定行，
	// 绑定行的 spec_id / image_id 分别引用运行配置与镜像，按主键 upsert 后这些引用在
	// 安装到当前 project 后依旧成立（见 installContainerAssets）。
	installedImageCount, installedSpecCount, installedDefinitionCount, assetErr := c.installContainerAssets(
		ctx, payload.ContainerImages, payload.ContainerTemplateSpecs, payload.ContainerTemplateDefinitions)
	if assetErr != nil {
		return nil, fmt.Errorf("failed to install container images or templates: %w", assetErr)
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
	// if strings.TrimSpace(req.StoreURL) != "" {
	// 	installWorkflow.URL = req.StoreURL
	// }
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
			targetScriptDir := utils.GetScriptFileDir(req.BaseDir, req.ProjectCode, scriptID)
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
		ContainerImageCount:              installedImageCount,
		ContainerTemplateSpecCount:       installedSpecCount,
		ContainerTemplateDefinitionCount: installedDefinitionCount,
	}, nil
}

// installContainerAssets 按导出内容创建/更新容器镜像、运行配置与绑定行，返回各自处理的数量。
//
// 导出格式（GenerateScriptJSONByScriptID / GenerateWorkflowJSONByWorkflowID）把容器资产拆成
// 三个独立列表：container_images 是镜像本体，container_template_specs 是共享运行配置
// （ContainerTemplateSpec），container_template_definitions 是「运行配置 × 镜像」绑定行
// （ContainerTemplateDefinition：spec_id 指向配置、image_id 指向镜像）。脚本的
// container_template_id 引用的就是绑定行主键，因此安装顺序必须是：
//  1. 先按主键 upsert 镜像：绑定行要引用它，且 service 层会校验镜像存在；
//  2. 再按主键 upsert 运行配置：绑定行的 spec_id 引用它；
//  3. 最后按主键 upsert 绑定行：主键原样保留，脚本的 container_template_id 引用因此仍然成立。
//
// 三个列表都只做「按主键 upsert」：「有 id 且已存在」→ 更新；「有 id 但不存在」→ 新增；
// 导出文件里没有的本地行保持不动（安装不会删除本地已有数据）。
// id 为空视为导出文件损坏直接报错（id 是这些实体之间唯一的引用键，随便补一个新 id 会让引用断链）。
func (c *Codec) installContainerAssets(ctx context.Context, imageMaps, specMaps, definitionMaps []map[string]any) (int, int, int, error) {
	if len(imageMaps) == 0 && len(specMaps) == 0 && len(definitionMaps) == 0 {
		return 0, 0, 0, nil
	}
	if c.containerService == nil {
		return 0, 0, 0, errors.New("container service is not configured")
	}

	for _, imageMap := range imageMaps {
		imageExport := &types.ContainerImageExport{}
		if err := decodeExportMap(imageMap, imageExport); err != nil {
			return 0, 0, 0, fmt.Errorf("invalid container image in export file: %w", err)
		}
		if imageExport.ID == 0 {
			return 0, 0, 0, errors.New("container image id is required in export file")
		}
		if err := c.upsertContainerImage(ctx, imageExport); err != nil {
			return 0, 0, 0, fmt.Errorf("failed to install container image %d: %w", imageExport.ID, err)
		}
	}

	for _, specMap := range specMaps {
		spec := &types.ContainerTemplateSpec{}
		if err := decodeExportMap(specMap, spec); err != nil {
			return 0, 0, 0, fmt.Errorf("invalid container template spec in export file: %w", err)
		}
		if spec.ID == 0 {
			return 0, 0, 0, errors.New("container template spec id is required in export file")
		}
		if err := c.upsertContainerTemplateSpec(ctx, spec); err != nil {
			return 0, 0, 0, fmt.Errorf("failed to install container template spec %d: %w", spec.ID, err)
		}
	}

	for _, definitionMap := range definitionMaps {
		definition := &types.ContainerTemplateDefinition{}
		if err := decodeExportMap(definitionMap, definition); err != nil {
			return 0, 0, 0, fmt.Errorf("invalid container template definition in export file: %w", err)
		}
		if definition.ID == 0 {
			return 0, 0, 0, errors.New("container template definition id is required in export file")
		}
		if err := c.upsertContainerTemplateDefinition(ctx, definition); err != nil {
			return 0, 0, 0, fmt.Errorf("failed to install container template definition %d: %w", definition.ID, err)
		}
	}

	return len(imageMaps), len(specMaps), len(definitionMaps), nil
}

// decodeExportMap 把导出文件里的 map 还原成强类型结构（与安装 workflow/script 体同一套做法）。
func decodeExportMap(item map[string]any, out any) error {
	raw, err := json.Marshal(item)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

// upsertContainerImage 按主键更新镜像，主键不存在则新增。
// 导出结构（ContainerImageExport）不含时间字段，这里也不动镜像的 created_at。
func (c *Codec) upsertContainerImage(ctx context.Context, imageExport *types.ContainerImageExport) error {
	item := &types.ContainerImage{
		ID:          imageExport.ID,
		Name:        imageExport.Name,
		FullName:    imageExport.FullName,
		Description: imageExport.Description,
		Size:        imageExport.Size,
		PullPolicy:  imageExport.PullPolicy,
	}
	if strings.TrimSpace(item.PullPolicy) == "" {
		item.PullPolicy = types.PullPolicyIfNotPresent
	}

	if _, err := c.containerService.GetContainerImageByID(ctx, item.ID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return c.containerService.CreateContainerImage(ctx, item)
		}
		return err
	}
	return c.containerService.UpdateContainerImage(ctx, item)
}

// upsertContainerTemplateSpec 按主键更新共享运行配置，主键不存在则新增。
// 导出结构不含时间字段，这里不动该配置的 created_at。
func (c *Codec) upsertContainerTemplateSpec(ctx context.Context, spec *types.ContainerTemplateSpec) error {
	if _, err := c.containerService.GetContainerTemplateSpecByID(ctx, spec.ID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return c.containerService.CreateContainerTemplateSpec(ctx, spec)
		}
		return err
	}
	return c.containerService.UpdateContainerTemplateSpec(ctx, spec)
}

// upsertContainerTemplateDefinition 按主键更新「运行配置 × 镜像」绑定行，主键不存在则新增。
// 绑定行的 spec_id / image_id 指向同一批 container_template_specs / container_images 的主键，
// 因此调用方必须先导入镜像与运行配置（见 installContainerAssets 的顺序约束）。
func (c *Codec) upsertContainerTemplateDefinition(ctx context.Context, definition *types.ContainerTemplateDefinition) error {
	if _, err := c.containerService.GetContainerTemplateDefinitionByID(ctx, definition.ID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return c.containerService.CreateContainerTemplateDefinition(ctx, definition)
		}
		return err
	}
	return c.containerService.UpdateContainerTemplateDefinition(ctx, definition)
}
