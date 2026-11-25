package types

import "time"

// SessionSummary 是会话历史的压缩摘要（每个会话至多一条）。
//
// 它只服务「拼装模型 prompt」：agent_messages 仍是唯一事实源，UI 历史、恢复执行
// 与 fork 都不依赖摘要。UpToSeq 是水位——Seq 不大于它的消息已被 Content 覆盖，
// 下一轮只需拼接「摘要 + 水位之后的原文」。
type SessionSummary struct {
	SessionID int64 `json:"session_id,string" gorm:"column:session_id;primaryKey;type:bigint;autoIncrement:false"`
	// UpToSeq 是已摘要到的最后一条 agent_messages 的 Seq；只增不减。
	UpToSeq int `json:"up_to_seq" gorm:"column:up_to_seq"`
	// Content 是压缩后的摘要正文（不含注入时使用的包裹标记）。
	Content string `json:"content" gorm:"column:content;type:longtext"`
	// Model 是生成摘要使用的模型，仅用于排障 / 统计。
	Model     string    `json:"model,omitempty" gorm:"column:model;type:varchar(128)"`
	CreatedAt time.Time `json:"created_at" gorm:"column:created_at"`
	UpdatedAt time.Time `json:"updated_at" gorm:"column:updated_at"`
}

// TableName 返回摘要表的表名。
func (SessionSummary) TableName() string { return "agent_session_summaries" }
