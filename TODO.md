## Container Event Ordering TODO

### P0 - 先止血（优先上线）

- [ ] AppSession 状态更新改为以 ContainerInstance 当前状态为准，而不是仅依赖事件名。
	- 位置：internal/manager/app_session_event_handler.go
	- 目标：即使事件处理顺序乱序，也不会出现 `STOPPED -> STOPPING` 这类状态回退。

- [ ] 增加 AppSession 状态“防回退”规则（单调迁移）。
	- 位置：internal/manager/app_session_event_handler.go
	- 规则建议：
		- 终态 `STOPPED/FAILED` 后，不允许被 `STOPPING/STARTING/CREATING` 覆盖。
		- `RUNNING` 后，不允许被 `STARTING/CREATING` 覆盖。

- [ ] 为 AppSessionEventHandler 增加乱序事件单测。
	- 位置：internal/manager/app_session_event_handler_test.go
	- 场景：先处理 stopped，再处理 stopping，最终状态应保持 stopped。

### P1 - 总线层顺序保证（根因治理）

- [ ] 将 MemoryBus 从“每次 Publish 为每个 handler 启 goroutine”改为“每个 handler 独立串行队列 + 单 worker goroutine”。
	- 位置：internal/event/bus.go
	- 目标：保证同一 handler 的事件处理顺序与 Publish 顺序一致。

- [ ] 为 MemoryBus 增加顺序性单测和并发回归测试。
	- 位置：internal/event/bus_test.go（新建）
	- 场景：连续发送 A/B/C，handler 观察到的顺序必须为 A/B/C。

### P2 - 幂等与重放防护（增强健壮性）

- [ ] 在 ContainerEvent 中引入可比较序号（建议复用 OutboxEvent.ID）。
	- 位置：internal/types/container.go、internal/manager/outbox_dispatcher.go

- [ ] AppSession 增加 `last_container_event_id` 字段，并在更新时做条件写入。
	- 位置：internal/types/container.go、internal/application/repository/container.go
	- 条件：仅当 `incoming_event_id > last_container_event_id` 才允许更新状态。

- [ ] 增加迁移脚本/自动迁移验证，确保历史数据兼容。
	- 位置：数据库迁移与 AutoMigrate 相关逻辑

### P2 - Outbox 恢复机制补全

- [ ] 实现 OutboxDispatcher.recoverStaleProcessing：将超时的 `processing` 事件恢复为 `pending`。
	- 位置：internal/manager/outbox_dispatcher.go
	- 背景：当前 recover 逻辑为占位实现，进程崩溃后可能遗留卡住事件。

- [ ] 为 stale processing recovery 增加配置项。
	- 建议项：`stale_processing_timeout`、`recovery_scan_interval`。
	- 位置：internal/config

- [ ] 增加恢复逻辑单测。
	- 场景：模拟事件卡在 processing 超时后可被恢复并再次消费。

### 验收标准

- [ ] 压测/并发测试下不再出现 AppSession 状态回退（如 stopped 后变 stopping）。
- [ ] 同一订阅者观察到的事件顺序稳定。
- [ ] 进程异常重启后，outbox 不会长期卡在 processing。
- [ ] 旧事件重放不会覆盖新状态。
