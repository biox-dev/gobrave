package builtin

import (
	"context"
	"fmt"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	"github.com/biox-dev/gobrave/internal/agent/tool"
)

// UIActionToolName 是「驱动前端界面」的工具名。
const UIActionToolName = "ui_action"

// UI 动作名（与前端 brave-ui/src/event-bus/actionRegistry.ts 注册的 action 一一对应）。
//
// 只实现 route.go 一个：新增动作时在这里加常量、在 uiActionHandlers 里加一个
// payload 构造函数，再在前端注册同名 action 即可，无需改动工具的其他部分。
const (
	// UIActionRouteGo 让前端跳到指定路由（前端 router.go：useRouterStore().go(path, state)）。
	UIActionRouteGo = "router.go"
)

// UIActionInput 是 ui_action 工具的入参。
//
// 不同 action 使用不同字段，未使用的字段留空即可（与前端 ActionDispatcher 的
// 多 action 分发对齐，而不是给每个动作造一个工具）。
type UIActionInput struct {
	Action string `json:"action"` // 动作名，取值见 UIAction* 常量
	Path   string `json:"path"`   // router.go：目标路由路径（前端 HashRouter 内部路径）
}

// UIActionOutput 是 ui_action 工具返回给模型的结果。
type UIActionOutput struct {
	Action  string `json:"action"`
	Pushed  bool   `json:"pushed"` // 是否已投递给在线客户端
	Path    string `json:"path,omitempty"`
	Message string `json:"message,omitempty"`
	Error   string `json:"error,omitempty"`
}

// uiActionPayload 把动作参数转成前端 payload。
//
// 每个动作一个构造函数，返回 payload 与一个空操作/错误。新增动作只需要：
//  1. 加一个 UIAction* 常量；
//  2. 在 uiActionPayload 的 switch 里加一个分支；
//  3. 前端 actionRegistry 注册同名 action。
func uiActionPayload(in UIActionInput) (map[string]any, error) {
	switch in.Action {
	case UIActionRouteGo:
		path := strings.TrimSpace(in.Path)
		if path == "" {
			return nil, fmt.Errorf("path is required for %s", UIActionRouteGo)
		}
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		return map[string]any{"path": path}, nil
	default:
		return nil, fmt.Errorf("unsupported action %q (supported: %s)", in.Action, UIActionRouteGo)
	}
}

// UIAction 返回「驱动前端界面」的工具（ui_action）。
//
// 用户 ID 取自执行事实（tool.UserIDFrom），模型不参与：只能操作自己这条连接对应的界面。
// 推送失败（无在线客户端 / hub 未接入）不视为工具失败——它是显示层提示，
// 模型仍然可以把结果原样讲给用户。
func UIAction(pusher runtimecontext.UIActionPusher) tool.Tool {
	return tool.NewFunc(
		UIActionToolName,
		"驱动当前用户的前端界面执行一个动作。目前支持 router.go：把用户界面跳转到指定路由路径（例如 /container-template、/agent-skill、/c/scripts）。用于「带用户去某个页面」这类请求——回答完后直接跳过去，不要只把路径打印在聊天里。动作会推送到用户当前打开的页面；用户不在线时静默忽略，不影响本次回复。",
		tool.Schema("驱动前端界面的入参", map[string]any{
			"action": tool.StringProperty("动作名。目前只支持 router.go（路由跳转）。"),
			"path":   tool.StringProperty("action=router.go 时的目标路径，必须以前端路由形式给出，例如 /container-template 或 /agent-skill（省略前导 / 会自动补上）。"),
		}, "action"),
		func(ctx context.Context, in UIActionInput) (UIActionOutput, error) {
			in.Action = strings.TrimSpace(in.Action)
			payload, err := uiActionPayload(in)
			if err != nil {
				return UIActionOutput{Action: in.Action, Error: err.Error()}, nil
			}
			if pusher == nil {
				return UIActionOutput{Action: in.Action, Error: "ui_action: realtime push is not available in this runtime"}, nil
			}
			userID := tool.UserIDFrom(ctx)
			if userID == 0 {
				return UIActionOutput{Action: in.Action, Error: "ui_action: no active user in this conversation"}, nil
			}

			out := UIActionOutput{Action: in.Action}
			if err := pusher.PushUIAction(ctx, userID, in.Action, payload); err != nil {
				out.Message = "action prepared but push failed: " + err.Error()
				return out, nil
			}
			out.Pushed = true
			out.Path, _ = payload["path"].(string)
			out.Message = "action pushed to the user's open UI"
			return out, nil
		},
	).WithPermission("execute", "", "驱动用户界面执行动作（路由跳转等）")
}
