package dataflow

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	dagruntime "github.com/biox-dev/gobrave/internal/dag"
	"github.com/biox-dev/gobrave/internal/types"
)

func resumeNodeStatusForRestart(status string, hasContainer bool) (string, bool) {
	status = strings.TrimSpace(strings.ToLower(status))
	switch status {
	case dagruntime.StatusSubmitted:
		return dagruntime.StatusReady, true
	case dagruntime.StatusRunning:
		if hasContainer {
			return "", false
		}
		return dagruntime.StatusReady, true
	default:
		return "", false
	}
}

func dynamicToJSONMap(v any) types.JSONMap {
	if v == nil {
		return types.JSONMap{}
	}
	if m, ok := v.(types.JSONMap); ok {
		return m
	}
	if m, ok := v.(map[string]any); ok {
		return types.JSONMap(dynamicCloneAnyMap(m))
	}
	if m, ok := v.(map[string]interface{}); ok {
		out := make(map[string]any, len(m))
		for k, val := range m {
			out[k] = val
		}
		return types.JSONMap(out)
	}
	return types.JSONMap{}
}

func dynamicCloneAnyMap(src map[string]any) map[string]any {
	if len(src) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

// dynamicStringSliceToAny converts []string to []any for JSONSlice compatibility.
func dynamicStringSliceToAny(items []string) []any {
	out := make([]any, 0, len(items))
	for _, item := range items {
		out = append(out, item)
	}
	return out
}

// dynamicToMapSlice converts mixed JSON-decoded list values into []map[string]any.
func dynamicToMapSlice(value any) []map[string]any {
	if value == nil {
		return []map[string]any{}
	}
	if rows, ok := value.([]map[string]any); ok {
		return rows
	}
	raw, ok := value.([]any)
	if !ok {
		return []map[string]any{}
	}
	rows := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		if m, ok := item.(map[string]any); ok {
			rows = append(rows, m)
		}
	}
	return rows
}

// dynamicToString performs tolerant scalar-to-string conversion for map values.
func dynamicToString(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

// dynamicIntFromAny is a tolerant converter for numeric fields coming from decoded JSON.
func dynamicIntFromAny(v any, fallback int) int {
	switch t := v.(type) {
	case int:
		return t
	case int64:
		return int(t)
	case float64:
		return int(t)
	case string:
		n, err := strconvAtoi(strings.TrimSpace(t))
		if err == nil {
			return n
		}
	}
	return fallback
}

// strconvAtoi is a small local parser to avoid extra import coupling in this file.
func strconvAtoi(v string) (int, error) {
	neg := false
	if strings.HasPrefix(v, "-") {
		neg = true
		v = strings.TrimPrefix(v, "-")
	}
	if v == "" {
		return 0, fmt.Errorf("empty")
	}
	n := 0
	for _, ch := range v {
		if ch < '0' || ch > '9' {
			return 0, fmt.Errorf("invalid")
		}
		n = n*10 + int(ch-'0')
	}
	if neg {
		n = -n
	}
	return n, nil
}

func buildNodeRerunReason(commandMatched bool, paramsMatched bool, requireParamsMD5 bool) string {
	if !commandMatched && requireParamsMD5 && !paramsMatched {
		return "command and params changed"
	}
	if !commandMatched {
		return "command changed"
	}
	if requireParamsMD5 && !paramsMatched {
		return "params changed"
	}
	return "node cache invalidated"
}

func appendUniqueString(items []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return items
	}
	for _, item := range items {
		if strings.TrimSpace(item) == value {
			return items
		}
	}
	return append(items, value)
}

func dynamicToMap(value any) map[string]any {
	m, ok := value.(map[string]any)
	if !ok {
		return map[string]any{}
	}
	cloned := make(map[string]any, len(m))
	for key, item := range m {
		cloned[key] = item
	}
	return cloned
}

func resolveNodeField(node map[string]any, key string) any {
	if node == nil {
		return nil
	}
	if value, ok := node[key]; ok {
		return value
	}
	if data := dynamicToMap(node["data"]); len(data) > 0 {
		if value, ok := data[key]; ok {
			return value
		}
	}
	return nil
}

func toAnySlice(value any) ([]any, bool) {
	if value == nil {
		return nil, false
	}
	if items, ok := value.([]any); ok {
		copied := make([]any, len(items))
		copy(copied, items)
		return copied, true
	}
	rv := reflect.ValueOf(value)
	if rv.Kind() != reflect.Array && rv.Kind() != reflect.Slice {
		return nil, false
	}
	out := make([]any, rv.Len())
	for i := 0; i < rv.Len(); i++ {
		out[i] = rv.Index(i).Interface()
	}
	return out, true
}

func cloneInputs(inputs map[string]any) map[string]any {
	cloned := make(map[string]any, len(inputs))
	for key, value := range inputs {
		cloned[key] = value
	}
	return cloned
}

func extractNodeInputKeys(node map[string]any) []string {
	inputs := dynamicToMap(node["inputs"])
	keys := make([]string, 0, len(inputs))
	for key := range inputs {
		keys = appendUniqueString(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func dataflowChannelID(fromNodeID string, fromPort string, toNodeID string, toPort string) string {
	return fmt.Sprintf("%s:%s->%s:%s", strings.TrimSpace(fromNodeID), strings.TrimSpace(fromPort), strings.TrimSpace(toNodeID), strings.TrimSpace(toPort))
}

func dataflowSourceChannelID(nodeID string, inputKey string) string {
	return fmt.Sprintf("source:%s:%s", strings.TrimSpace(nodeID), strings.TrimSpace(inputKey))
}
