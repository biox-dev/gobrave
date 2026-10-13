// Package repository 提供 agent 域各持久化端口的基于数据库（GORM）的实现。
//
// 架构说明：
//   - 持久化端口（TaskRepository / PermissionRepository / QuestionRepository /
//     EventRepository / ConversationRepository / MemoryRepository / ProfileRepository 接口）
//     定义在 agent 包中，由消费方（AgentService 等编排层）持有，遵循「接口定义在消费侧」。
//   - 本包作为适配器层，把这些端口的实现落到数据库。因为实现需要引用 agent 包的领域类型
//     （Task / AgentEvent / PermissionRequest ...），所以本包单向依赖 agent 包；agent 包
//     不反向依赖本包，从而避免循环依赖。
package repository
