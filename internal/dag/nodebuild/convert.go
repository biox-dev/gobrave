package nodebuild

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"github.com/biox-dev/gobrave/internal/types"
)

// This file contains the tolerant converters used to read the loosely typed
// payload produced by the DAG compiler. The compiler emits map[string]any values
// decoded from JSON, so every numeric/boolean field may arrive as a string,
// float64 or native Go value.

// trimmed converts any scalar payload into a whitespace-trimmed string.
func trimmed(v any) string {
	switch value := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(value)
	case fmt.Stringer:
		return strings.TrimSpace(value.String())
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", value))
	}
}

// toInt converts a scalar payload into an int, returning fallback on failure.
func toInt(v any, fallback int) int {
	switch value := v.(type) {
	case int:
		return value
	case int8:
		return int(value)
	case int16:
		return int(value)
	case int32:
		return int(value)
	case int64:
		return int(value)
	case uint:
		return int(value)
	case uint8:
		return int(value)
	case uint16:
		return int(value)
	case uint32:
		return int(value)
	case uint64:
		return int(value)
	case float32:
		return int(value)
	case float64:
		return int(value)
	case string:
		if parsed, err := strconv.Atoi(strings.TrimSpace(value)); err == nil {
			return parsed
		}
	case json.Number:
		if parsed, err := value.Int64(); err == nil {
			return int(parsed)
		}
	}
	return fallback
}

// toBool converts a scalar payload into a bool, returning false on failure.
func toBool(v any) bool {
	switch value := v.(type) {
	case bool:
		return value
	case string:
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "true", "1", "yes", "y", "on":
			return true
		default:
			return false
		}
	case int:
		return value != 0
	case int64:
		return value != 0
	case float64:
		return value != 0
	default:
		return false
	}
}

// asMap performs best-effort map extraction.
func asMap(v any) map[string]any {
	switch value := v.(type) {
	case map[string]any:
		return value
	case types.JSONMap:
		return map[string]any(value)
	default:
		return map[string]any{}
	}
}

// isList reports whether the payload already behaves like a list.
func isList(v any) bool {
	if v == nil {
		return false
	}
	kind := reflect.ValueOf(v).Kind()
	return kind == reflect.Slice || kind == reflect.Array
}

// appendToList appends value to a list-like payload, upgrading scalars into a
// two-element list so fan-in inputs keep every produced value.
func appendToList(current any, value any) []any {
	if current == nil {
		return []any{value}
	}
	if items, ok := current.([]any); ok {
		return append(items, value)
	}
	rv := reflect.ValueOf(current)
	if rv.IsValid() && (rv.Kind() == reflect.Slice || rv.Kind() == reflect.Array) {
		out := make([]any, 0, rv.Len()+1)
		for i := 0; i < rv.Len(); i++ {
			out = append(out, rv.Index(i).Interface())
		}
		return append(out, value)
	}
	return []any{current, value}
}

// cloneJSONMap performs a shallow copy of a JSONMap.
//
// An empty input still yields a non-nil map so that JSON serialization is a
// stable "{}" rather than "null". Instance identity depends on that stability.
func cloneJSONMap(src types.JSONMap) types.JSONMap {
	if len(src) == 0 {
		return types.JSONMap{}
	}
	out := make(types.JSONMap, len(src))
	for key, value := range src {
		out[key] = value
	}
	return out
}

// cloneJSONSlice copies a JSON slice so callers cannot mutate compiler output.
func cloneJSONSlice(src types.JSONSlice) types.JSONSlice {
	if len(src) == 0 {
		return types.JSONSlice{}
	}
	return append(types.JSONSlice(nil), src...)
}

// compactStrings trims, drops empty entries and removes duplicates while
// preserving the original order.
func compactStrings(items []string) []string {
	out := make([]string, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		normalised := strings.TrimSpace(item)
		if normalised == "" {
			continue
		}
		if _, ok := seen[normalised]; ok {
			continue
		}
		seen[normalised] = struct{}{}
		out = append(out, normalised)
	}
	return out
}

// stringSliceToAny widens a string slice for JSONSlice compatibility.
func stringSliceToAny(items []string) []any {
	out := make([]any, 0, len(items))
	for _, item := range items {
		out = append(out, item)
	}
	return out
}

// ToJSONMap normalises any map-like payload into types.JSONMap.
//
// It is exported so schedulers can reuse the exact conversion the builder
// applies, keeping their compiler-row parsing in sync with node construction.
func ToJSONMap(v any) types.JSONMap {
	switch value := v.(type) {
	case nil:
		return types.JSONMap{}
	case types.JSONMap:
		return cloneJSONMap(value)
	case map[string]any:
		return types.JSONMap(cloneAnyMap(value))
	case string:
		trimmedValue := strings.TrimSpace(value)
		if trimmedValue == "" {
			return types.JSONMap{}
		}
		decoded := map[string]any{}
		if err := json.Unmarshal([]byte(trimmedValue), &decoded); err == nil {
			return types.JSONMap(decoded)
		}
	}
	return types.JSONMap{}
}

// ToJSONSlice normalises any slice-like payload into types.JSONSlice.
func ToJSONSlice(v any) types.JSONSlice {
	switch value := v.(type) {
	case nil:
		return types.JSONSlice{}
	case types.JSONSlice:
		return append(types.JSONSlice(nil), value...)
	case []any:
		return types.JSONSlice(append([]any(nil), value...))
	case []string:
		return types.JSONSlice(stringSliceToAny(value))
	}
	return types.JSONSlice{}
}

// ToStringSlice normalises a mixed array payload into a de-duplicated string slice.
func ToStringSlice(v any) []string {
	switch value := v.(type) {
	case nil:
		return nil
	case []string:
		return compactStrings(value)
	case []any:
		out := make([]string, 0, len(value))
		for _, item := range value {
			out = append(out, trimmed(item))
		}
		return compactStrings(out)
	default:
		rv := reflect.ValueOf(v)
		if rv.IsValid() && (rv.Kind() == reflect.Slice || rv.Kind() == reflect.Array) {
			out := make([]string, 0, rv.Len())
			for i := 0; i < rv.Len(); i++ {
				out = append(out, trimmed(rv.Index(i).Interface()))
			}
			return compactStrings(out)
		}
	}
	return nil
}

// ToInt exposes the tolerant numeric conversion for scheduler row parsing.
func ToInt(v any, fallback int) int { return toInt(v, fallback) }

// ToString exposes the tolerant scalar-to-string conversion for scheduler row parsing.
func ToString(v any) string { return trimmed(v) }

// ToStringSliceFromAny widens a plain string slice for JSONSlice columns.
func ToStringSliceFromAny(items []string) []any { return stringSliceToAny(compactStrings(items)) }

// cloneAnyMap performs a shallow copy so callers never mutate compiler output.
func cloneAnyMap(src map[string]any) map[string]any {
	if len(src) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(src))
	for key, value := range src {
		out[key] = value
	}
	return out
}
