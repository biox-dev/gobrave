package providers

import (
	"context"
	"fmt"

	"github.com/biox-dev/gobrave/internal/agent"
)

// notImplementedProvider 是尚未实现真实调用的 Provider 占位实现。
// 后续为某个 Provider 接入真实调用时，用具体实现替换对应构造函数即可。
type notImplementedProvider struct {
	name string
}

func (p notImplementedProvider) Name() string { return p.name }

func (p notImplementedProvider) New(opts agent.Options) (agent.Agent, error) {
	return &notImplementedAgent{name: p.name, opts: opts}, nil
}

type notImplementedAgent struct {
	name string
	opts agent.Options
}

func (a *notImplementedAgent) Name() string { return a.name }

func (a *notImplementedAgent) Invoke(_ context.Context, _ agent.Request) (*agent.Result, error) {
	return nil, fmt.Errorf("%w: provider=%s", agent.ErrNotImplemented, a.name)
}

func (a *notImplementedAgent) Stream(_ context.Context, _ agent.Request, _ agent.StreamHandler) (*agent.Result, error) {
	return nil, fmt.Errorf("%w: provider=%s", agent.ErrNotImplemented, a.name)
}
