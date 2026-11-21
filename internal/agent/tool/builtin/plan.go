package builtin

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/tool"
	agenttypes "github.com/biox-dev/gobrave/internal/agent/types"
)

// planService 是计划读写能力的窄端口（由 agent 的 PlanService 实现）。
//
// 与 Deps 里其它能力一致：这里只声明工具用到的方法，测试 fake 只需实现这几项。
type planService interface {
	Save(ctx context.Context, plan *agenttypes.AgentPlan) (*agenttypes.AgentPlan, error)
	Get(ctx context.Context, id int64) (*agenttypes.AgentPlan, error)
	ListByProject(ctx context.Context, projectID int64) ([]*agenttypes.AgentPlan, error)
	Delete(ctx context.Context, id int64) error
}

// 计划（plan）工具名。
//
// 计划是 agent 的通用能力：一组按树组织的步骤，每步可引用会话产出的资源 ID。
// agent 可以跨会话查询计划，再按节点来源 ID 去查询资源详情；撰写报告只是其中一种用法。
const (
	CreatePlanToolName = "create_plan"
	ListPlansToolName  = "list_plans"
	GetPlanToolName    = "get_plan"
	UpdatePlanToolName = "update_plan"
	DeletePlanToolName = "delete_plan"
)

// PlanItemInput 是计划节点的工具入参。
type PlanItemInput struct {
	// ItemID 可为空：新节点留空由后端生成，更新已有节点时传 get_plan 返回的 id。
	ItemID   string `json:"item_id,omitempty"`
	ParentID string `json:"parent_id,omitempty"`
	Title    string `json:"title"`
	Summary  string `json:"summary,omitempty"`
	// Source 取 custom / analysis / analysis_node / script / workflow，与会话资源类型一致。
	Source    string `json:"source,omitempty"`
	SourceID  string `json:"source_id,omitempty"`
	SortOrder int    `json:"sort_order,omitempty"`
}

// PlanOutput 是计划工具返回给模型的结果。
type PlanOutput struct {
	PlanID    int64                 `json:"plan_id,string,omitempty"`
	ProjectID int64                 `json:"project_id,string,omitempty"`
	SessionID int64                 `json:"session_id,string,omitempty"`
	Title     string                `json:"title,omitempty"`
	Goal      string                `json:"goal,omitempty"`
	Items     []agenttypes.PlanItem `json:"items,omitempty"`
	Plans     []PlanBrief           `json:"plans,omitempty"`
	Error     string                `json:"error,omitempty"`
}

// PlanBrief 是计划列表项的只读摘要。
type PlanBrief struct {
	PlanID  int64  `json:"plan_id,string"`
	Title   string `json:"title"`
	Goal    string `json:"goal,omitempty"`
	ItemNum int    `json:"item_num"`
}

// CreatePlan 创建计划（也可一次性写入完整节点树）。
func CreatePlan(svc planService) tool.Tool {
	return tool.NewFunc(
		CreatePlanToolName,
		"Create a plan in the CURRENT project: a tree of steps (items) where each step may reference a session resource by source + source_id. The plan stores NO resource details — only titles and resource IDs; use get_plan to read it back and then fetch each resource with its own tool. Call list_session_resources first to get real resource IDs. Use it for multi-session work; a report can later be written from this plan via create_report(plan_id).",
		tool.Schema("创建计划的入参", map[string]any{
			"title": tool.StringProperty("计划标题（必填）。"),
			"goal":  tool.StringProperty("计划目标 / 执行要求（可选）。"),
			"items": planItemsProperty(),
		}, "title"),
		func(ctx context.Context, in planWriteInput) (PlanOutput, error) {
			return savePlanFromInput(ctx, svc, CreatePlanToolName, 0, in)
		},
	).WithPermission("write", "", "创建 agent 计划")
}

// UpdatePlan 整体覆盖一份已有计划（节点树以本次入参为准）。
func UpdatePlan(svc planService) tool.Tool {
	return tool.NewFunc(
		UpdatePlanToolName,
		"Overwrite an existing plan. Pass plan_id plus the COMPLETE item list you want the plan to have (items not included are removed). Keep item_id for items that already exist so their identity (and user edits) are preserved.",
		tool.Schema("更新计划的入参", map[string]any{
			"plan_id": tool.StringProperty("计划 ID（必填）。"),
			"title":   tool.StringProperty("计划标题（必填）。"),
			"goal":    tool.StringProperty("计划目标 / 执行要求。"),
			"items":   planItemsProperty(),
		}, "plan_id", "title"),
		func(ctx context.Context, in planWriteInput) (PlanOutput, error) {
			planID, err := parsePositiveID(in.PlanID, "plan_id")
			if err != nil {
				return PlanOutput{Error: err.Error()}, nil
			}
			return savePlanFromInput(ctx, svc, UpdatePlanToolName, planID, in)
		},
	).WithPermission("write", "", "更新 agent 计划")
}

// GetPlan 读取单份计划及其节点树。
func GetPlan(svc planService) tool.Tool {
	return tool.NewFunc(
		GetPlanToolName,
		"Get one plan by plan_id: title, goal and the item tree with source / source_id. Items only carry resource IDs, never resource details — fetch each referenced resource with its own tool (get_analysis_node / get_script / get_workflow / get_analysis_workflow) when you need the actual results.",
		tool.Schema("读取计划的入参", map[string]any{
			"plan_id": tool.StringProperty("计划 ID（必填，支持 @plan:<id> 形式）。"),
		}, "plan_id"),
		func(ctx context.Context, in planReadInput) (PlanOutput, error) {
			planID, err := parsePlanID(in.PlanID)
			if err != nil {
				return PlanOutput{}, fmt.Errorf("%s: %w", GetPlanToolName, err)
			}
			if svc == nil {
				return PlanOutput{}, fmt.Errorf("%s: plan service is not available", GetPlanToolName)
			}
			plan, err := svc.Get(ctx, planID)
			if err != nil {
				return PlanOutput{PlanID: planID, Error: err.Error()}, nil
			}
			return newPlanOutput(plan), nil
		},
	).WithPermission("read", "", "读取 agent 计划")
}

// ListPlans 列出当前项目的计划。
func ListPlans(svc planService) tool.Tool {
	return tool.NewFunc(
		ListPlansToolName,
		"List plans of the CURRENT project (id, title, goal, item count). Use it to find the plan a user refers to before continuing work or writing a report from it.",
		tool.Schema("列出计划的入参", map[string]any{}),
		func(ctx context.Context, _ struct{}) (PlanOutput, error) {
			projectID, err := requireProjectID(ctx, ListPlansToolName)
			if err != nil {
				return PlanOutput{}, err
			}
			if svc == nil {
				return PlanOutput{}, fmt.Errorf("%s: plan service is not available", ListPlansToolName)
			}
			plans, err := svc.ListByProject(ctx, projectID)
			if err != nil {
				return PlanOutput{}, fmt.Errorf("%s: %w", ListPlansToolName, err)
			}
			out := make([]PlanBrief, 0, len(plans))
			for _, plan := range plans {
				if plan == nil {
					continue
				}
				out = append(out, PlanBrief{
					PlanID:  plan.ID,
					Title:   plan.Title,
					Goal:    plan.Goal,
					ItemNum: len(plan.Items),
				})
			}
			return PlanOutput{ProjectID: projectID, Plans: out}, nil
		},
	).WithPermission("read", "", "列出 agent 计划")
}

// DeletePlan 删除一份计划。已引用该计划的报告不会被删除，只保留 PlanID 引用。
func DeletePlan(svc planService) tool.Tool {
	return tool.NewFunc(
		DeletePlanToolName,
		"Delete a plan by plan_id. Reports that referenced it are kept (their plan_id simply points to a removed plan). Confirm with the user before deleting.",
		tool.Schema("删除计划的入参", map[string]any{
			"plan_id": tool.StringProperty("计划 ID（必填）。"),
		}, "plan_id"),
		func(ctx context.Context, in planReadInput) (PlanOutput, error) {
			planID, err := parsePlanID(in.PlanID)
			if err != nil {
				return PlanOutput{}, fmt.Errorf("%s: %w", DeletePlanToolName, err)
			}
			if svc == nil {
				return PlanOutput{}, fmt.Errorf("%s: plan service is not available", DeletePlanToolName)
			}
			if err := svc.Delete(ctx, planID); err != nil {
				return PlanOutput{PlanID: planID, Error: err.Error()}, nil
			}
			return PlanOutput{PlanID: planID}, nil
		},
	).WithPermission("write", "", "删除 agent 计划")
}

type planWriteInput struct {
	PlanID string          `json:"plan_id,omitempty"`
	Title  string          `json:"title"`
	Goal   string          `json:"goal,omitempty"`
	Items  []PlanItemInput `json:"items,omitempty"`
}

type planReadInput struct {
	PlanID string `json:"plan_id"`
}

// newPlanOutput 把计划实体转换为工具结果。
func newPlanOutput(plan *agenttypes.AgentPlan) PlanOutput {
	if plan == nil {
		return PlanOutput{}
	}
	return PlanOutput{
		PlanID:    plan.ID,
		ProjectID: plan.ProjectID,
		SessionID: plan.SessionID,
		Title:     plan.Title,
		Goal:      plan.Goal,
		Items:     plan.Items,
	}
}

// savePlanFromInput 把工具入参转为计划实体并保存（新建或覆盖）。
func savePlanFromInput(ctx context.Context, svc planService, toolName string, planID int64, in planWriteInput) (PlanOutput, error) {
	if svc == nil {
		return PlanOutput{}, fmt.Errorf("%s: plan service is not available", toolName)
	}
	projectID, err := requireProjectID(ctx, toolName)
	if err != nil {
		return PlanOutput{}, err
	}

	items, err := toPlanItems(in.Items)
	if err != nil {
		return PlanOutput{Title: strings.TrimSpace(in.Title), Error: err.Error()}, nil
	}

	plan, err := svc.Save(ctx, &agenttypes.AgentPlan{
		ID:        planID,
		ProjectID: projectID,
		// 记录创建计划时所在的会话，便于追溯「这个计划由哪个会话产生」。
		SessionID: tool.SessionIDFrom(ctx),
		Title:     in.Title,
		Goal:      in.Goal,
		Items:     items,
	})
	if err != nil {
		return PlanOutput{Title: strings.TrimSpace(in.Title), Error: err.Error()}, nil
	}
	return newPlanOutput(plan), nil
}

// toPlanItems 解析工具入参的节点 ID 字段（字符串→int64，保精度）。
func toPlanItems(in []PlanItemInput) ([]agenttypes.PlanItem, error) {
	if len(in) == 0 {
		return nil, nil
	}
	out := make([]agenttypes.PlanItem, 0, len(in))
	for _, item := range in {
		step := agenttypes.PlanItem{
			Title:     item.Title,
			Summary:   item.Summary,
			Source:    agenttypes.PlanSource(strings.ToLower(strings.TrimSpace(item.Source))),
			SortOrder: item.SortOrder,
		}
		if strings.TrimSpace(item.ItemID) != "" {
			id, err := parsePositiveID(item.ItemID, "item_id")
			if err != nil {
				return nil, err
			}
			step.ID = id
		}
		if strings.TrimSpace(item.ParentID) != "" {
			id, err := parsePositiveID(item.ParentID, "parent_id")
			if err != nil {
				return nil, err
			}
			step.ParentID = id
		}
		if strings.TrimSpace(item.SourceID) != "" {
			id, err := parsePositiveID(item.SourceID, "source_id")
			if err != nil {
				return nil, err
			}
			step.SourceID = id
		}
		out = append(out, step)
	}
	return out, nil
}

// planItemsProperty 声明计划节点数组的 JSON Schema。
func planItemsProperty() map[string]any {
	return map[string]any{
		"type":        "array",
		"description": "计划节点列表（扁平结构，用 parent_id 表示层级、sort_order 表示同级顺序）。",
		"items": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"item_id":    tool.StringProperty("节点 ID（新节点留空，更新已有节点时回传原 ID）。"),
				"parent_id":  tool.StringProperty("父节点 ID（根节点留空）。"),
				"title":      tool.StringProperty("节点标题（必填）。"),
				"summary":    tool.StringProperty("该步骤要做什么（可选）。"),
				"source":     tool.StringProperty("来源：custom（无资源）| analysis | analysis_node | script | workflow。"),
				"source_id":  tool.StringProperty("来源资源 ID（source=custom 时留空）。来自 list_session_resources。"),
				"sort_order": map[string]any{"type": "integer", "description": "同级顺序（从 0 开始）。"},
			},
			"required": []string{"title"},
		},
	}
}

// parsePlanID 解析计划 ID，容错剥离 @plan: 前缀。
func parsePlanID(raw string) (int64, error) {
	idStr := strings.TrimSpace(raw)
	if idx := strings.Index(idStr, "@plan:"); idx >= 0 {
		idStr = strings.TrimSpace(idStr[idx+len("@plan:"):])
	}
	return parsePositiveID(idStr, "plan_id")
}

// parsePositiveID 解析必填的正整数 ID。
func parsePositiveID(raw, field string) (int64, error) {
	idStr := strings.TrimSpace(raw)
	if idStr == "" {
		return 0, fmt.Errorf("%s is required", field)
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid %s %q: %w", field, idStr, err)
	}
	if id <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", field)
	}
	return id, nil
}
