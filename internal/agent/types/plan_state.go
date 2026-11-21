package types

import "time"

// PlanItemStatus 是计划节点的执行状态（由 agent 在执行过程中上报）。
type PlanItemStatus string

const (
	// PlanItemPending 表示尚未开始。
	PlanItemPending PlanItemStatus = "pending"
	// PlanItemInProgress 表示正在执行。
	PlanItemInProgress PlanItemStatus = "in_progress"
	// PlanItemCompleted 表示已完成。
	PlanItemCompleted PlanItemStatus = "completed"
	// PlanItemSkipped 表示跳过（例如用户取消 / 前置条件不满足）。
	PlanItemSkipped PlanItemStatus = "skipped"
)

// Valid 状态是否是受支持的取值。
func (s PlanItemStatus) Valid() bool {
	switch s {
	case PlanItemPending, PlanItemInProgress, PlanItemCompleted, PlanItemSkipped:
		return true
	default:
		return false
	}
}

// AgentPlanItemState 是计划节点的运行态（agent_plan_item_states）。
//
// 计划结构（AgentPlan.Items）可被用户整体覆盖，而运行态是「执行过程的事实」：
// 二者分开存放，结构更新不会把进度打回 pending。状态是**稀疏**的——没有记录的
// 节点一律视为 pending，因此每步上报只写变化的那几行。
type AgentPlanItemState struct {
	PlanID int64          `json:"plan_id,string" gorm:"column:plan_id;type:bigint;primaryKey;autoIncrement:false"`
	ItemID int64          `json:"item_id,string" gorm:"column:item_id;type:bigint;primaryKey;autoIncrement:false"`
	Status PlanItemStatus `json:"status" gorm:"column:status;type:varchar(32);not null"`
	// SessionID 是推进该状态所在的会话，便于多会话协作时追溯「谁做的」。
	SessionID int64 `json:"session_id,string,omitempty" gorm:"column:session_id;type:bigint;index"`
	// Note 是 agent 对本次状态变化的简短说明（可选）。
	Note      string    `json:"note,omitempty" gorm:"column:note;type:text"`
	UpdatedAt time.Time `json:"updated_at" gorm:"column:updated_at"`
}

// TableName 返回计划节点运行态表的表名。
func (AgentPlanItemState) TableName() string { return "agent_plan_item_states" }

// PlanItemStatusUpdate 是一次节点状态上报。
type PlanItemStatusUpdate struct {
	ItemID int64          `json:"item_id,string"`
	Status PlanItemStatus `json:"status"`
	Note   string         `json:"note,omitempty"`
}
