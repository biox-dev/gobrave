package compiler

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

type Stage interface {
	Name() string
	Run(ctx *CompileContext) error
}

type cacheKey struct {
	NodeID string
	Handle string
	Scope  string
}

type NodeRuntimeState struct {
	OriginalNodeID string
	Node           map[string]any
	Kind           string
	NodeIDBase     string
	NodeID         string
	NodeName       string
	ScriptID       string
	SampleLabel    string
	Sample         map[string]any
	Inputs         map[string]any
	Outputs        map[string]any
	NodeParams     map[string]any
	ResolvedInputs map[string]any
	ResolvedOutput map[string]any
	InputErrors    []string
	MaxRetry       int
}

type CompileContext struct {
	AnalysisID int64
	Params     map[string]any
	Dag        map[string]any
	Abort      bool

	Nodes []map[string]any
	Edges []map[string]any

	NodeMap   map[string]map[string]any
	Incoming  map[string][]map[string]any
	Outgoing  map[string][]map[string]any
	Order     []string
	NodeKind  map[string]string
	NodeLabel map[string][]string

	NodeSamplesMap map[string]map[string]map[string]any
	OutputCache    map[cacheKey]any
	NodeStates     map[string][]*NodeRuntimeState
	StateOrder     []*NodeRuntimeState

	AnalysisNodes []map[string]any
	AnalysisEdges []map[string]any
}

func NewCompileContext(analysisID int64, params map[string]any, dagDefinition map[string]any) *CompileContext {
	if params == nil {
		params = map[string]any{}
	}
	if dagDefinition == nil {
		dagDefinition = map[string]any{}
	}
	return &CompileContext{
		AnalysisID:     analysisID,
		Params:         params,
		Dag:            dagDefinition,
		Abort:          false,
		Incoming:       map[string][]map[string]any{},
		Outgoing:       map[string][]map[string]any{},
		NodeMap:        map[string]map[string]any{},
		NodeKind:       map[string]string{},
		NodeLabel:      map[string][]string{},
		NodeSamplesMap: map[string]map[string]map[string]any{},
		OutputCache:    map[cacheKey]any{},
		NodeStates:     map[string][]*NodeRuntimeState{},
		AnalysisNodes:  make([]map[string]any, 0),
		AnalysisEdges:  make([]map[string]any, 0),
	}
}

func asMap(v any) map[string]any {
	m, ok := v.(map[string]any)
	if !ok || m == nil {
		return map[string]any{}
	}
	return m
}

func asMapSlice(v any) []map[string]any {
	raw, ok := v.([]any)
	if !ok {
		return nil
	}
	res := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		if m, ok := item.(map[string]any); ok {
			res = append(res, m)
		}
	}
	return res
}

func cloneMap(src map[string]any) map[string]any {
	if src == nil {
		return map[string]any{}
	}
	dst := make(map[string]any, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func nodeKey(node map[string]any) string {
	// `node_id` is the workflow-instance identity and must be unique even when
	// the same script is reused multiple times in a DAG.
	return strings.TrimSpace(fmt.Sprintf("%v", firstNonNil(node["node_id"], node["id"], node["name"], "")))
}

func nodeName(node map[string]any) string {
	return fmt.Sprintf("%v", firstNonNil(node["name"], node["id"], "node"))
}

func nodeIDBase(node map[string]any) string {
	// Prefer node_id to avoid collisions when multiple nodes share the same name/script.
	raw := strings.TrimSpace(fmt.Sprintf("%v", firstNonNil(node["node_id"], node["id"], node["name"], "")))
	if raw == "" {
		raw = "node"
	}
	re := regexp.MustCompile(`\s+`)
	return re.ReplaceAllString(raw, "_")
}

func buildNodeID(nodeIDBase string, sampleLabel string) string {
	if strings.TrimSpace(sampleLabel) == "" {
		return nodeIDBase
	}
	re := regexp.MustCompile(`\s+`)
	normalized := re.ReplaceAllString(strings.TrimSpace(sampleLabel), "_")
	return nodeIDBase + "_" + normalized
}

func scriptID(node map[string]any) string {
	return fmt.Sprintf("%v", firstNonNil(node["script_id"], node["name"], node["id"], ""))
}

func sampleLabel(sample map[string]any, index int) string {
	// nodeNameVal := strings.TrimSpace(fmt.Sprintf("%v", firstNonNil(sample["node_name"], "")))
	// fileNameVal := strings.TrimSpace(fmt.Sprintf("%v", firstNonNil(sample["file_name"], "")))
	nodeNameVal := sample["node_name"]
	fileNameVal := sample["file_name"]
	if nodeNameVal != nil && nodeNameVal != "" && fileNameVal != nil && fileNameVal != "" {
		return fmt.Sprintf("%s (%s) (%d)", nodeNameVal, fileNameVal, index+1)
	}
	if fileNameVal != "" && fileNameVal != nil {
		return fmt.Sprintf("%s (%d)", fileNameVal, index+1)
	}
	if nodeNameVal != "" && nodeNameVal != nil {
		return fmt.Sprintf("%s (%d)", nodeNameVal, index+1)
	}
	analysisResultID := fmt.Sprintf("%v", firstNonNil(sample["analysis_result_id"], ""))
	return strings.TrimSpace(fmt.Sprintf("%s (%d)", analysisResultID, index+1))
}

func sampleValue(sample map[string]any, handle string) any {
	if v, ok := sample[handle]; ok {
		return v
	}
	norm := normalizeKey
	mapped := make(map[string]any, len(sample))
	for k, v := range sample {
		mapped[norm(k)] = v
	}
	if v, ok := mapped[norm(handle)]; ok {
		return v
	}
	return nil
}

func sampleExtraMeta(sample map[string]any, inputHandles []string) map[string]any {
	norm := normalizeKey
	consumed := map[string]struct{}{}
	for _, h := range inputHandles {
		consumed[norm(h)] = struct{}{}
	}
	extra := map[string]any{}
	for k, v := range sample {
		if v == nil {
			continue
		}
		if _, ok := consumed[norm(k)]; ok {
			continue
		}
		extra[k] = v
	}
	return extra
}

func schemaType(schema map[string]any) string {
	return strings.ToLower(strings.TrimSpace(fmt.Sprintf("%v", firstNonNil(schema["type"], ""))))
}

func scatterField(node map[string]any) string {
	scatter := asMap(node["scatter"])
	if strings.ToLower(strings.TrimSpace(fmt.Sprintf("%v", firstNonNil(scatter["mode"], "")))) != "each" {
		return ""
	}
	return strings.TrimSpace(fmt.Sprintf("%v", firstNonNil(scatter["field"], "")))
}

func gatherField(node map[string]any) string {
	gather := asMap(node["gather"])
	if strings.ToLower(strings.TrimSpace(fmt.Sprintf("%v", firstNonNil(gather["mode"], "")))) != "list" {
		return ""
	}
	return strings.TrimSpace(fmt.Sprintf("%v", firstNonNil(gather["field"], "")))
}

func edgeValue(edge map[string]any, camel string, snake string) string {
	return strings.TrimSpace(fmt.Sprintf("%v", firstNonNil(edge[camel], edge[snake], "")))
}

func deriveUpstreamSampleLabels(ctx *CompileContext, nid string) []string {
	labelSets := make([][]string, 0)
	for _, inEdge := range ctx.Incoming[nid] {
		src := fmt.Sprintf("%v", firstNonNil(inEdge["source"], ""))
		if ctx.NodeKind[src] != "sample" {
			continue
		}
		srcLabels := append([]string(nil), ctx.NodeLabel[src]...)
		if len(srcLabels) > 0 {
			labelSets = append(labelSets, srcLabels)
		}
	}
	if len(labelSets) == 0 {
		return nil
	}
	orderedBase := append([]string(nil), labelSets[0]...)
	for _, labels := range labelSets[1:] {
		allow := map[string]struct{}{}
		for _, label := range labels {
			allow[label] = struct{}{}
		}
		filtered := make([]string, 0, len(orderedBase))
		for _, label := range orderedBase {
			if _, ok := allow[label]; ok {
				filtered = append(filtered, label)
			}
		}
		orderedBase = filtered
	}
	if len(orderedBase) > 0 {
		return orderedBase
	}
	merged := make([]string, 0)
	seen := map[string]struct{}{}
	for _, labels := range labelSets {
		for _, label := range labels {
			if _, ok := seen[label]; ok {
				continue
			}
			seen[label] = struct{}{}
			merged = append(merged, label)
		}
	}
	return merged
}

func requiredPropertyNames(schema map[string]any) []string {
	properties := asMap(schema["properties"])
	res := make([]string, 0)
	for name, cfg := range properties {
		if truthy(asMap(cfg)["required"]) {
			res = append(res, name)
		}
	}
	sort.Strings(res)
	return res
}

func collectRequiredInputErrors(inputHandle string, inputSchema map[string]any, value any) []string {
	errs := make([]string, 0)
	typeName := schemaType(inputSchema)
	if typeName == "object" {
		obj, ok := value.(map[string]any)
		if !ok {
			if truthy(inputSchema["required"]) {
				errs = append(errs, fmt.Sprintf("missing required input: %s", inputHandle))
			}
			return errs
		}
		for _, prop := range requiredPropertyNames(inputSchema) {
			if obj[prop] == nil {
				errs = append(errs, fmt.Sprintf("missing required input: %s.%s", inputHandle, prop))
			}
		}
		return errs
	}
	if typeName == "list" {
		itemSchema := asMap(inputSchema["items"])
		if schemaType(itemSchema) != "object" {
			if truthy(inputSchema["required"]) && value == nil {
				errs = append(errs, fmt.Sprintf("missing required input: %s", inputHandle))
			}
			return errs
		}
		obj, ok := value.(map[string]any)
		if !ok {
			if truthy(inputSchema["required"]) {
				errs = append(errs, fmt.Sprintf("missing required input: %s", inputHandle))
			}
			return errs
		}
		for _, prop := range requiredPropertyNames(itemSchema) {
			if obj[prop] == nil {
				errs = append(errs, fmt.Sprintf("missing required input: %s.%s", inputHandle, prop))
			}
		}
		return errs
	}
	if truthy(inputSchema["required"]) && value == nil {
		errs = append(errs, fmt.Sprintf("missing required input: %s", inputHandle))
	}
	return errs
}

func projectInputValueBySchema(value any, inputSchema map[string]any) any {
	typeName := schemaType(inputSchema)
	if typeName == "object" {
		properties := asMap(inputSchema["properties"])
		if obj, ok := value.(map[string]any); ok {
			res := map[string]any{}
			for key := range properties {
				if obj[key] != nil {
					res[key] = obj[key]
				}
			}
			return res
		}
		return value
	}
	if typeName == "list" {
		itemSchema := asMap(inputSchema["items"])
		if schemaType(itemSchema) != "object" {
			return value
		}
		properties := asMap(itemSchema["properties"])
		if obj, ok := value.(map[string]any); ok {
			res := map[string]any{}
			for key := range properties {
				if obj[key] != nil {
					res[key] = obj[key]
				}
			}
			return res
		}
	}
	return value
}

func renderOutputPattern(pattern string, sample map[string]any, sampleLabel string) string {
	sampleName := fmt.Sprintf("%v", firstNonNil(sample["file_name"], sample["sample_id"], sampleLabel))
	return strings.ReplaceAll(pattern, "{sample}", sampleName)
}

func buildNodeName(name string, sampleLabel string) string {
	label := sampleLabel
	if strings.TrimSpace(label) == "" {
		label = "merged"
	}
	return fmt.Sprintf("%s (%s)", label, name)
}

func truthy(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return strings.EqualFold(strings.TrimSpace(t), "true") || strings.TrimSpace(t) == "1"
	case int:
		return t != 0
	case int64:
		return t != 0
	case float64:
		return t != 0
	default:
		return false
	}
}

func intOrDefault(v any, fallback int) int {
	switch t := v.(type) {
	case int:
		return t
	case int64:
		return int(t)
	case float64:
		return int(t)
	default:
		return fallback
	}
}

func firstNonNil(values ...any) any {
	for _, v := range values {
		if v == nil {
			continue
		}
		s := fmt.Sprintf("%v", v)
		if strings.TrimSpace(s) == "<nil>" {
			continue
		}
		if str, ok := v.(string); ok && strings.TrimSpace(str) == "" {
			continue
		}
		return v
	}
	return nil
}

func normalizeKey(key string) string {
	re := regexp.MustCompile(`[^a-z0-9]`)
	return re.ReplaceAllString(strings.ToLower(key), "")
}

func scatterMatchesInputHandle(scatter string, inputHandle string) bool {
	scatterNorm := normalizeKey(scatter)
	handleNorm := normalizeKey(inputHandle)
	if scatterNorm == "" || handleNorm == "" {
		return false
	}
	if scatterNorm == handleNorm {
		return true
	}
	if strings.TrimSuffix(scatterNorm, "s") == strings.TrimSuffix(handleNorm, "s") {
		return true
	}
	return false
}
