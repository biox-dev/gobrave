// Package repository 提供 agent 域各持久化端口的基于数据库（GORM）的实现。
//
// 架构说明：
//   - 持久化端口（TaskRepository / PermissionRepository / QuestionRepository /
//     MemoryRepository / ProfileRepository / SessionStore / MessageStore / HistoryStore）
//     统一定义在 internal/agent/types/interface 中；领域实体在 internal/agent/types。
//   - 本包作为适配器层，把这些端口的实现落到数据库，单向依赖 types 与 types/interface，
//     不反向依赖 application/service，从而避免循环依赖。
package repository
