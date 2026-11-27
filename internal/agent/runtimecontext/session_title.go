package runtimecontext

import "context"

// 本文件定义「更新当前会话标题」端口，供 builtin 的 update_session_title 使用。
//
// 更新目标固定为当前执行会话（由 tool.SessionIDFrom 提供），不接受模型填写任意
// session_id：会话属于某个用户 / 项目，放开 ID 会带来越权改名风险。实现侧仍会按
// userID 做一次归属校验，作为纵深防御。

// SessionTitleUpdater 抽象「更新当前会话标题」的能力。
//
// 与其它写能力一样，它是进程级静态依赖，由 container 装配后经 builtin.Deps 注入；
// 会话 / 用户取自执行事实（tool.SessionIDFrom / tool.UserIDFrom）。
type SessionTitleUpdater interface {
	// UpdateSessionTitle 把会话标题更新为 title，返回更新后的会话摘要。
	//
	// userID > 0 且与会话归属不一致时返回错误；会话不存在时返回错误。
	// 实现负责在成功后把 sessionTitleUpdated 事件推给该用户的在线前端，
	// 使会话列表无需刷新即可显示新标题。
	UpdateSessionTitle(ctx context.Context, sessionID, userID int64, title string) (SessionTitleUpdateResult, error)
}

// SessionTitleUpdateResult 是一次会话改名的结果。
type SessionTitleUpdateResult struct {
	SessionID int64
	Title     string
}
