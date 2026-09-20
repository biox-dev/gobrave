package dag

import (
	"fmt"
	"sort"

	"github.com/biox-dev/gobrave/internal/types"
)

// validateOutputPatterns checks that every handle declared in
// analysis_nodes.output_patterns produced a value in outputs.json.
//
// The check is intentionally simple: the handle only has to be present, so a
// pattern such as {"boxplot": {"type": "file"}} just requires outputs.json to
// contain a "boxplot" key. The column stays a generic types.JSONMap because it
// is built from external io_schema JSON and travels through the map-based node
// payloads shared by every scheduler, so no typed struct is needed here.
func validateOutputPatterns(node *types.AnalysisNode, resolved map[string]any) []string {
	if node == nil || len(node.OutputPatterns) == 0 {
		return nil
	}

	handles := make([]string, 0, len(node.OutputPatterns))
	for handle := range node.OutputPatterns {
		handles = append(handles, handle)
	}
	sort.Strings(handles)

	validationErrors := make([]string, 0)
	for _, handle := range handles {
		if _, ok := resolved[handle]; ok {
			continue
		}
		validationErrors = append(validationErrors, fmt.Sprintf("missing output: %s", handle))
	}
	return validationErrors
}
