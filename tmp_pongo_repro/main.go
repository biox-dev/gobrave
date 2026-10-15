package main

import (
	"fmt"

	"github.com/biox-dev/gobrave/internal/agent"
	"github.com/biox-dev/gobrave/internal/handler"
	"github.com/biox-dev/gobrave/internal/types"
)

const tpl = `{% if item.OwnerType == "custom" %}CUSTOM{% elif item.OwnerType == "analysis_node" %}NODE{% else %}OTHER{% endif %}`

func main() {
	items := []handler.ProjectReportItemVar{
		{Name: "突变菌与细菌RNA差异基因交集", OwnerType: types.ProjectReportItemOwnerCustom},
		{Name: "小肠突变差异基因分析", Content: "/x/out", OwnerType: types.ProjectReportItemOwnerAnalysisNode},
	}

	for _, it := range items {
		out, err := agent.RenderSystemPromptTemplate(tpl, map[string]any{"item": it})
		fmt.Printf("%-30s -> %q err=%v\n", it.Name, out, err)
	}
}
