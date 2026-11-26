package builtin

import (
	"context"
	"fmt"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	"github.com/biox-dev/gobrave/internal/agent/tool"
)

// PublishScriptToolName 是「发布脚本到 store」工具名。
const PublishScriptToolName = "publish_script"

// PublishScriptInput 是 publish_script 工具的入参。
//
// ScriptID 是 script 表主键（字符串以保精度，容错 @script: 前缀）；
// Message 是本次 git 提交的说明，留空时由后端生成默认文案。
type PublishScriptInput struct {
	ScriptID string `json:"script_id"`
	Message  string `json:"message"`
}

// PublishScriptOutput 是 publish_script 工具返回给模型的结果。
type PublishScriptOutput struct {
	runtimecontext.StorePublishResult
	Error string `json:"error,omitempty"`
}

// PublishScriptToStore 返回一个「发布脚本到 store」工具。
//
// 行为（与 handler 的 /script/publish 同口径）：
//   - 重新生成 script.json 并提交到脚本目录；
//   - 首次发布时创建 store 记录（随机 PathName），再次发布沿用原目录；
//   - 把脚本目录 push 到 store 裸仓库，返回 store_id / store_path / pushed。
//
// 该工具会写库、重写脚本目录并 push 到 store，因此声明 write 授权。
func PublishScriptToStore(publisher runtimecontext.StoreComponentPublisher) tool.Tool {
	return tool.NewFunc(
		PublishScriptToolName,
		`把当前项目里的脚本发布到本地组件仓库（store），使其可被 install_script 安装。

行为：
- 重新生成 script.json 并提交为一个 git commit；
- 首次发布创建 store 记录，再次发布沿用同一 store 目录（不会重复创建）；
- 把脚本目录 push 到 store 裸仓库。

参数：
- script_id（必填）：script 表主键，可传 "@script:<id>" 形式；脚本必须属于当前激活项目；
- message：本次发布的提交说明，留空时由后端生成。

返回：store_id（可直接传给 install_script）、store_type、store_name、path_name、store_path、component_id（script_id，即脚本目录名）、pushed（是否真的推了新提交）、up_to_date（为 true 表示自上次发布后没有新内容，属幂等成功）。`,
		tool.Schema("发布脚本到 store 的入参", map[string]any{
			"script_id": tool.StringProperty("要发布的脚本主键 ID（必填，整数，字符串以保精度）。"),
			"message":   tool.StringProperty("本次发布的 git 提交说明；留空时自动生成。"),
		}, "script_id"),
		func(ctx context.Context, in PublishScriptInput) (PublishScriptOutput, error) {
			projectID, err := requireProjectID(ctx, PublishScriptToolName)
			if err != nil {
				return PublishScriptOutput{}, err
			}
			scriptID, err := parseScriptID(in.ScriptID)
			if err != nil {
				return PublishScriptOutput{}, wrapToolNameError(PublishScriptToolName, err)
			}
			if publisher == nil {
				return PublishScriptOutput{}, fmt.Errorf("%s: store publisher is not available in this runtime", PublishScriptToolName)
			}

			if err := authorizeResourceWrite(ctx, PublishScriptToolName, fmt.Sprintf("Publish script %d to store", scriptID), tool.PermissionPreview{}); err != nil {
				return PublishScriptOutput{}, err
			}

			result, publishErr := publisher.PublishScript(ctx, runtimecontext.StorePublishScriptInput{
				ProjectID: projectID,
				ScriptID:  scriptID,
				Message:   strings.TrimSpace(in.Message),
			})
			out := PublishScriptOutput{StorePublishResult: result}
			if publishErr != nil {
				out.Error = publishErr.Error()
			}
			return out, nil
		},
	)
}
