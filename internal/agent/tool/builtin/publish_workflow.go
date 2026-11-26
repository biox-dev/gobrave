package builtin

import (
	"context"
	"fmt"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	"github.com/biox-dev/gobrave/internal/agent/tool"
)

// PublishWorkflowToolName 是「发布工作流到 store」工具名。
const PublishWorkflowToolName = "publish_workflow"

// PublishWorkflowInput 是 publish_workflow 工具的入参。
//
// WorkflowID 是 workflow 表主键（字符串以保精度，容错 @workflow: 前缀）；
// Message 是本次 git 提交的说明，留空时由后端生成默认文案。
type PublishWorkflowInput struct {
	WorkflowID string `json:"workflow_id"`
	Message    string `json:"message"`
}

// PublishWorkflowOutput 是 publish_workflow 工具返回给模型的结果。
type PublishWorkflowOutput struct {
	runtimecontext.StorePublishResult
	Error string `json:"error,omitempty"`
}

// PublishWorkflowToStore 返回一个「发布工作流到 store」工具。
//
// 行为（与 handler 的 /workflow/publish 同口径）：
//   - 重新生成 workflow.json（含脚本快照）并提交到工作流目录；
//   - 首次发布时创建 store 记录（随机 PathName），再次发布沿用原目录；
//   - 把工作流目录 push 到 store 裸仓库，返回 store_id / store_path / pushed。
//
// 该工具会写库、重写工作流目录并 push 到 store，因此声明 write 授权。
func PublishWorkflowToStore(publisher runtimecontext.StoreComponentPublisher) tool.Tool {
	return tool.NewFunc(
		PublishWorkflowToolName,
		`把当前项目里的工作流发布到本地组件仓库（store），使其可被 install_workflow 安装。

行为：
- 重新生成 workflow.json（含引用的脚本目录快照）并提交为一个 git commit；
- 首次发布创建 store 记录，再次发布沿用同一 store 目录（不会重复创建）；
- 把工作流目录 push 到 store 裸仓库。

参数：
- workflow_id（必填）：workflow 表主键，可传 "@workflow:<id>" 形式；工作流必须属于当前激活项目；
- message：本次发布的提交说明，留空时由后端生成。

返回：store_id（可直接传给 install_workflow）、store_type、store_name、path_name、store_path、component_id（workflow_id，即工作流目录名）、pushed（是否真的推了新提交）、up_to_date（为 true 表示自上次发布后没有新内容，属幂等成功）。`,
		tool.Schema("发布工作流到 store 的入参", map[string]any{
			"workflow_id": tool.StringProperty("要发布的工作流主键 ID（必填，整数，字符串以保精度）。"),
			"message":     tool.StringProperty("本次发布的 git 提交说明；留空时自动生成。"),
		}, "workflow_id"),
		func(ctx context.Context, in PublishWorkflowInput) (PublishWorkflowOutput, error) {
			projectID, err := requireProjectID(ctx, PublishWorkflowToolName)
			if err != nil {
				return PublishWorkflowOutput{}, err
			}
			workflowID, err := parseWorkflowID(in.WorkflowID)
			if err != nil {
				return PublishWorkflowOutput{}, wrapToolNameError(PublishWorkflowToolName, err)
			}
			if publisher == nil {
				return PublishWorkflowOutput{}, fmt.Errorf("%s: store publisher is not available in this runtime", PublishWorkflowToolName)
			}

			if err := authorizeResourceWrite(ctx, PublishWorkflowToolName, fmt.Sprintf("Publish workflow %d to store", workflowID), tool.PermissionPreview{}); err != nil {
				return PublishWorkflowOutput{}, err
			}

			result, publishErr := publisher.PublishWorkflow(ctx, runtimecontext.StorePublishWorkflowInput{
				ProjectID:  projectID,
				WorkflowID: workflowID,
				Message:    strings.TrimSpace(in.Message),
			})
			out := PublishWorkflowOutput{StorePublishResult: result}
			if publishErr != nil {
				out.Error = publishErr.Error()
			}
			return out, nil
		},
	)
}
