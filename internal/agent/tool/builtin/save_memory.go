package builtin

import (
	"context"
	"fmt"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/tool"
)

// SaveMemoryToolName 是「保存长期记忆」工具名。
const SaveMemoryToolName = "save_memory"

// 记忆类别（与 agent.MemoryKind 取值保持一致，避免 tool 包依赖 agent 包）。
const (
	memoryKindFact    = "fact"
	memoryKindSummary = "summary"
	memoryKindNote    = "note"
	memoryKindEvent   = "event"
)

// SaveMemoryInput 是 save_memory 工具的入参。
//
// 用户归属（user_id）不在入参里：它由 RuntimeContext 提供，避免模型把记忆写到别的用户
// 名下。MemoryID 为 0 表示新建（主键由持久化层 BeforeCreate 分配），> 0 表示更新既有记忆
// （例如补充 / 纠正同一条注意事项）。
type SaveMemoryInput struct {
	MemoryID   int64          `json:"memory_id,string,omitempty"`
	Kind       string         `json:"kind,omitempty"`
	Content    string         `json:"content"`
	Importance int            `json:"importance,omitempty"`
	Metadata   map[string]any `json:"metadata,omitempty"`
}

// SaveMemoryOutput 是 save_memory 工具返回给模型的结果。
type SaveMemoryOutput struct {
	MemoryID   int64  `json:"memory_id,string,omitempty"`
	Kind       string `json:"kind,omitempty"`
	Importance int    `json:"importance,omitempty"`
	Created    bool   `json:"created"`
	Message    string `json:"message,omitempty"`
	Error      string `json:"error,omitempty"`
}

// SaveMemory 返回一个「保存长期记忆」工具。
//
// 使用场景（供模型理解）：
//   - 调用工具（create_script / run_analysis_node / build_analysis_node_params 等）反复
//     出现同一类可复现错误时，把「错误现象 + 根因 + 下次如何规避」写成一条 note 记忆；
//   - 后续轮次开始时，相关记忆会随系统提示词的「相关记忆」块注入，从而避免重复踩坑。
//
// 归属用户由 RuntimeContext.UserID 提供（见 tool.UserIDFrom），不由模型填写，避免把记忆
// 写到别的用户名下。
//
// 该工具会写入用户级持久化数据，因此声明 write 授权：上层在执行前会先走权限门禁。
func SaveMemory() tool.Tool {
	return tool.NewFunc(
		SaveMemoryToolName,
		"保存一条长期记忆（跨会话保留）。当调用其他工具遇到可复现的错误（参数格式不对、上下文缺失、环境未配置等）时，用它把「错误现象 + 原因 + 下次正确的做法」写成一条注意事项，避免后续重复犯同样的错；也可用于记录用户偏好与已确认的事实。",
		tool.Schema("保存长期记忆的入参", map[string]any{
			"memory_id": tool.IntegerProperty("可选：要更新的既有记忆 ID（雪花 ID）。留空 / 0 表示新建一条记忆。"),
			"kind": map[string]any{
				"type":        "string",
				"description": "记忆类别：fact（用户偏好 / 事实）、note（注意事项，默认）、summary（会话 / 任务摘要）、event（事件记录）。",
				"enum":        []string{memoryKindFact, memoryKindNote, memoryKindSummary, memoryKindEvent},
			},
			"content":    tool.StringProperty("记忆正文。写注意事项时建议包含：触发场景、错误现象 / 报错文本、根因、下次正确做法。"),
			"importance": tool.IntegerProperty("重要度 0-10（默认 5），越高检索排序越靠前。"),
			"metadata":   map[string]any{"type": "object", "description": "可选：扩展信息（如 source、tool、tags）。"},
		}, "content"),
		func(ctx context.Context, in SaveMemoryInput) (SaveMemoryOutput, error) {
			content := strings.TrimSpace(in.Content)
			if content == "" {
				return SaveMemoryOutput{}, fmt.Errorf("save_memory: content is required")
			}

			userID := resolveMemoryUserID(ctx)
			if userID <= 0 {
				return SaveMemoryOutput{}, fmt.Errorf("save_memory: user id is not available in this runtime")
			}

			saver := tool.MemorySaverFrom(ctx)
			if saver == nil {
				return SaveMemoryOutput{}, fmt.Errorf("save_memory: memory saver is not available in this runtime")
			}

			kind := normalizeMemoryKind(in.Kind)
			importance := normalizeMemoryImportance(in.Importance)

			// 记忆归属与上下文都取自运行时上下文：项目 / 会话写进结构化字段（Memory.ProjectID /
			// Memory.SessionID），供上下文优先检索与会话回溯。
			var projectID int64
			var sessionID int64
			if rc := tool.RuntimeContextFrom(ctx); rc != nil {
				projectID = rc.ProjectID
				sessionID = rc.SessionID
			}
			metadata := in.Metadata

			res, err := saver.SaveMemory(ctx, tool.MemorySaveInput{
				UserID:     userID,
				ID:         in.MemoryID,
				SessionID:  sessionID,
				ProjectID:  projectID,
				Kind:       kind,
				Content:    content,
				Importance: importance,
				Metadata:   metadata,
			})
			if err != nil {
				return SaveMemoryOutput{
					MemoryID: in.MemoryID,
					Kind:     kind,
					Error:    err.Error(),
				}, nil
			}

			action := "已更新记忆"
			if res.Created {
				action = "已记录到长期记忆"
			}
			return SaveMemoryOutput{
				MemoryID:   res.ID,
				Kind:       res.Kind,
				Importance: res.Importance,
				Created:    res.Created,
				Message:    action,
			}, nil
		},
	).WithPermission("write", "", "保存一条长期记忆")
}

// resolveMemoryUserID 取记忆归属用户：来自 RuntimeContext.UserID（请求级用户事实的唯一来源）。
func resolveMemoryUserID(ctx context.Context) int64 {
	return tool.UserIDFrom(ctx)
}

// normalizeMemoryKind 归一化记忆类别；无法识别时回退为 note。
func normalizeMemoryKind(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case memoryKindFact:
		return memoryKindFact
	case memoryKindSummary:
		return memoryKindSummary
	case memoryKindEvent:
		return memoryKindEvent
	default:
		return memoryKindNote
	}
}

// normalizeMemoryImportance 把重要度收敛到 [0, 10]；未填（0）时取默认值 5。
func normalizeMemoryImportance(importance int) int {
	if importance <= 0 {
		return 5
	}
	if importance > 10 {
		return 10
	}
	return importance
}
