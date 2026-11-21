package service

import (
	"testing"

	agenttypes "github.com/biox-dev/gobrave/internal/agent/types"
	"github.com/biox-dev/gobrave/internal/utils"
)

// TestNormalizePlanItems 覆盖计划节点清洗：
// 生成缺失 ID、丢弃空标题、非法来源回退 custom、custom 清空 source_id、
// 悬空 / 自引用父节点提升为根。
func TestNormalizePlanItems(t *testing.T) {
	_ = utils.InitSnowflake(1)
	items := NormalizePlanItems([]agenttypes.PlanItem{
		{Title: "概述", Source: agenttypes.PlanSourceCustom, SourceID: 123},
		{Title: "  ", Source: agenttypes.PlanSourceCustom},
		{Title: "差异分析", Source: "bogus", SourceID: 42, ParentID: 999},
	})

	if len(items) != 2 {
		t.Fatalf("item count = %d, want 2", len(items))
	}
	for _, item := range items {
		if item.ID <= 0 {
			t.Fatalf("item %q got empty id", item.Title)
		}
		if item.ParentID != 0 {
			t.Fatalf("item %q parent = %d, want 0", item.Title, item.ParentID)
		}
	}
	if items[0].SourceID != 0 || items[0].Source != agenttypes.PlanSourceCustom {
		t.Fatalf("custom item = %+v, want source custom with source_id 0", items[0])
	}
	if items[1].Source != agenttypes.PlanSourceCustom {
		t.Fatalf("invalid source = %q, want fallback custom", items[1].Source)
	}
	if items[1].SourceID != 0 {
		t.Fatalf("fallback custom item keeps source_id = %d, want 0", items[1].SourceID)
	}
}
