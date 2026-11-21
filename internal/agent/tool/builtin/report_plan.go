package builtin

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/tool"
	agenttypes "github.com/biox-dev/gobrave/internal/agent/types"
)

// reportPlanService 是报告大纲读写能力的窄端口（由 agent 的 ReportPlanService 实现）。
//
// 与 Deps 里其它能力一致：这里只声明工具用到的方法，测试 fake 只需实现这几项。
type reportPlanService interface {
	Save(ctx context.Context, plan *agenttypes.ReportPlan) (*agenttypes.ReportPlan, error)
	Get(ctx context.Context, id int64) (*agenttypes.ReportPlan, error)
	ListByProject(ctx context.Context, projectID int64) ([]*agenttypes.ReportPlan, error)
}

// 报告大纲（plan）工具名。
//
// 大纲是「跨会话共享的报告结构」：复杂任务由多个会话产出资源，再由独立的报告撰写
// 会话按大纲写正文。因此大纲由一组独立的 CRUD 工具管理，而不是由 create_report 内嵌。
const (
	CreateReportPlanToolName = "create_report_plan"
	ListReportPlansToolName  = "list_report_plans"
	GetReportPlanToolName    = "get_report_plan"
	UpdateReportPlanToolName = "update_report_plan"
)

// ReportPlanSectionInput 是大纲章节的工具入参。
type ReportPlanSectionInput struct {
	// SectionID 可为空：新章节留空由后端生成，更新已有章节时传 get_report_plan 返回的 id。
	SectionID string `json:"section_id,omitempty"`
	ParentID  string `json:"parent_id,omitempty"`
	Title     string `json:"title"`
	Summary   string `json:"summary,omitempty"`
	// Source 取 custom / analysis / analysis_node / script / workflow，与会话资源类型一致。
	Source    string `json:"source,omitempty"`
	SourceID  string `json:"source_id,omitempty"`
	SortOrder int    `json:"sort_order,omitempty"`
}

// ReportPlanOutput 是大纲工具返回给模型的结果。
type ReportPlanOutput struct {
	PlanID    int64                          `json:"plan_id,string,omitempty"`
	ProjectID int64                          `json:"project_id,string,omitempty"`
	Title     string                         `json:"title,omitempty"`
	Goal      string                         `json:"goal,omitempty"`
	Sections  []agenttypes.ReportPlanSection `json:"sections,omitempty"`
	Plans     []ReportPlanBrief              `json:"plans,omitempty"`
	Error     string                         `json:"error,omitempty"`
}

// ReportPlanBrief 是大纲列表项的只读摘要。
type ReportPlanBrief struct {
	PlanID     int64  `json:"plan_id,string"`
	Title      string `json:"title"`
	Goal       string `json:"goal,omitempty"`
	SectionNum int    `json:"section_num"`
}

// CreateReportPlan 创建报告大纲（也可用于一次性写入完整章节树）。
func CreateReportPlan(svc reportPlanService) tool.Tool {
	return tool.NewFunc(
		CreateReportPlanToolName,
		"Create a report plan (outline) in the CURRENT project. A plan describes which sections the report must contain and which session resources each section is based on. Pass the full section list in `sections`; each section has title / summary / source (custom | analysis | analysis_node | script | workflow) / source_id / parent_id / sort_order. Use this for complex tasks where several sessions produce results and the report is written later in a dedicated session. Call list_session_resources first to get real resource IDs.",
		tool.Schema("创建报告大纲的入参", map[string]any{
			"title":    tool.StringProperty("大纲标题（必填）。"),
			"goal":     tool.StringProperty("报告目标 / 写作要求（可选，交给撰写会话）。"),
			"sections": planSectionsProperty(),
		}, "title"),
		func(ctx context.Context, in reportPlanWriteInput) (ReportPlanOutput, error) {
			return savePlanFromInput(ctx, svc, CreateReportPlanToolName, 0, in)
		},
	).WithPermission("write", "", "创建项目报告大纲")
}

// UpdateReportPlan 整体覆盖一份已有大纲（章节树以本次入参为准）。
func UpdateReportPlan(svc reportPlanService) tool.Tool {
	return tool.NewFunc(
		UpdateReportPlanToolName,
		"Overwrite an existing report plan. Pass plan_id plus the COMPLETE section list you want the plan to have (sections not included are removed). Keep section_id for sections that already exist so their identity (and user edits) are preserved.",
		tool.Schema("更新报告大纲的入参", map[string]any{
			"plan_id":  tool.StringProperty("大纲 ID（必填）。"),
			"title":    tool.StringProperty("大纲标题（必填）。"),
			"goal":     tool.StringProperty("报告目标 / 写作要求。"),
			"sections": planSectionsProperty(),
		}, "plan_id", "title"),
		func(ctx context.Context, in reportPlanWriteInput) (ReportPlanOutput, error) {
			planID, err := parsePositiveID(in.PlanID, "plan_id")
			if err != nil {
				return ReportPlanOutput{Error: err.Error()}, nil
			}
			return savePlanFromInput(ctx, svc, UpdateReportPlanToolName, planID, in)
		},
	).WithPermission("write", "", "更新项目报告大纲")
}

// GetReportPlan 读取单份大纲及其章节树。
func GetReportPlan(svc reportPlanService) tool.Tool {
	return tool.NewFunc(
		GetReportPlanToolName,
		"Get one report plan (outline) by plan_id: title, goal and the section tree with source / source_id. Call this before writing a report that is bound to a plan, then read the referenced resource results for each section.",
		tool.Schema("读取报告大纲的入参", map[string]any{
			"plan_id": tool.StringProperty("大纲 ID（必填，支持 @plan:<id> 形式）。"),
		}, "plan_id"),
		func(ctx context.Context, in reportPlanReadInput) (ReportPlanOutput, error) {
			planID, err := parsePlanID(in.PlanID)
			if err != nil {
				return ReportPlanOutput{}, fmt.Errorf("%s: %w", GetReportPlanToolName, err)
			}
			if svc == nil {
				return ReportPlanOutput{}, fmt.Errorf("%s: report plan service is not available", GetReportPlanToolName)
			}
			plan, err := svc.Get(ctx, planID)
			if err != nil {
				return ReportPlanOutput{PlanID: planID, Error: err.Error()}, nil
			}
			return ReportPlanOutput{
				PlanID:    plan.ID,
				ProjectID: plan.ProjectID,
				Title:     plan.Title,
				Goal:      plan.Goal,
				Sections:  plan.Sections,
			}, nil
		},
	).WithPermission("read", "", "读取报告大纲")
}

// ListReportPlans 列出当前项目的大纲。
func ListReportPlans(svc reportPlanService) tool.Tool {
	return tool.NewFunc(
		ListReportPlansToolName,
		"List report plans of the CURRENT project (id, title, goal, section count). Use it to find the plan a user refers to before writing a report.",
		tool.Schema("列出报告大纲的入参", map[string]any{}),
		func(ctx context.Context, _ struct{}) (ReportPlanOutput, error) {
			projectID, err := requireProjectID(ctx, ListReportPlansToolName)
			if err != nil {
				return ReportPlanOutput{}, err
			}
			if svc == nil {
				return ReportPlanOutput{}, fmt.Errorf("%s: report plan service is not available", ListReportPlansToolName)
			}
			plans, err := svc.ListByProject(ctx, projectID)
			if err != nil {
				return ReportPlanOutput{}, fmt.Errorf("%s: %w", ListReportPlansToolName, err)
			}
			out := make([]ReportPlanBrief, 0, len(plans))
			for _, plan := range plans {
				if plan == nil {
					continue
				}
				out = append(out, ReportPlanBrief{
					PlanID:     plan.ID,
					Title:      plan.Title,
					Goal:       plan.Goal,
					SectionNum: len(plan.Sections),
				})
			}
			return ReportPlanOutput{ProjectID: projectID, Plans: out}, nil
		},
	).WithPermission("read", "", "列出项目报告大纲")
}

type reportPlanWriteInput struct {
	PlanID   string                   `json:"plan_id,omitempty"`
	Title    string                   `json:"title"`
	Goal     string                   `json:"goal,omitempty"`
	Sections []ReportPlanSectionInput `json:"sections,omitempty"`
}

type reportPlanReadInput struct {
	PlanID string `json:"plan_id"`
}

// savePlanFromInput 把工具入参转为大纲实体并保存（新建或覆盖）。
func savePlanFromInput(ctx context.Context, svc reportPlanService, toolName string, planID int64, in reportPlanWriteInput) (ReportPlanOutput, error) {
	if svc == nil {
		return ReportPlanOutput{}, fmt.Errorf("%s: report plan service is not available", toolName)
	}
	projectID, err := requireProjectID(ctx, toolName)
	if err != nil {
		return ReportPlanOutput{}, err
	}

	sections, err := toPlanSections(in.Sections)
	if err != nil {
		return ReportPlanOutput{Title: strings.TrimSpace(in.Title), Error: err.Error()}, nil
	}

	plan, err := svc.Save(ctx, &agenttypes.ReportPlan{
		ID:        planID,
		ProjectID: projectID,
		Title:     in.Title,
		Goal:      in.Goal,
		Sections:  sections,
	})
	if err != nil {
		return ReportPlanOutput{Title: strings.TrimSpace(in.Title), Error: err.Error()}, nil
	}
	return ReportPlanOutput{
		PlanID:    plan.ID,
		ProjectID: plan.ProjectID,
		Title:     plan.Title,
		Goal:      plan.Goal,
		Sections:  plan.Sections,
	}, nil
}

// toPlanSections 解析工具入参的章节 ID 字段（字符串→int64，保精度）。
func toPlanSections(in []ReportPlanSectionInput) ([]agenttypes.ReportPlanSection, error) {
	if len(in) == 0 {
		return nil, nil
	}
	out := make([]agenttypes.ReportPlanSection, 0, len(in))
	for _, s := range in {
		section := agenttypes.ReportPlanSection{
			Title:     s.Title,
			Summary:   s.Summary,
			Source:    agenttypes.ReportPlanSource(strings.ToLower(strings.TrimSpace(s.Source))),
			SortOrder: s.SortOrder,
		}
		if strings.TrimSpace(s.SectionID) != "" {
			id, err := parsePositiveID(s.SectionID, "section_id")
			if err != nil {
				return nil, err
			}
			section.ID = id
		}
		if strings.TrimSpace(s.ParentID) != "" {
			id, err := parsePositiveID(s.ParentID, "parent_id")
			if err != nil {
				return nil, err
			}
			section.ParentID = id
		}
		if strings.TrimSpace(s.SourceID) != "" {
			id, err := parsePositiveID(s.SourceID, "source_id")
			if err != nil {
				return nil, err
			}
			section.SourceID = id
		}
		out = append(out, section)
	}
	return out, nil
}

// planSectionsProperty 声明章节数组的 JSON Schema。
func planSectionsProperty() map[string]any {
	return map[string]any{
		"type":        "array",
		"description": "大纲章节列表（扁平结构，用 parent_id 表示层级、sort_order 表示同级顺序）。",
		"items": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"section_id": tool.StringProperty("章节 ID（新章节留空，更新已有章节时回传原 ID）。"),
				"parent_id":  tool.StringProperty("父章节 ID（根章节留空）。"),
				"title":      tool.StringProperty("章节标题（必填）。"),
				"summary":    tool.StringProperty("该章节要写什么（可选，给撰写会话的提示）。"),
				"source":     tool.StringProperty("内容来源：custom（自定义）| analysis | analysis_node | script | workflow。"),
				"source_id":  tool.StringProperty("来源资源 ID（source=custom 时留空）。来自 list_session_resources。"),
				"sort_order": map[string]any{"type": "integer", "description": "同级顺序（从 0 开始）。"},
			},
			"required": []string{"title"},
		},
	}
}

// parsePlanID 解析大纲 ID，容错剥离 @plan: 前缀。
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
