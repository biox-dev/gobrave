package dag

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/biox-dev/gobrave/internal/logger"
	"github.com/biox-dev/gobrave/internal/types"
)

// This file holds the pull-side counterpart of the removed push propagation.
//
// The dynamic / dataflow schedulers materialize a node only after its upstream is
// already satisfied, so an upstream-completion callback can never reach the node:
// it did not exist yet. Inputs are therefore bound when the node is materialized
// (or, for nodes persisted ahead of time by the static scheduler, right before
// they are prepared for execution).
//
// Binding has two halves:
//   - params / resolved_inputs receive the upstream's resolved output value (done
//     by nodebuild.MergeUpstreamInputs when a graph snapshot is available);
//   - inputs whose declared type needs the *original* request form (currently only
//     SelectAssay, which expects {"files": "<go_file id>"}) additionally get a
//     request_param rewrite and a structured resolved_inputs produced by the
//     script form parser. That rewrite is what this file provides.

// UpstreamInputBinding is one resolved incoming edge feeding a downstream input.
type UpstreamInputBinding struct {
	// TargetHandle is the downstream input handle (the edge target_handle).
	TargetHandle string
	// SourceHandle is the upstream output handle (the edge source_handle).
	SourceHandle string
	// FileID is the go_file primary key recorded for the upstream output handle,
	// empty when the upstream output is not a mirrorable file.
	FileID string
}

// BuildUpstreamInputBindings turns the incoming edges of one node into handle
// bindings, reading each source node's recorded output file id. Edges without a
// resolvable source node are skipped; edges whose source produced no file id are
// kept but carry an empty FileID so callers can decide how to treat them.
func BuildUpstreamInputBindings(incoming []*types.AnalysisEdge, upstream map[string]*types.AnalysisNode) []UpstreamInputBinding {
	if len(incoming) == 0 {
		return nil
	}
	bindings := make([]UpstreamInputBinding, 0, len(incoming))
	for _, edge := range incoming {
		if edge == nil {
			continue
		}
		targetHandle := strings.TrimSpace(edge.TargetHandle)
		sourceHandle := strings.TrimSpace(edge.SourceHandle)
		if targetHandle == "" || sourceHandle == "" {
			continue
		}
		source := upstream[strings.TrimSpace(edge.SourceNode)]
		if source == nil {
			continue
		}
		bindings = append(bindings, UpstreamInputBinding{
			TargetHandle: targetHandle,
			SourceHandle: sourceHandle,
			FileID:       OutputFileIDFor(source, sourceHandle),
		})
	}
	return bindings
}

// OutputFileIDFor returns the recorded go_file id of one output handle of a node,
// or an empty string when none was recorded.
func OutputFileIDFor(node *types.AnalysisNode, handle string) string {
	if node == nil || len(node.OutputFileIDs) == 0 {
		return ""
	}
	value, ok := node.OutputFileIDs[strings.TrimSpace(handle)]
	if !ok || value == nil {
		return ""
	}
	s, _ := value.(string)
	return strings.TrimSpace(s)
}

// ApplySelectAssayBindings rewrites target.request_param into the original input
// form ({"files": "<go_file id>"}) for every SelectAssay input that has a recorded
// upstream file id, then replaces the matching resolved_inputs with the structured
// recipe produced by the parser. It returns true when the target was modified.
//
// target is mutated in place (RequestParam and ResolvedInputs). parser may be nil,
// in which case only request_param is rewritten and resolved_inputs keeps the
// merged raw value.
func ApplySelectAssayBindings(ctx context.Context, parser NodeRequestParser, target *types.AnalysisNode, bindings []UpstreamInputBinding) bool {
	if target == nil || len(bindings) == 0 {
		return false
	}
	patterns := map[string]any(target.InputsPatterns)

	requestParams := map[string]any{}
	if raw := strings.TrimSpace(target.RequestParam); raw != "" {
		if err := json.Unmarshal([]byte(raw), &requestParams); err != nil {
			requestParams = map[string]any{}
		}
	}

	changed := false
	for _, binding := range bindings {
		inputCfg := asMap(patterns[binding.TargetHandle])
		if strings.ToLower(strings.TrimSpace(asString(inputCfg["type"]))) != "selectassay" {
			continue
		}
		if binding.FileID == "" {
			continue
		}
		requestParams[binding.TargetHandle] = map[string]any{"file": binding.FileID}
		changed = true
	}
	if !changed {
		return false
	}

	encoded, err := json.Marshal(requestParams)
	if err != nil {
		return false
	}
	target.RequestParam = string(encoded)

	resolvedInputs := map[string]any(target.ResolvedInputs)
	if resolvedInputs == nil {
		resolvedInputs = map[string]any{}
	}
	overwriteResolvedInputs(resolvedInputs, target, parseNodeRequestParam(ctx, parser, target, requestParams))
	target.ResolvedInputs = types.JSONMap(resolvedInputs)
	return true
}

// ApplyRequestParamParse parses target.request_param through the injected parser and
// overwrites the matching declared resolved_inputs. It returns true when the target was
// modified.
//
// It is the request_param-source counterpart of ApplySelectAssayBindings: where that one
// needs a resolved upstream edge (and its recorded file id) to know what to rewrite, this
// one derives resolved_inputs straight from request_param. It exists so a node whose
// inputs were written as raw request values - for example a scatter(each) item's
// per-handle value, keyed by the input name - can be turned into the structured recipe
// the script form expects, without requiring any upstream graph snapshot. parser may be
// nil, in which case nothing changes.
func ApplyRequestParamParse(ctx context.Context, parser NodeRequestParser, target *types.AnalysisNode) bool {
	if target == nil || parser == nil {
		return false
	}
	requestParams := map[string]any{}
	if raw := strings.TrimSpace(target.RequestParam); raw != "" {
		if err := json.Unmarshal([]byte(raw), &requestParams); err != nil {
			return false
		}
	}
	if len(requestParams) == 0 {
		return false
	}

	parsed := parseNodeRequestParam(ctx, parser, target, requestParams)
	if len(parsed) == 0 {
		return false
	}

	// Only overwrite the resolved_inputs of handles this node actually declares; a parsed
	// payload also carries non-input fields (analysis_name / colors / groups) that must not
	// leak into the node's inputs.
	declared := map[string]any(target.InputsPatterns)
	changed := false
	for key := range parsed {
		if _, ok := declared[key]; ok {
			changed = true
			break
		}
	}
	if !changed {
		return false
	}

	resolvedInputs := map[string]any(target.ResolvedInputs)
	if resolvedInputs == nil {
		resolvedInputs = map[string]any{}
	}
	overwriteResolvedInputs(resolvedInputs, target, parsed)
	target.ResolvedInputs = types.JSONMap(resolvedInputs)
	return true
}

// parseNodeRequestParam 调用注入的解析器把 request_param 还原成结构化配方。解析器未
// 注入或解析失败时返回 nil，调用方保留原始 resolved_inputs。
func parseNodeRequestParam(ctx context.Context, parser NodeRequestParser, target *types.AnalysisNode, requestParam map[string]any) map[string]any {
	if parser == nil || target == nil {
		return nil
	}
	parsed, err := parser.ParseRequestParam(ctx, target.ScriptID, requestParam)
	if err != nil {
		logger.Warnf(ctx, "[upstream_binding] parse target node request_param failed, analysis_id=%d node_id=%s script_id=%d err=%v",
			target.AnalysisID, target.NodeID, target.ScriptID, err)
		return nil
	}
	return parsed
}

// overwriteResolvedInputs 用解析结果按 key 覆盖 resolved_inputs。只覆盖目标节点声明的
// 输入句柄，避免 buildParseAnalysisResult 顺带产生的 analysis_name / colors / groups 等
// 非输入字段污染节点输入。
func overwriteResolvedInputs(resolvedInputs map[string]any, target *types.AnalysisNode, parsed map[string]any) {
	if len(parsed) == 0 || target == nil {
		return
	}
	declared := map[string]any(target.InputsPatterns)
	for key, value := range parsed {
		if _, ok := declared[key]; !ok {
			continue
		}
		resolvedInputs[key] = value
	}
}
