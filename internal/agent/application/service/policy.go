package service

import (
	"context"

	. "github.com/biox-dev/gobrave/internal/agent/types"
	. "github.com/biox-dev/gobrave/internal/agent/types/interface"
)

// defaultPermissionPolicy 是框架默认策略：
//   - network：直接允许；
//   - read / write / delete / move / execute / analysis_node：需要人工确认；
//   - 未知类型：保守地要求人工确认。
type defaultPermissionPolicy struct{}

// DefaultPermissionPolicy 返回框架内置的默认策略实例。
func DefaultPermissionPolicy() PermissionPolicy { return defaultPermissionPolicy{} }

func (defaultPermissionPolicy) Check(_ context.Context, _ int64, operation Operation) PermissionDecision {
	switch operation.Type {
	case OperationNetwork:
		return DecisionAllow
	case OperationRead, OperationWrite, OperationDelete, OperationMove, OperationExecute, OperationAnalysisNode:
		return DecisionAsk
	default:
		return DecisionAsk
	}
}

// allowAllPermissionPolicy 用于无 UI 的独立调用场景（standalone Runtime）。
type allowAllPermissionPolicy struct{}

// AllowAllPermissionPolicy 返回一个“全部放行”的策略实例。
func AllowAllPermissionPolicy() PermissionPolicy { return allowAllPermissionPolicy{} }

func (allowAllPermissionPolicy) Check(_ context.Context, _ int64, _ Operation) PermissionDecision {
	return DecisionAllow
}

// nopUserAgentConfigProvider 是空配置提供者。
type nopUserAgentConfigProvider struct{}

func (nopUserAgentConfigProvider) GetAgentConfig(context.Context, int64) (UserAgentConfig, error) {
	return UserAgentConfig{}, nil
}

// userPermissionPolicy 先按用户自定义的许可策略决策，未命中时回退到默认策略。
type userPermissionPolicy struct {
	provider UserAgentConfigProvider
	fallback PermissionPolicy
}

// NewUserPermissionPolicy 创建用户感知的权限策略。
func NewUserPermissionPolicy(provider UserAgentConfigProvider) PermissionPolicy {
	if provider == nil {
		provider = nopUserAgentConfigProvider{}
	}
	return &userPermissionPolicy{provider: provider, fallback: DefaultPermissionPolicy()}
}

func (p *userPermissionPolicy) Check(ctx context.Context, userID int64, operation Operation) PermissionDecision {
	cfg, err := p.provider.GetAgentConfig(ctx, userID)
	if err == nil {
		if decision, ok := cfg.Permissions[operation.Type]; ok {
			return decision
		}
	}
	return p.fallback.Check(ctx, userID, operation)
}
