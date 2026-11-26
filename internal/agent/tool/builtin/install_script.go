package builtin

import (
	"context"
	"fmt"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	"github.com/biox-dev/gobrave/internal/agent/tool"
)

// InstallScriptToolName 是「从 store 安装脚本」工具名。
const InstallScriptToolName = "install_script"

// InstallScriptInput 是 install_script 工具的入参。
//
// StoreID 是 store 表主键（字符串以保精度，容错 @store: 前缀）；
// Create 为 true 时作为副本新增（生成新的 script_id），否则按导出文件里的
// script_id 在目标项目内更新或新增。
type InstallScriptInput struct {
	StoreID string `json:"store_id"`
	Create  bool   `json:"create"`
}

// InstallScriptOutput 是 install_script 工具返回给模型的结果。
type InstallScriptOutput struct {
	runtimecontext.StoreInstallResult
	Error string `json:"error,omitempty"`
}

// InstallScriptFromStore 返回一个「从 store 安装脚本」工具。
//
// 行为（与 handler 的 /store/:storeId/install-script 同口径）：
//   - create=true 时生成新的 script_id；否则从 store 裸仓库 HEAD 里的 script.json 读取；
//   - 把 store 同步到目标脚本目录，按导出文件版本解析并落库（脚本 + 容器模板运行配置 / 绑定行）。
//
// 该工具会写库并同步磁盘目录，因此声明 write 授权。
func InstallScriptFromStore(installer runtimecontext.StoreComponentInstaller) tool.Tool {
	return tool.NewFunc(
		InstallScriptToolName,
		`把组件仓库（store）里的脚本安装到当前激活项目。

行为：
- 默认按 script.json 里的 script_id 在目标项目内更新或新增；create=true 时作为副本新增（生成新的 script_id，避免覆盖已有脚本）；
- 把 store 同步到目标脚本目录；
- 解析 script.json（按文件顶层 version 选择对应格式）并落库：脚本、容器模板运行配置与绑定行。

参数：
- store_id（必填）：store 表主键，可传 "@store:<id>" 形式；先用 page_store（store_type=script）查到可安装的 store；
- create：是否作为副本新增，默认 false（按原 script_id 安装）。

返回：store_id、store_path、target_dir（本地脚本目录）、component_id（script_id）、installed_id（script 表主键）、installed_container_template_spec_count、installed_container_template_definition_count。`,
		tool.Schema("从 store 安装脚本的入参", map[string]any{
			"store_id": tool.StringProperty("要安装的 store 主键 ID（必填，整数，字符串以保精度）。"),
			"create":   tool.BooleanProperty("是否作为副本新增（生成新的 script_id）；默认 false 表示按原 script_id 安装。"),
		}, "store_id"),
		func(ctx context.Context, in InstallScriptInput) (InstallScriptOutput, error) {
			projectID, err := requireProjectID(ctx, InstallScriptToolName)
			if err != nil {
				return InstallScriptOutput{}, err
			}
			storeID, err := parseStoreID(in.StoreID)
			if err != nil {
				return InstallScriptOutput{}, wrapToolNameError(InstallScriptToolName, err)
			}
			if installer == nil {
				return InstallScriptOutput{}, fmt.Errorf("%s: store installer is not available in this runtime", InstallScriptToolName)
			}

			description := fmt.Sprintf("Install script from store %d", storeID)
			if in.Create {
				description = fmt.Sprintf("Install script from store %d as a new copy", storeID)
			}
			if err := authorizeResourceWrite(ctx, InstallScriptToolName, description, tool.PermissionPreview{}); err != nil {
				return InstallScriptOutput{}, err
			}

			result, installErr := installer.InstallScript(ctx, runtimecontext.StoreInstallScriptInput{
				ProjectID:  projectID,
				StoreID:    storeID,
				CreateMode: in.Create,
			})
			out := InstallScriptOutput{StoreInstallResult: result}
			if installErr != nil {
				out.Error = installErr.Error()
			}
			return out, nil
		},
	)
}
