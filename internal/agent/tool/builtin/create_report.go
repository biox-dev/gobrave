package builtin

import (
	"context"
	"fmt"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	"github.com/biox-dev/gobrave/internal/agent/tool"
	"github.com/biox-dev/gobrave/internal/agent/tool/coder/diff"
)

// CreateReportToolName 是「创建项目报告」工具名。
const CreateReportToolName = "create_report"

// CreateReportInput 是 create_report 工具的入参。
//
// 目标项目不由模型提供：它取自执行事实（tool.ProjectIDFrom，当前用户激活项目）。
type CreateReportInput struct {
	Title string `json:"title"`
	// PlanID 可选：按某份报告大纲撰写（@plan:<id> 或 list_report_plans 得到）。
	PlanID  string `json:"plan_id,omitempty"`
	Content string `json:"content,omitempty"`
}

// ReportWriteOutput 是 create_report / update_report 工具返回给模型的结果。
type ReportWriteOutput struct {
	ReportID  int64  `json:"report_id,string,omitempty"`
	ProjectID int64  `json:"project_id,string,omitempty"`
	Title     string `json:"title,omitempty"`
	// FilePath 是报告正文文件的绝对路径（报告目录下的 output.md）。
	FilePath string `json:"file_path,omitempty"`
	Error    string `json:"error,omitempty"`
}

// CreateReport 返回「创建项目报告」工具。
//
// 与 skill.CreateSkill 同构：先在当前项目下新建报告记录（title），再把 body 写入该报告
// 工作目录下的 output.md（utils.GetProjectReportOutputFile）。写入会改变项目数据并落盘，
// 因此先请求写授权并附带 unified diff，批准后才真正写库 / 写文件。
func CreateReport(writer runtimecontext.ProjectReportWriter) tool.Tool {
	return tool.NewFunc(
		CreateReportToolName,
		"Create a project report in the CURRENT project: inserts a report record (title) and writes the report body to its markdown file (output.md). This tool ONLY writes report content; report structure (sections / which resources each section uses) is a report PLAN managed by create_report_plan / update_report_plan. For complex multi-session tasks, pass plan_id to bind the report to a plan and write the body section by section following that plan (use get_report_plan / list_session_resources to inspect sources). Pass the full markdown body in `content`; when omitted a `# <title>` heading is written. A diff is shown for approval before writing.",
		tool.Schema("创建项目报告的入参", map[string]any{
			"title":   tool.StringProperty("报告标题（必填）。"),
			"plan_id": tool.StringProperty("可选：按该报告大纲撰写（大纲 ID，来自 @plan:<id> 或 list_report_plans）。"),
			"content": tool.StringProperty("报告正文（markdown，写入 output.md）。留空时写入 \"# <title>\"。"),
		}, "title"),
		func(ctx context.Context, in CreateReportInput) (ReportWriteOutput, error) {
			if writer == nil {
				return ReportWriteOutput{}, fmt.Errorf("%s: project report writer is not available in this runtime", CreateReportToolName)
			}
			projectID, err := requireProjectID(ctx, CreateReportToolName)
			if err != nil {
				return ReportWriteOutput{}, err
			}
			userID := tool.UserIDFrom(ctx)
			if userID <= 0 {
				return ReportWriteOutput{}, fmt.Errorf("%s: user_id is required in this runtime", CreateReportToolName)
			}

			// 先算计划：校验入参并生成待写入正文（不写库 / 不落盘），供授权前展示 diff。
			var planID int64
			if strings.TrimSpace(in.PlanID) != "" {
				id, parseErr := parsePlanID(in.PlanID)
				if parseErr != nil {
					return ReportWriteOutput{Title: strings.TrimSpace(in.Title), Error: parseErr.Error()}, nil
				}
				planID = id
			}
			plan, err := writer.PlanCreate(ctx, runtimecontext.ProjectReportCreateInput{
				UserID:    userID,
				ProjectID: projectID,
				PlanID:    planID,
				Title:     in.Title,
				Content:   in.Content,
			})
			if err != nil {
				return ReportWriteOutput{Title: strings.TrimSpace(in.Title), Error: err.Error()}, nil
			}

			if err := tool.RequirePermission(ctx, tool.PermissionRequest{
				ToolName:    CreateReportToolName,
				Action:      "write",
				Description: fmt.Sprintf("Create report %s", plan.Title),
				Params:      reportDiffPreview(plan),
			}); err != nil {
				return ReportWriteOutput{}, err
			}

			info, err := writer.Apply(ctx, plan)
			if err != nil {
				return ReportWriteOutput{Title: plan.Title, Error: err.Error()}, nil
			}
			return newReportWriteOutput(info), nil
		},
	)
}

// newReportWriteOutput 把报告写入结果映射为工具结果。
func newReportWriteOutput(info runtimecontext.ProjectReportWriteInfo) ReportWriteOutput {
	return ReportWriteOutput{
		ReportID:  info.ReportID,
		ProjectID: info.ProjectID,
		Title:     info.Title,
		FilePath:  info.FilePath,
	}
}

// reportDiffPreview 把写入计划转为授权预览：file_path / diff 与 coder edit / write 的
// 授权参数同键（tool.PermissionPreview），上层会放进 Operation.Metadata 供前端渲染 diff。
func reportDiffPreview(plan runtimecontext.ProjectReportWritePlan) tool.PermissionPreview {
	text, _, _ := diff.GenerateDiff(plan.Before, plan.After, plan.FilePath)
	return tool.PermissionPreview{FilePath: plan.FilePath, Diff: text}
}

// parseReportID 解析必填的报告 ID：容错剥离用户 / 模型可能带上的 "@report:" 前缀。
func parseReportID(raw string) (int64, error) {
	idStr := strings.TrimSpace(raw)
	if idx := strings.Index(idStr, "@report:"); idx >= 0 {
		idStr = strings.TrimSpace(idStr[idx+len("@report:"):])
	}
	return parsePositiveID(idStr, "report_id")
}
