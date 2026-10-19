// Package coder 提供从 opencode 迁移的编码类工具（bash / edit / view / write / patch /
// glob / grep / ls / fetch / sourcegraph / diagnostics）。
//
// 迁移约定：
//   - 全部实现 gobrave 的 tool.Tool（Definition + Execute），可直接注册进 tool.Registry；
//   - 工具的依赖（文件历史 / LSP 客户端 / 工作目录 / 会话 ID）在执行期从 ctx 读取，
//     因此工具本身无状态，可在注册表里单例复用；
//   - 权限请求与 opencode internal/llm/tools 对齐：工具在真正读写 / 执行前自行调用
//     tool.RequirePermission，由 ctx 中的 PermissionRequester 桥接到 Runtime。
package coder

import "github.com/biox-dev/gobrave/internal/agent/tool"

// All 返回全部 coder 工具。新增工具时在此追加即可自动接入各 Provider。
func All() []tool.Tool {
	return []tool.Tool{
		NewBash(),
		NewEdit(),
		NewView(),
		NewWrite(),
		NewPatch(),
		NewGlob(),
		NewGrep(),
		NewLs(),
		NewFetch(),
		NewSourcegraph(),
		NewDiagnostics(),
	}
}
