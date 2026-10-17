package container

import (
	"context"
	"fmt"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent"
	"github.com/biox-dev/gobrave/internal/agent/tool"
)

// memorySaver 实现 tool.MemorySaver：把工具（save_memory）写记忆的请求落到记忆域的
// MemoryManager 上。
//
// 之所以放在 container 而不是 tool 包：Memory / MemoryManager 属于 agent 包，而 tool 包
// 刻意不依赖 agent（避免循环引用）。这里只做「入参归一化 → 构造 agent.Memory → 持久化」，
// 真正的存储细节仍由 MemoryRepository 负责。
//
// 直接使用 MemoryManager.Save（而不是 AgentService.SaveMemory）是为了避免 container →
// AgentService → Client → Options 的构造环。
type memorySaver struct {
	mgr *agent.MemoryManager
}

// newMemorySaver 构建「保存长期记忆」执行器并注入内置工具体系。
func newMemorySaver(mgr *agent.MemoryManager) tool.MemorySaver {
	return &memorySaver{mgr: mgr}
}

func (s *memorySaver) SaveMemory(ctx context.Context, input tool.MemorySaveInput) (tool.MemorySaveResult, error) {
	if s.mgr == nil {
		return tool.MemorySaveResult{}, fmt.Errorf("save_memory: memory manager is not configured")
	}
	if input.UserID <= 0 {
		return tool.MemorySaveResult{}, fmt.Errorf("save_memory: user_id is required")
	}
	content := strings.TrimSpace(input.Content)
	if content == "" {
		return tool.MemorySaveResult{}, fmt.Errorf("save_memory: content is required")
	}

	kind := agent.MemoryKind(strings.ToLower(strings.TrimSpace(input.Kind)))
	switch kind {
	case agent.MemoryKindFact, agent.MemoryKindSummary, agent.MemoryKindNote, agent.MemoryKindEvent:
		// 合法类别，原样使用。
	default:
		kind = agent.MemoryKindNote
	}

	memID := input.ID
	var mem *agent.Memory
	created := memID == 0
	if created {
		mem = agent.NewMemory(input.UserID, input.ProjectID, input.SessionID, kind, content)
	} else {
		existing, err := s.mgr.Get(ctx, memID)
		if err != nil {
			return tool.MemorySaveResult{}, fmt.Errorf("save_memory: failed to load memory %d: %w", memID, err)
		}
		// 只允许更新本人记忆，避免模型把注意事项写进其他用户的数据。
		if existing.UserID != input.UserID {
			return tool.MemorySaveResult{}, fmt.Errorf("save_memory: memory %d does not belong to user %d", memID, input.UserID)
		}
		mem = existing
		mem.Kind = kind
		mem.Content = content
		if mem.ProjectID <= 0 && input.ProjectID > 0 {
			mem.ProjectID = input.ProjectID
		}
		if mem.SessionID <= 0 && input.SessionID > 0 {
			mem.SessionID = input.SessionID
		}
	}

	// importance 由工具层归一化到 [0,10]；这里只做兜底。
	if input.Importance > 10 {
		mem.Importance = 10
	} else if input.Importance > 0 {
		mem.Importance = input.Importance
	} else if created {
		mem.Importance = 5
	}

	// metadata 合并写入；project_id 同时落到结构化字段与 metadata，便于兼容旧数据。
	if len(input.Metadata) > 0 || input.ProjectID > 0 {
		if mem.Metadata == nil {
			mem.Metadata = make(map[string]any, len(input.Metadata)+1)
		}
		for k, v := range input.Metadata {
			mem.Metadata[k] = v
		}
		if input.ProjectID > 0 {
			mem.Metadata["project_id"] = input.ProjectID
		}
	}

	if err := s.mgr.Save(ctx, mem); err != nil {
		return tool.MemorySaveResult{}, fmt.Errorf("save_memory: %w", err)
	}

	return tool.MemorySaveResult{
		ID:         mem.ID,
		Kind:       string(mem.Kind),
		Importance: mem.Importance,
		Created:    created,
	}, nil
}
