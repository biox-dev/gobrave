package orchestratorv3

import (
	"fmt"
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
