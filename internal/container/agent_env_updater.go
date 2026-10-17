package container

import (
	"context"
	"fmt"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/tool"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
)

// agentEnvUpdater 实现 tool.AgentEnvUpdater：把用户当前业务上下文（llmEnv）写回
// user.AgentConfig.EnvID / EnvType。
//
// 与前端调用 /auth/agent-env 的效果一致；区别只是触发方是 Agent 工具（create_script 在
// 新建分析节点后把上下文切到新节点）。这里只改业务上下文，不触碰 Profile 与权限策略。
//
// 只读一次用户（GetUserByID → 改 EnvID/EnvType → Save），避免用请求 ctx 中可能过期的
// User 快照覆盖用户在别处同时修改的 Profile / Permissions。
type agentEnvUpdater struct {
	userRepo interfaces.UserRepository
}

// newAgentEnvUpdater 构建「更新用户业务上下文」执行器并注入内置工具体系。
func newAgentEnvUpdater(userRepo interfaces.UserRepository) tool.AgentEnvUpdater {
	return &agentEnvUpdater{userRepo: userRepo}
}

func (u *agentEnvUpdater) UpdateAgentEnv(ctx context.Context, userID int64, envID string, envType string) error {
	if u.userRepo == nil {
		return fmt.Errorf("update_agent_env: user repository is not configured")
	}
	if userID <= 0 {
		return fmt.Errorf("update_agent_env: user_id is required")
	}

	user, err := u.userRepo.GetUserByID(ctx, userID)
	if err != nil {
		return fmt.Errorf("update_agent_env: failed to get user %d: %w", userID, err)
	}
	if user == nil {
		return fmt.Errorf("update_agent_env: user %d not found", userID)
	}

	user.AgentConfig.EnvID = strings.TrimSpace(envID)
	user.AgentConfig.EnvType = strings.TrimSpace(envType)
	if err := u.userRepo.UpdateUser(ctx, user); err != nil {
		return fmt.Errorf("update_agent_env: failed to update user %d: %w", userID, err)
	}
	return nil
}
