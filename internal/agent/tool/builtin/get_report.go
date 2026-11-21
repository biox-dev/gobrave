package builtin

import (
	"context"
	"fmt"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	"github.com/biox-dev/gobrave/internal/agent/tool"
)

// GetReportToolName 是「按 ID 查看项目报告」工具名。
const GetReportToolName = "get_report"

// GetReportInput 是 get_report 工具的入参。
//
// ReportID 对应用户消息里的 @report:<id> 引用（如 "@report:2109711590457610240"），
// 它是项目报告主键 ID（t_project_report.id），必填。
type GetReportInput struct {
	ReportID string `json:"report_id"` // 项目报告主键 ID（支持 "@report:<id>" 形式，字符串以保精度）
}

// GetReportOutput 是 get_report 工具返回给模型的结果。
//
// 直接内嵌 runtimecontext.ProjectReportDetail（标题 / 正文文件路径 / 正文），
// 只额外补 error：正文文件不存在时 Content 为空串而不是报错。
type GetReportOutput struct {
	runtimecontext.ProjectReportDetail
	Error string `json:"error,omitempty"`
}

// GetReport 返回「按 ID 查看项目报告」工具。
//
// 与 create_report / update_report 互补：后两者负责写报告（并在授权前展示 diff），
// 本工具负责读——按 report_id 取回报告标题、正文文件路径（output.md）与正文内容，
// 供模型回答报告内容相关问题，或在 update_report 前确认当前正文。
// 需要收集分析证据时，用 list_session_resources 列出会话资源，再用 get_analysis_node /
// get_analysis_workflow 读取各节点 / 运行的结果。
//
// 该工具是只读的，因此声明 read 授权：上层在执行前会先走权限门禁。
func GetReport(browser runtimecontext.ProjectReportBrowser) tool.Tool {
	return tool.NewFunc(
		GetReportToolName,
		"按 report_id 查看单个项目报告。返回 title（报告标题）、file_path（报告正文文件 output.md 的绝对路径）与 content（正文 markdown 全文，已直接读回）。用于回答用户对 @report:<id> 引用的提问，或在 update_report 前确认当前正文。需要收集分析证据（会话内分析节点 / 分析运行的结果）时先用 list_session_resources 列出资源，再用 get_analysis_node / get_analysis_workflow 读取；需要修改正文时用 update_report。",
		tool.Schema("按 report_id 查看项目报告的入参", map[string]any{
			"report_id": tool.StringProperty("项目报告主键 ID（必填，整数，字符串以保精度）。来自用户输入里的 @report:<id> 引用时直接使用冒号后的 ID。"),
		}, "report_id"),
		func(ctx context.Context, in GetReportInput) (GetReportOutput, error) {
			reportID, err := parseReportID(in.ReportID)
			if err != nil {
				return GetReportOutput{}, fmt.Errorf("%s: %w", GetReportToolName, err)
			}

			projectID, err := requireProjectID(ctx, GetReportToolName)
			if err != nil {
				return GetReportOutput{}, err
			}

			userID := tool.UserIDFrom(ctx)
			if userID <= 0 {
				return GetReportOutput{}, fmt.Errorf("%s: user_id is required in this runtime", GetReportToolName)
			}

			if browser == nil {
				return GetReportOutput{}, fmt.Errorf("%s: project report browser is not available in this runtime", GetReportToolName)
			}

			report, err := browser.GetReportByID(ctx, userID, projectID, reportID)
			if err != nil {
				return GetReportOutput{
					ProjectReportDetail: runtimecontext.ProjectReportDetail{ReportID: reportID},
					Error:               err.Error(),
				}, nil
			}

			return GetReportOutput{ProjectReportDetail: report}, nil
		},
	).WithPermission("read", "", "按 ID 查看项目报告标题与正文")
}
