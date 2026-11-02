package builtin

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	"github.com/biox-dev/gobrave/internal/agent/tool"
)

// GetReportContextToolName 是「按 @report:<id> 获取报告撰写上下文」工具名。
const GetReportContextToolName = "get_report_context"

// GetReportContextInput 是 get_report_context 工具的入参。
//
// ReportID 对应用户消息里的 @report:<id> 引用（如 "@report:2109711590457610240"），
// 它是项目报告主键 ID（t_project_report.id），必填：模型必须显式给出要撰写的报告，
// 不再从系统提示词注入的 project_report_items 变量回退。
type GetReportContextInput struct {
	ReportID string `json:"report_id"` // 项目报告主键 ID（支持 "@report:<id>" 形式，字符串以保精度）
}

// reportContextTemplate 是工具返回给模型的固定模板（pongo2 语法）。
//
// 模板只负责把「报告事实 + 条目清单」组织成可读文本；具体撰写要求由 Profile 系统提示词
// 负责。analysis_node 条目只给出 output.md 路径，模型必须读取文件真实内容后再总结。
const reportContextTemplate = `- 报告标题：{{ report_title }}
- 工作目录：{{ workspace_directory }}
- 输出文件：{{ output_file }}

---

## 分析结果来源

所有分析证据

{% for item in project_report_items %}
{% if item.OwnerType == "custom" %}

### 自定义章节

**章节名称：** {{ item.Name }}


{% elif item.OwnerType == "analysis_node" %}

### 分析任务

**分析名称：** {{ item.Name }}

**结果文件：** {{ item.Content }}

必须读取该 Markdown 文件的实际内容，并仅根据其中真实结果进行总结。

{% endif %}
{% endfor %}`

// GetReportContext 返回一个「按报告 ID 获取报告撰写上下文」工具。
//
// 行为：
//   - 解析必填入参 report_id（容错剥离 @report: 前缀）；
//   - 通过构造注入的 ProjectReportContextResolver 按报告 ID 解析报告标题、工作目录、输出文件与
//     条目列表（名称 / 内容 / 来源类型），条目解析口径与原 RuntimeContextResolver 的
//     projectReport 分支一致；
//   - 用内置 pongo2 模板把上述事实渲染成一段 Markdown 文本，作为工具结果返回给模型。
//
// 该工具是只读的，因此声明 read 授权：上层在执行前会先走权限门禁。
func GetReportContext(browser runtimecontext.ProjectReportContextResolver) tool.Tool {
	return tool.NewFunc(
		GetReportContextToolName,
		"按 report_id 获取项目报告的撰写上下文。用于回答 / 撰写用户通过 @report:<id> 引用的报告：直接使用冒号后的 ID 作为 report_id。返回报告标题、工作目录、输出文件，以及报告条目清单：custom 条目直接给出章节名称，analysis_node 条目给出对应分析的结果文件（output.md）绝对路径。撰写 / 总结报告前必须调用本工具，并对每个结果文件读取实际内容后再总结，禁止臆造结果。",
		tool.Schema("按 report_id 获取报告上下文的入参", map[string]any{
			"report_id": tool.StringProperty("项目报告主键 ID（必填，整数，字符串以保精度）。来自用户输入里的 @report:<id> 引用，直接使用冒号后的 ID。"),
		}, "report_id"),
		func(ctx context.Context, in GetReportContextInput) (string, error) {
			reportID, err := parseReportID(in.ReportID)
			if err != nil {
				return "", fmt.Errorf("%s: %w", GetReportContextToolName, err)
			}

			projectID, err := requireProjectID(ctx, GetReportContextToolName)
			if err != nil {
				return "", err
			}

			userID := tool.UserIDFrom(ctx)
			if userID <= 0 {
				return "", fmt.Errorf("%s: user_id is required in this runtime", GetReportContextToolName)
			}

			if browser == nil {
				return "", fmt.Errorf("%s: project resource browser is not available in this runtime", GetReportContextToolName)
			}

			reportCtx, err := browser.GetReportContext(ctx, userID, projectID, reportID)
			if err != nil {
				return "", err
			}

			rendered, err := tool.RenderTemplate(reportContextTemplate, map[string]any{
				"report_title":         reportCtx.ReportTitle,
				"workspace_directory":  reportCtx.WorkingDir,
				"output_file":          reportCtx.OutputFile,
				"project_report_items": reportCtx.Items,
			})
			if err != nil {
				return "", fmt.Errorf("%s: failed to render report context: %w", GetReportContextToolName, err)
			}
			return rendered, nil
		},
	).WithPermission("read", "", "按报告 ID 读取报告标题、工作目录与条目清单")
}

// parseReportID 解析必填的报告 ID：为空或非法时返回错误。
// 容错处理用户 / 模型可能带上的 "@report:" 前缀（如 "@report:2109711590457610240"）。
func parseReportID(raw string) (int64, error) {
	idStr := strings.TrimSpace(raw)
	if idx := strings.Index(idStr, "@report:"); idx >= 0 {
		idStr = strings.TrimSpace(idStr[idx+len("@report:"):])
	}
	if idStr == "" {
		return 0, fmt.Errorf("report_id is required")
	}

	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid report_id %q: %w", idStr, err)
	}
	if id <= 0 {
		return 0, fmt.Errorf("report_id must be a positive integer")
	}
	return id, nil
}
