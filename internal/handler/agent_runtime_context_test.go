package handler

import (
	"strings"
	"testing"

	"github.com/biox-dev/gobrave/internal/agent"
	"github.com/biox-dev/gobrave/internal/types"
)

// TestProjectReportItemVarOwnerTypeRenderable 是项目报告模板的分支回归测试。
//
// 背景：报告模板通过 {% if item.OwnerType == "custom" %} / {% elif item.OwnerType ==
// "analysis_node" %} 按条目来源分支输出正文（见 Profile 系统提示词模板）。pongo2 的
// `==` 最终是 Go 的接口相等比较，动态类型不同即不相等，因此 ProjectReportItemVar.
// OwnerType 必须是 string：若改回具名字符串类型（types.ProjectReportItemOwnerType），
// 所有分支会全部落空、循环体整段不输出（自定义章节与各分析任务段落一起消失）。
//
// 该断言直接覆盖「变量结构体 → pongo2 渲染」这一段，防止回归。
func TestProjectReportItemVarOwnerTypeRenderable(t *testing.T) {
	// 与真实报告模板同构：按来源分支，未命中时输出 "?" 便于定位失败。
	const tpl = `{% for item in project_report_items %}` +
		`{% if item.OwnerType == "custom" %}[custom:{{ item.Name }}]` +
		`{% elif item.OwnerType == "analysis_node" %}[node:{{ item.Name }}|{{ item.Content }}]` +
		`{% elif item.OwnerType == "ai_summary" %}[summary:{{ item.Name }}]` +
		`{% else %}[?]{% endif %}{% endfor %}`

	items := []ProjectReportItemVar{
		{Name: "突变菌与细菌RNA差异基因交集", OwnerType: string(types.ProjectReportItemOwnerCustom)},
		{Name: "小肠突变差异基因分析", Content: "/data/36/out", OwnerType: string(types.ProjectReportItemOwnerAnalysisNode)},
		{Name: "摘要一", Content: "/data/36/summary.md", OwnerType: string(types.ProjectReportItemOwnerAISummary)},
	}

	out, err := agent.RenderSystemPromptTemplate(tpl, map[string]any{"project_report_items": items})
	if err != nil {
		t.Fatalf("render project report template: %v", err)
	}

	want := "[custom:突变菌与细菌RNA差异基因交集]" +
		"[node:小肠突变差异基因分析|/data/36/out]" +
		"[summary:摘要一]"
	if out != want {
		t.Fatalf("rendered = %q, want %q", out, want)
	}
	if strings.Contains(out, "[?]") {
		t.Fatalf("OwnerType 分支未命中（是否又改回具名字符串类型？）: %q", out)
	}
}
