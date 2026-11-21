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

// PlanItem 是计划中的一个节点（可嵌套），对应 agent_plan_items 的一行。
//
// ParentID 指向同一计划内的父节点（0 表示根），SortOrder 为同级顺序：前端据此渲染
// 可拖拽的树，agent 据此按顺序执行 / 撰写。
//
// 结构与运行态在**同一行**、按列更新：update_plan 只写结构列（标题 / 来源 / 排序 /
// 父子），report_plan_progress 只写 status 列，因此用户编辑计划不会清掉执行进度。
type PlanItem struct {
	// ID 是节点主键（雪花 ID），也是「执行结果 ↔ 计划节点」关联的引用目标。
	ID        int64      `json:"id,string" gorm:"column:id;primaryKey;type:bigint;autoIncrement:false"`
	PlanID    int64      `json:"plan_id,string" gorm:"column:plan_id;type:bigint;not null;index"`
	ParentID  int64      `json:"parent_id,string" gorm:"column:parent_id;type:bigint;not null"`
	Title     string     `json:"title" gorm:"column:title;type:varchar(255);not null"`
	Summary   string     `json:"summary,omitempty" gorm:"column:summary;type:text"`
	Source    PlanSource `json:"source" gorm:"column:source;type:varchar(32)"`
	SourceID  int64      `json:"source_id,string" gorm:"column:source_id;type:bigint"`
	SortOrder int        `json:"sort_order" gorm:"column:sort_order;type:int;not null"`
	// Status 是节点执行状态（空值等价于 pending）。
	Status PlanItemStatus `json:"status" gorm:"column:status;type:varchar(32);not null;default:pending"`
	// SessionID 是推进该状态所在的会话，便于多会话协作时追溯「谁做的」。
	SessionID int64 `json:"session_id,string,omitempty" gorm:"column:session_id;type:bigint;index"`
	// Note 是 agent 对本次状态变化的简短说明（可选）。
	Note      string    `json:"note,omitempty" gorm:"column:note;type:text"`
	CreatedAt time.Time `json:"created_at" gorm:"column:created_at"`
	UpdatedAt time.Time `json:"updated_at" gorm:"column:updated_at"`
}

// TableName 返回计划节点表的表名。
func (PlanItem) TableName() string { return "agent_plan_items" }

// PlanItemStatusUpdate 是一次节点状态上报。
type PlanItemStatusUpdate struct {
	ItemID int64          `json:"item_id,string"`
	Status PlanItemStatus `json:"status"`
	Note   string         `json:"note,omitempty"`
}
