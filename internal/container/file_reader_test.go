package container

import (
	"strconv"
	"testing"
)

func TestNormalizePreviewFormat(t *testing.T) {
	cases := []struct {
		format string
		path   string
		want   string
	}{
		{"xlsx", "a/b.xlsx", "xlsx"},
		{"", "a/b.CSV", "csv"},
		{".tsv", "a/b.tsv", "tsv"},
		{"", "a/b.unknown", "unknown"},
	}
	for _, tc := range cases {
		if got := normalizePreviewFormat(tc.format, tc.path); got != tc.want {
			t.Fatalf("normalizePreviewFormat(%q,%q)=%q want %q", tc.format, tc.path, got, tc.want)
		}
	}
}

func TestBuildSheetPreviews(t *testing.T) {
	// cellData: 行 "0" 是表头（name/age），行 "1"、"2" 是数据。
	workbookData := map[string]any{
		"name":       "demo",
		"sheetOrder": []any{"sheet-01", "sheet-02"},
		"sheets": map[string]any{
			"sheet-01": map[string]any{
				"name": "Sheet1",
				"cellData": map[string]any{
					"0": map[string]any{
						"0": map[string]any{"v": "name"},
						"1": map[string]any{"v": "age"},
					},
					"1": map[string]any{
						"0": map[string]any{"v": "alice"},
						"1": map[string]any{"v": "30"},
					},
					"2": map[string]any{
						"0": map[string]any{"v": "bob"},
						"1": map[string]any{"v": "25"},
					},
				},
			},
			"sheet-02": map[string]any{
				"name":     "Sheet2",
				"cellData": map[string]any{},
			},
		},
	}

	tabs := buildSheetPreviews(workbookData)
	if len(tabs) != 2 {
		t.Fatalf("expected 2 sheets, got %d", len(tabs))
	}

	// 顺序按 sheetOrder。
	if tabs[0].Name != "Sheet1" || tabs[1].Name != "Sheet2" {
		t.Fatalf("unexpected sheet order/names: %+v", tabs)
	}

	if got := tabs[0].Columns; len(got) != 2 || got[0] != "name" || got[1] != "age" {
		t.Fatalf("unexpected columns: %+v", got)
	}
	if len(tabs[0].Rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(tabs[0].Rows))
	}
	if tabs[0].Rows[0]["name"] != "alice" || tabs[0].Rows[0]["age"] != "30" {
		t.Fatalf("unexpected first row: %+v", tabs[0].Rows[0])
	}
}

func TestBuildSheetPreviewsLimitsRows(t *testing.T) {
	cellData := map[string]any{
		"0": map[string]any{"0": map[string]any{"v": "col"}},
	}
	for i := 1; i <= 10; i++ {
		cellData[strconv.Itoa(i)] = map[string]any{"0": map[string]any{"v": "v"}}
	}
	tabs := buildSheetPreviews(map[string]any{
		"sheets": map[string]any{"s1": map[string]any{"name": "s1", "cellData": cellData}},
	})
	if len(tabs) != 1 {
		t.Fatalf("expected 1 sheet, got %d", len(tabs))
	}
	if len(tabs[0].Rows) != previewRowLimit {
		t.Fatalf("expected %d preview rows, got %d", previewRowLimit, len(tabs[0].Rows))
	}
}

func TestBuildSheetPreviewsFallbackColumnName(t *testing.T) {
	// 表头为空 → 生成占位列名 column_1。
	tabs := buildSheetPreviews(map[string]any{
		"sheets": map[string]any{"s1": map[string]any{"name": "s1", "cellData": map[string]any{
			"0": map[string]any{"0": map[string]any{"v": ""}},
		}}},
	})
	if len(tabs) != 1 || len(tabs[0].Columns) != 1 || tabs[0].Columns[0] != "column_1" {
		t.Fatalf("unexpected fallback columns: %+v", tabs)
	}
}
