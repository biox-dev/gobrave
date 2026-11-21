package builtin

import (
	"context"
	"fmt"

	"github.com/biox-dev/gobrave/internal/agent/tool"
	agenttypes "github.com/biox-dev/gobrave/internal/agent/types"
)

// sessionResourceLister 是会话资源列表能力的窄端口。
type sessionResourceLister interface {
	ListBySession(ctx context.Context, sessionID int64) ([]agenttypes.SessionResource, error)
}

// ListSessionResourcesToolName 是「列出当前会话产出的资源」工具名。
const ListSessionResourcesToolName = "list_session_resources"

// SessionResourceListOutput 是 list_session_resources 返回给模型的结果。
type SessionResourceListOutput struct {
	SessionID int64                  `json:"session_id,string"`
	Resources []SessionResourceBrief `json:"resources"`
	Error     string                 `json:"error,omitempty"`
}

// SessionResourceBrief 是会话资源的一条只读摘要。
type SessionResourceBrief struct {
	ResourceType string `json:"resource_type"`
	ResourceID   int64  `json:"resource_id,string"`
}

// ListSessionResources 列当前会话产出的资源（脚本 / 工作流 / 分析节点 / 分析运行 / 报告）。
//
// 简单任务（单个会话完成）中，agent 用它拿到本会话的资源 ID，然后直接写报告；
// 复杂任务中，它用于把本会话资源登记进大纲章节的 source_id。
func ListSessionResources(svc sessionResourceLister) tool.Tool {
	return tool.NewFunc(
		ListSessionResourcesToolName,
		"List resources produced by the CURRENT session (script / workflow / analysis_node / analysis / project_report) with their IDs. Use the returned IDs to fill report plan section source_id, or to decide what to summarize when writing a report for a simple single-session task.",
		tool.Schema("列出当前会话资源的入参", map[string]any{}),
		func(ctx context.Context, _ struct{}) (SessionResourceListOutput, error) {
			sessionID := tool.SessionIDFrom(ctx)
			if sessionID <= 0 {
				return SessionResourceListOutput{}, fmt.Errorf("%s: session_id is required in this runtime", ListSessionResourcesToolName)
			}
			if svc == nil {
				return SessionResourceListOutput{}, fmt.Errorf("%s: session resource service is not available", ListSessionResourcesToolName)
			}
			rows, err := svc.ListBySession(ctx, sessionID)
			if err != nil {
				return SessionResourceListOutput{SessionID: sessionID, Error: err.Error()}, nil
			}
			out := make([]SessionResourceBrief, 0, len(rows))
			for _, row := range rows {
				out = append(out, SessionResourceBrief{
					ResourceType: string(row.ResourceType),
					ResourceID:   row.ResourceID,
				})
			}
			return SessionResourceListOutput{SessionID: sessionID, Resources: out}, nil
		},
	).WithPermission("read", "", "列出当前会话产出的资源")
}
