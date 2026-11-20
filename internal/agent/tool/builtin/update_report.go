package builtin

import (
	"context"
	"fmt"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	"github.com/biox-dev/gobrave/internal/agent/tool"
)

// UpdateReportToolName 是「更新项目报告」工具名。
const UpdateReportToolName = "update_report"

// UpdateReportInput 是 update_report 工具的入参。
//
// 支持两种互斥的编辑方式：
//   - 文本替换：old_string → new_string，要求原文在报告正文文件（output.md）中唯一出现；
//   - 字段覆盖：只给 title / content 即可，无需精确原文（空串字段表示保持不变）。
type UpdateReportInput struct {
	// ReportID 是报告主键 ID（支持 "@report:<id>" 形式，字符串以保精度）。
	ReportID  string `json:"report_id"`
	Title     string `json:"title,omitempty"`
	Content   string `json:"content,omitempty"`
	OldString string `json:"old_string,omitempty"`
	NewString string `json:"new_string,omitempty"`
}

// UpdateReport 返回「更新项目报告」工具。
//
// 与 skill.EditSkill 同构：文本替换模式在报告正文（output.md）上做一次唯一替换；
// 字段覆盖模式直接给新的标题 / 整篇正文。两种模式都会先计算写入计划并附带 unified diff，
// 经写授权后才写库 / 落盘。
func UpdateReport(writer runtimecontext.ProjectReportWriter) tool.Tool {
	return tool.NewFunc(
		UpdateReportToolName,
		"Update an existing project report. Choose exactly one mode. (1) Text replacement: pass report_id + old_string + new_string; old_string must appear exactly once in the report body file (output.md), so include enough surrounding context. (2) Field overwrite: pass report_id + title and/or content to replace the title / the whole markdown body without needing the original text (omitted/empty fields stay unchanged). Do not combine both modes. A diff is shown for approval before writing.",
		tool.Schema("更新项目报告的入参", map[string]any{
			"report_id":  tool.StringProperty("项目报告主键 ID（必填，整数，字符串以保精度）。来自 @report:<id> 引用时直接使用冒号后的 ID。"),
			"title":      tool.StringProperty("字段覆盖模式：新的报告标题（留空表示不变）。"),
			"content":    tool.StringProperty("字段覆盖模式：新的完整正文（整篇覆盖 output.md，留空表示不变）。"),
			"old_string": tool.StringProperty("文本替换模式：要替换的原文，必须在报告正文中唯一出现（含空白与换行需完全一致）。"),
			"new_string": tool.StringProperty("文本替换模式：替换后的文本。"),
		}, "report_id"),
		func(ctx context.Context, in UpdateReportInput) (ReportWriteOutput, error) {
			if writer == nil {
				return ReportWriteOutput{}, fmt.Errorf("%s: project report writer is not available in this runtime", UpdateReportToolName)
			}
			reportID, err := parseReportID(in.ReportID)
			if err != nil {
				return ReportWriteOutput{}, fmt.Errorf("%s: %w", UpdateReportToolName, err)
			}
			userID := tool.UserIDFrom(ctx)
			if userID <= 0 {
				return ReportWriteOutput{}, fmt.Errorf("%s: user_id is required in this runtime", UpdateReportToolName)
			}

			// 两种模式互斥：同时给原文和目标值会让「最终结果为哪一版」不可预测。
			fieldEdit := strings.TrimSpace(in.Title) != "" || strings.TrimSpace(in.Content) != ""
			var plan runtimecontext.ProjectReportWritePlan
			if fieldEdit {
				if strings.TrimSpace(in.OldString) != "" || strings.TrimSpace(in.NewString) != "" {
					return ReportWriteOutput{
						ReportID: reportID,
						Error:    "old_string/new_string cannot be combined with title/content: choose one edit mode",
					}, nil
				}
				plan, err = writer.PlanUpdate(ctx, runtimecontext.ProjectReportUpdateInput{
					UserID:   userID,
					ReportID: reportID,
					Title:    in.Title,
					Content:  in.Content,
				})
			} else {
				plan, err = writer.PlanEdit(ctx, runtimecontext.ProjectReportEditInput{
					UserID:    userID,
					ReportID:  reportID,
					OldString: in.OldString,
					NewString: in.NewString,
				})
			}
			if err != nil {
				return ReportWriteOutput{ReportID: reportID, Error: err.Error()}, nil
			}

			if err := tool.RequirePermission(ctx, tool.PermissionRequest{
				ToolName:    UpdateReportToolName,
				Action:      "write",
				Description: fmt.Sprintf("Update report %s", plan.Title),
				Params:      reportDiffPreview(plan),
			}); err != nil {
				return ReportWriteOutput{}, err
			}

			info, err := writer.Apply(ctx, plan)
			if err != nil {
				return ReportWriteOutput{ReportID: reportID, Error: err.Error()}, nil
			}
			return newReportWriteOutput(info), nil
		},
	)
}
