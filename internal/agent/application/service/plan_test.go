package service

import (
	"testing"

	agenttypes "github.com/biox-dev/gobrave/internal/agent/types"
	"github.com/biox-dev/gobrave/internal/utils"
)

// TestNormalizeReportPlanSections 覆盖大纲章节清洗：
// 生成缺失 ID、丢弃空标题、非法来源回退 custom、custom 清空 source_id、
// 悬空 / 自引用父节点提升为根。
func TestNormalizeReportPlanSections(t *testing.T) {
	_ = utils.InitSnowflake(1)
	sections := NormalizeReportPlanSections([]agenttypes.ReportPlanSection{
		{Title: "引言", Source: agenttypes.ReportPlanSourceCustom, SourceID: 123},
		{Title: "  ", Source: agenttypes.ReportPlanSourceCustom},
		{Title: "差异分析", Source: "bogus", SourceID: 42, ParentID: 999},
	})

	if len(sections) != 2 {
		t.Fatalf("section count = %d, want 2", len(sections))
	}
	for _, s := range sections {
		if s.ID <= 0 {
			t.Fatalf("section %q got empty id", s.Title)
		}
		if s.ParentID != 0 {
			t.Fatalf("section %q parent = %d, want 0", s.Title, s.ParentID)
		}
	}
	if sections[0].SourceID != 0 || sections[0].Source != agenttypes.ReportPlanSourceCustom {
		t.Fatalf("custom section = %+v, want source custom with source_id 0", sections[0])
	}
	if sections[1].Source != agenttypes.ReportPlanSourceCustom {
		t.Fatalf("invalid source = %q, want fallback custom", sections[1].Source)
	}
	if sections[1].SourceID != 0 {
		t.Fatalf("fallback custom section keeps source_id = %d, want 0", sections[1].SourceID)
	}
}
