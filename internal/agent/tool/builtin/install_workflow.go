package builtin

import (
	"context"
	"fmt"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	"github.com/biox-dev/gobrave/internal/agent/tool"
)

// InstallWorkflowToolName 是「从 store 安装工作流」工具名。
const InstallWorkflowToolName = "install_workflow"

// InstallWorkflowInput 是 install_workflow 工具的入参。
//
// StoreID 是 store 表主键（字符串以保精度，容错 @store: 前缀），通常来自 page_store 的结果。
type InstallWorkflowInput struct {
	StoreID string `json:"store_id"`
}

// InstallWorkflowOutput 是 install_workflow 工具返回给模型的结果。
type InstallWorkflowOutput struct {
	runtimecontext.StoreInstallResult
	Error string `json:"error,omitempty"`
}

// InstallWorkflowFromStore 返回一个「从 store 安装工作流」工具。
//
// 行为（与 handler 的 /store/:storeId/install-workflow 同口径）：
//   - 从 store 裸仓库 HEAD 里的 workflow.json 读出 workflow_id，同步出目标工作流目录；
//   - 按导出文件版本解析并落库（工作流 + 引用的脚本 + 容器模板运行配置 / 绑定行）；
//   - 脚本快照从工作流目录还原到脚本目录。
//
// 该工具会写库并同步磁盘目录，因此声明 write 授权。
func InstallWorkflowFromStore(installer runtimecontext.StoreComponentInstaller) tool.Tool {
	return tool.NewFunc(
		InstallWorkflowToolName,
		`把组件仓库（store）里的工作流安装到当前激活项目。

行为：
- 从 store 的 workflow.json 读取 workflow_id，把工作流目录同步到本地（已有则覆盖本地改动）；
- 解析 workflow.json（按文件顶层 version 选择对应格式）并落库：工作流、它引用的脚本、容器模板运行配置与绑定行；
- 工作流目录内的脚本快照还原到本地脚本目录。

参数：
- store_id（必填）：store 表主键，可传 "@store:<id>" 形式；先用 page_store（store_type=workflow）查到可安装的 store。

返回：store_id、store_path、target_dir（本地工作流目录）、component_id（workflow_id）、installed_id（workflow 表主键）、installed_script_count、installed_container_template_spec_count、installed_container_template_definition_count。`,
		tool.Schema("从 store 安装工作流的入参", map[string]any{
			"store_id": tool.StringProperty("要安装的 store 主键 ID（必填，整数，字符串以保精度）。"),
		}, "store_id"),
		func(ctx context.Context, in InstallWorkflowInput) (InstallWorkflowOutput, error) {
			projectID, err := requireProjectID(ctx, InstallWorkflowToolName)
			if err != nil {
				return InstallWorkflowOutput{}, err
			}
			storeID, err := parseStoreID(in.StoreID)
			if err != nil {
				return InstallWorkflowOutput{}, wrapToolNameError(InstallWorkflowToolName, err)
			}
			if installer == nil {
				return InstallWorkflowOutput{}, fmt.Errorf("%s: store installer is not available in this runtime", InstallWorkflowToolName)
			}

			if err := authorizeResourceWrite(ctx, InstallWorkflowToolName, fmt.Sprintf("Install workflow from store %d", storeID), tool.PermissionPreview{}); err != nil {
				return InstallWorkflowOutput{}, err
			}

			result, installErr := installer.InstallWorkflow(ctx, runtimecontext.StoreInstallWorkflowInput{
				ProjectID: projectID,
				StoreID:   storeID,
			})
			out := InstallWorkflowOutput{StoreInstallResult: result}
			if installErr != nil {
				out.Error = installErr.Error()
			}
			return out, nil
		},
	)
}
