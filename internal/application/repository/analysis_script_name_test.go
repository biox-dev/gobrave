package repository

import (
	"context"
	"strings"
	"testing"

	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/utils"
	"github.com/ncruces/go-sqlite3/gormlite"
	"gorm.io/gorm"
)

// newAnalysisNodeTestDB 打开共享内存 sqlite 并迁移 pipeline_components / analysis_nodes，
// 用于校验脚本名称列映射与分页节点列表的 script name join。
func newAnalysisNodeTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	_ = utils.InitSnowflake(1)
	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := gorm.Open(gormlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&types.Script{}, &types.AnalysisNode{}); err != nil {
		t.Fatalf("auto migrate: %v", err)
	}
	return db
}

// TestScriptNameMapsToComponentNameColumn 锁定「Go/JSON 侧叫 name、数据库列仍是 component_name」这条约定。
func TestScriptNameMapsToComponentNameColumn(t *testing.T) {
	db := newAnalysisNodeTestDB(t)

	script := &types.Script{ProjectID: 7, ScriptID: "s-1", ComponentType: "script", Name: "align-fastq"}
	if err := db.Create(script).Error; err != nil {
		t.Fatalf("create script: %v", err)
	}

	var raw struct {
		ComponentName string `gorm:"column:component_name"`
	}
	if err := db.Table("pipeline_components").
		Select("component_name").
		Where("id = ?", script.ID).
		Scan(&raw).Error; err != nil {
		t.Fatalf("select component_name: %v", err)
	}
	if raw.ComponentName != "align-fastq" {
		t.Fatalf("component_name column not populated: %q", raw.ComponentName)
	}

	// 读回时也要能通过 name 字段拿到同一列的值。
	reloaded := &types.Script{}
	if err := db.Where("id = ?", script.ID).Take(reloaded).Error; err != nil {
		t.Fatalf("reload script: %v", err)
	}
	if reloaded.Name != "align-fastq" {
		t.Fatalf("script name not reloaded: %q", reloaded.Name)
	}
}

// TestPageAnalysisNodesByProjectIDJoinsScriptName 校验分页节点列表按 script_id join 出脚本名称，
// 且没有对应脚本时 script_name 为空（LEFT JOIN 语义）。
func TestPageAnalysisNodesByProjectIDJoinsScriptName(t *testing.T) {
	db := newAnalysisNodeTestDB(t)
	ctx := context.Background()

	script := &types.Script{ProjectID: 7, ScriptID: "s-1", ComponentType: "script", Name: "align-fastq"}
	if err := db.Create(script).Error; err != nil {
		t.Fatalf("create script: %v", err)
	}

	withScript := &types.AnalysisNode{
		ProjectID: 7, AnalysisID: 9, AnalysisNodeID: "n-1",
		NodeID: "align", NodeName: "Align", ScriptID: script.ID, Status: "succeeded",
	}
	orphan := &types.AnalysisNode{
		ProjectID: 7, AnalysisID: 9, AnalysisNodeID: "n-2",
		NodeID: "sort", NodeName: "Sort", ScriptID: 999999, Status: "succeeded",
	}
	for _, node := range []*types.AnalysisNode{withScript, orphan} {
		if err := db.Create(node).Error; err != nil {
			t.Fatalf("create node: %v", err)
		}
	}

	repo := NewAnalysisRepository(db, nil)
	items, total, err := repo.PageAnalysisNodesByProjectID(ctx, &types.Pagination{Page: 1, PageSize: 20}, 7, 0, 0)
	if err != nil {
		t.Fatalf("PageAnalysisNodesByProjectID: %v", err)
	}
	if total != 2 || len(items) != 2 {
		t.Fatalf("unexpected result size: total=%d len=%d", total, len(items))
	}

	names := map[string]string{}
	for _, item := range items {
		names[item.AnalysisNodeID] = item.ScriptName
	}
	if names["n-1"] != "align-fastq" {
		t.Fatalf("script name not joined: %+v", names)
	}
	if names["n-2"] != "" {
		t.Fatalf("orphan node should have empty script name: %+v", names)
	}

	// script_id 过滤仍按 analysis_nodes 的列生效，且计数正确。
	filtered, filteredTotal, err := repo.PageAnalysisNodesByProjectID(ctx, &types.Pagination{Page: 1, PageSize: 20}, 7, script.ID, 0)
	if err != nil {
		t.Fatalf("PageAnalysisNodesByProjectID with script filter: %v", err)
	}
	if filteredTotal != 1 || len(filtered) != 1 || filtered[0].AnalysisNodeID != "n-1" {
		t.Fatalf("script filter mismatch: total=%d items=%+v", filteredTotal, filtered)
	}
}
