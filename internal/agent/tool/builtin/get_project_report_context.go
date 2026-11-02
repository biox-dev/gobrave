package builtin

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/tool"
)

// GetProjectReportContextToolName 是「按报告 ID 获取报告撰写上下文」工具名。
const GetProjectReportContextToolName = "get_project_report_context"

// GetProjectReportContextInput 是 get_project_report_context 工具的入参。
//
// ReportID 是项目报告主键 ID（t_project_report.id），必填：模型必须显式给出要撰写的
// 报告，不再从系统提示词注入的 project_report_items 变量回退。
type GetProjectReportContextInput struct {
	ReportID string `json:"report_id"` // 项目报告主键 ID（必填，字符串以保精度）
}

// projectReportContextTemplate 是工具返回给模型的固定模板（pongo2 语法）。
//
// 模板只负责把「报告事实 + 条目清单」组织成可读文本；具体撰写要求由 Profile 系统提示词
// 负责。analysis_node 条目只给出 output.md 路径，模型必须读取文件真实内容后再总结。
const projectReportContextTemplate = `- 报告标题：{{ report_title }}
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

// GetProjectReportContext 返回一个「按报告 ID 获取报告撰写上下文」工具。
//
// 行为：
//   - 解析必填入参 report_id；
//   - 通过注入的 ProjectResourceBrowser 按报告 ID 解析报告标题、工作目录、输出文件与
//     条目列表（名称 / 内容 / 来源类型），条目解析口径与原 RuntimeContextResolver 的
//     projectReport 分支一致；
//   - 用内置 pongo2 模板把上述事实渲染成一段 Markdown 文本，作为工具结果返回给模型。
//
// 该工具是只读的，因此声明 read 授权：上层在执行前会先走权限门禁。
func GetProjectReportContext() tool.Tool {
	return tool.NewFunc(
		GetProjectReportContextToolName,
		"按 report_id 获取项目报告的撰写上下文。返回报告标题、工作目录、输出文件，以及报告条目清单：custom 条目直接给出章节名称，analysis_node 条目给出对应分析的结果文件（output.md）绝对路径。撰写 / 总结报告前必须调用本工具，并对每个结果文件读取实际内容后再总结，禁止臆造结果。",
		tool.Schema("按 report_id 获取项目报告上下文的入参", map[string]any{
			"report_id": tool.StringProperty("项目报告主键 ID（必填，整数，字符串以保精度）。"),
		}, "report_id"),
		func(ctx context.Context, in GetProjectReportContextInput) (string, error) {
			reportID, err := parseProjectReportID(in.ReportID)
			if err != nil {
				return "", fmt.Errorf("%s: %w", GetProjectReportContextToolName, err)
			}

			projectID, err := requireProjectID(ctx, GetProjectReportContextToolName)
			if err != nil {
				return "", err
			}

			userID := tool.UserIDFrom(ctx)
			if userID <= 0 {
				return "", fmt.Errorf("%s: user_id is required in this runtime", GetProjectReportContextToolName)
			}

			browser := tool.ProjectResourceBrowserFrom(ctx)
			if browser == nil {
				return "", fmt.Errorf("%s: project resource browser is not available in this runtime", GetProjectReportContextToolName)
			}

			reportCtx, err := browser.GetProjectReportContext(ctx, userID, projectID, reportID)
			if err != nil {
				return "", fmt.Errorf("%s: %w", GetProjectReportContextToolName, err)
			}

			rendered, err := tool.RenderTemplate(projectReportContextTemplate, map[string]any{
				"report_title":         reportCtx.ReportTitle,
				"workspace_directory":  reportCtx.WorkingDir,
				"output_file":          reportCtx.OutputFile,
				"project_report_items": reportCtx.Items,
			})
			if err != nil {
				return "", fmt.Errorf("%s: failed to render report context: %w", GetProjectReportContextToolName, err)
			}
			return rendered, nil
		},
	).WithPermission("read", "", "按报告 ID 读取报告标题、工作目录与条目清单")
}

// parseProjectReportID 解析必填的报告 ID：为空或非法时返回错误。
func parseProjectReportID(raw string) (int64, error) {
	idStr := strings.TrimSpace(raw)
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
