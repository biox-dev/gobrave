package dag

import (
	"testing"

	"github.com/biox-dev/gobrave/internal/types"
)

func TestValidateOutputPatternsPresence(t *testing.T) {
	node := &types.AnalysisNode{
		OutputPatterns: types.JSONMap{
			"boxplot":    map[string]any{"type": "file"},
			"genes":      map[string]any{"type": "file"},
			"up_genes":   map[string]any{"type": "file"},
			"down_genes": map[string]any{"type": "file"},
		},
	}
	// Every declared handle has a value in outputs.json.
	resolved := map[string]any{
		"boxplot":    "boxplot.png",
		"genes":      "Gene1,Gene2,Gene3",
		"up_genes":   "GeneA,GeneB",
		"down_genes": "GeneX,GeneY",
	}
	if got := validateOutputPatterns(node, resolved); len(got) != 0 {
		t.Fatalf("expected no validation errors, got %v", got)
	}
}

func TestValidateOutputPatternsMissingHandle(t *testing.T) {
	node := &types.AnalysisNode{
		OutputPatterns: types.JSONMap{
			"boxplot": map[string]any{"type": "file"},
			"genes":   map[string]any{"type": "file"},
		},
	}
	resolved := map[string]any{"boxplot": "boxplot.png"}

	got := validateOutputPatterns(node, resolved)
	if len(got) != 1 || got[0] != "missing output: genes" {
		t.Fatalf("unexpected validation errors: %v", got)
	}
}

func TestValidateOutputPatternsNilNodeOrEmptyPatterns(t *testing.T) {
	if got := validateOutputPatterns(nil, map[string]any{"a": 1}); got != nil {
		t.Fatalf("expected nil for nil node, got %v", got)
	}
	empty := &types.AnalysisNode{}
	if got := validateOutputPatterns(empty, map[string]any{}); got != nil {
		t.Fatalf("expected nil when no patterns are declared, got %v", got)
	}
}
