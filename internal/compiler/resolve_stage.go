package compiler

import (
	"fmt"
	"sort"
	"strings"
)

type ResolveStage struct {
	sampleStrategy    ResolveStrategy
	singletonStrategy ResolveStrategy
}

type ResolveStrategy interface {
	Resolve(ctx *CompileContext, state *NodeRuntimeState) error
}

func (s *ResolveStage) Name() string { return "ResolveStage" }

func (s *ResolveStage) Run(ctx *CompileContext) error {
	if s.sampleStrategy == nil {
		s.sampleStrategy = &SampleResolveStrategy{}
	}
	if s.singletonStrategy == nil {
		s.singletonStrategy = &SingletonResolveStrategy{}
	}

	for _, nid := range ctx.Order {
		states := ctx.NodeStates[nid]
		for _, state := range states {
			var err error
			if state.Kind == "sample" {
				err = s.sampleStrategy.Resolve(ctx, state)
			} else {
				err = s.singletonStrategy.Resolve(ctx, state)
			}
			if err != nil {
				return err
			}
		}
	}
	return nil
}

type SampleResolveStrategy struct{}

func (s *SampleResolveStrategy) Resolve(ctx *CompileContext, state *NodeRuntimeState) error {
	incoming := ctx.Incoming[state.OriginalNodeID]
	for _, inputHandle := range sortedMapKeys(state.Inputs) {
		inputSchema := asMap(state.Inputs[inputHandle])
		var inEdgeMatch map[string]any
		for _, edge := range incoming {
			if edgeValue(edge, "targetHandle", "target_handle") == inputHandle {
				inEdgeMatch = edge
				break
			}
		}

		if inEdgeMatch != nil {
			src := fmt.Sprintf("%v", firstNonNil(inEdgeMatch["source"], ""))
			sourceIDBase := nodeIDBase(ctx.NodeMap[src])
			sourceHandle := edgeValue(inEdgeMatch, "sourceHandle", "source_handle")

			resolved := ctx.OutputCache[cacheKey{NodeID: buildNodeID(sourceIDBase, state.SampleLabel), Handle: sourceHandle, Scope: state.SampleLabel}]
			if resolved == nil {
				resolved = ctx.OutputCache[cacheKey{NodeID: sourceIDBase, Handle: sourceHandle, Scope: ""}]
			}
			if resolved == nil {
				resolved = ctx.OutputCache[cacheKey{NodeID: sourceIDBase, Handle: sourceHandle, Scope: "aggregate"}]
			}

			resolved = projectInputValueBySchema(resolved, inputSchema)
			state.ResolvedInputs[inputHandle] = resolved
			state.InputErrors = append(state.InputErrors, collectRequiredInputErrors(inputHandle, inputSchema, resolved)...)
			continue
		}

		byName := sampleValue(state.Sample, inputHandle)
		direct := byName
		if direct == nil {
			scatter := scatterField(state.Node)
			if scatterMatchesInputHandle(scatter, inputHandle) {
				direct = cloneMap(state.Sample)
			}
		}
		if direct != nil {
			// 散列(each)节点里，按句柄名命中的 item 值属于「原始请求值」，应写进节点的
			// request_param，而不是直接写进 resolved_inputs：request_param 保留原始值，
			// 落库时（nodebuild.Materialize）再由解析器据其生成结构化 resolved_inputs，
			// 与 select-assay 输入同一口径。
			//
			// 只有「按名称」(sampleValue) 命中的值走这条路；按散列字段名匹配整行 item 的
			// 兼容分支（scatterMatchesInputHandle）仍写回 resolved_inputs，因为整行 item
			// 不是可解析的原始表单值（例如 {"files": "<id>"}）。
			if byName != nil && isScatterEachNode(state.Node) {
				if state.RequestParam == nil {
					state.RequestParam = map[string]any{}
				}
				state.RequestParam[inputHandle] = direct
				continue
			}
			direct = projectInputValueBySchema(direct, inputSchema)
			state.ResolvedInputs[inputHandle] = direct
			state.InputErrors = append(state.InputErrors, collectRequiredInputErrors(inputHandle, inputSchema, direct)...)
			continue
		}

		if fmt.Sprintf("%v", firstNonNil(inputSchema["type"], "")) == "CollectedSampleSelect" {
			state.ResolvedInputs[inputHandle] = state.Sample
			continue
		}

		state.InputErrors = append(state.InputErrors, collectRequiredInputErrors(inputHandle, inputSchema, nil)...)
	}

	extra := sampleExtraMeta(state.Sample, sortedMapKeys(state.Inputs))
	if len(extra) > 0 {
		if _, exists := state.ResolvedInputs["meta"]; !exists {
			state.ResolvedInputs["meta"] = extra
		}
	}

	hasInputErr := len(state.InputErrors) > 0
	for _, outputHandle := range sortedMapKeys(state.Outputs) {
		outputCfg := asMap(state.Outputs[outputHandle])
		var outputValue any
		if !hasInputErr {
			outputValue = state.ResolvedInputs[outputHandle]
		}
		if !hasInputErr {
			if pattern, ok := outputCfg["pattern"].(string); ok && pattern != "" {
				outputValue = renderOutputPattern(pattern, state.Sample, state.SampleLabel)
			}
		}
		state.ResolvedOutput[outputHandle] = outputValue
		ctx.OutputCache[cacheKey{NodeID: state.NodeID, Handle: outputHandle, Scope: state.SampleLabel}] = outputValue
	}

	return nil
}

type SingletonResolveStrategy struct{}

func (s *SingletonResolveStrategy) Resolve(ctx *CompileContext, state *NodeRuntimeState) error {
	incoming := ctx.Incoming[state.OriginalNodeID]
	isAggregate := state.Kind == "aggregate"
	gather := gatherField(state.Node)

	for _, inputHandle := range sortedMapKeys(state.Inputs) {
		inputSchema := asMap(state.Inputs[inputHandle])
		values := make([]any, 0)
		for _, inEdge := range incoming {
			if edgeValue(inEdge, "targetHandle", "target_handle") != inputHandle {
				continue
			}
			src := fmt.Sprintf("%v", firstNonNil(inEdge["source"], ""))
			sourceIDBase := nodeIDBase(ctx.NodeMap[src])
			sourceKind := ctx.NodeKind[src]
			sourceHandle := edgeValue(inEdge, "sourceHandle", "source_handle")

			switch sourceKind {
			case "sample":
				sourceLabels := ctx.NodeLabel[src]
				if len(sourceLabels) == 0 {
					sourceLabels = []string{""}
				}
				for _, sampleLabel := range sourceLabels {
					values = append(values, ctx.OutputCache[cacheKey{NodeID: buildNodeID(sourceIDBase, sampleLabel), Handle: sourceHandle, Scope: sampleLabel}])
				}
			case "singleton":
				values = append(values, ctx.OutputCache[cacheKey{NodeID: sourceIDBase, Handle: sourceHandle, Scope: ""}])
			default:
				values = append(values, ctx.OutputCache[cacheKey{NodeID: sourceIDBase, Handle: sourceHandle, Scope: "aggregate"}])
			}
		}

		filtered := make([]any, 0, len(values))
		for _, v := range values {
			if v != nil {
				filtered = append(filtered, v)
			}
		}

		isList := isAggregate && gather != "" && inputHandle == gather
		if isList {
			state.ResolvedInputs[inputHandle] = filtered
			state.InputErrors = append(state.InputErrors, collectRequiredInputErrors(inputHandle, inputSchema, filtered)...)
			continue
		}
		if len(filtered) > 0 {
			state.ResolvedInputs[inputHandle] = filtered[0]
			state.InputErrors = append(state.InputErrors, collectRequiredInputErrors(inputHandle, inputSchema, filtered[0])...)
			continue
		}

		direct := ctx.Params[inputHandle]
		if direct != nil {
			direct = projectInputValueBySchema(direct, inputSchema)
			state.ResolvedInputs[inputHandle] = direct
			state.InputErrors = append(state.InputErrors, collectRequiredInputErrors(inputHandle, inputSchema, direct)...)
			continue
		}

		state.InputErrors = append(state.InputErrors, collectRequiredInputErrors(inputHandle, inputSchema, nil)...)
	}

	hasInputErr := len(state.InputErrors) > 0
	for _, outputHandle := range sortedMapKeys(state.Outputs) {
		outputCfg := asMap(state.Outputs[outputHandle])
		var outputValue any
		if !hasInputErr {
			outputValue = state.ResolvedInputs[outputHandle]
		}
		if !hasInputErr {
			if pattern, ok := outputCfg["pattern"].(string); ok && pattern != "" {
				if isAggregate {
					outputValue = strings.ReplaceAll(pattern, "{sample}", "merged")
				} else {
					outputValue = renderOutputPattern(pattern, ctx.Params, state.SampleLabel)
				}
			}
		}
		state.ResolvedOutput[outputHandle] = outputValue
		scope := ""
		if isAggregate {
			scope = "aggregate"
		}
		ctx.OutputCache[cacheKey{NodeID: state.NodeID, Handle: outputHandle, Scope: scope}] = outputValue
	}

	return nil
}

func sortedMapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
