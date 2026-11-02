package builtin

import (
	"strings"
	"testing"

	"github.com/biox-dev/gobrave/internal/agent/tool"
)

// TestProjectReportContextTemplate 覆盖 get_project_report_context 的内置模板渲染。
//
// 重点验证：
//   - 报告事实（标题 / 工作目录 / 输出文件）被正确填充；
//   - custom 条目直接输出章节名称；
//   - analysis_node 条目输出结果文件路径，并带上「必须读取实际内容」的约束；
//   - OwnerType 是 string 时 pongo2 的 == 分支能命中（一旦改回具名字符串类型即失败）。
func TestProjectReportContextTemplate(t *testing.T) {
	items := []tool.ProjectReportItemBrief{
		{Name: "引言", Content: "用户编辑的正文", OwnerType: "custom"},
		{Name: "差异基因分析", Content: "/data/36/analysis_node/1/output/output.md", OwnerType: "analysis_node"},
	}

	rendered, err := tool.RenderTemplate(projectReportContextTemplate, map[string]any{
		"report_title":         "肠道菌群研究报告",
		"workspace_directory":  "/data/36/report/1",
		"output_file":          "/data/36/report/1/output.md",
		"project_report_items": items,
	})
	if err != nil {
		t.Fatalf("render project report context template: %v", err)
	}

	for _, want := range []string{
		"- 报告标题：肠道菌群研究报告",
		"- 工作目录：/data/36/report/1",
		"- 输出文件：/data/36/report/1/output.md",
		"### 自定义章节",
		"**章节名称：** 引言",
		"### 分析任务",
		"**分析名称：** 差异基因分析",
		"**结果文件：** /data/36/analysis_node/1/output/output.md",
		"必须读取该 Markdown 文件的实际内容",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered context missing %q:\n%s", want, rendered)
		}
	}
}

func TestParseProjectReportID(t *testing.T) {
	if id, err := parseProjectReportID(" 123 "); err != nil || id != 123 {
		t.Fatalf("parseProjectReportID(123) = (%d, %v), want (123, nil)", id, err)
	}
	for _, raw := range []string{"", "abc", "0", "-1"} {
		if _, err := parseProjectReportID(raw); err == nil {
			t.Fatalf("parseProjectReportID(%q) expected error", raw)
		}
	}
}
