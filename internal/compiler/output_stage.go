package compiler

import "fmt"

type OutputStage struct {
	builder *RuntimeNodeBuilder
}

type RuntimeNodeBuilder struct{}

func (s *OutputStage) Name() string { return "OutputStage" }

func (s *OutputStage) Run(ctx *CompileContext) error {
	if s.builder == nil {
		s.builder = &RuntimeNodeBuilder{}
	}
	ctx.AnalysisNodes = make([]map[string]any, 0, len(ctx.StateOrder))
	for _, state := range ctx.StateOrder {
		ctx.AnalysisNodes = append(ctx.AnalysisNodes, s.builder.Build(ctx, state))
	}
	return nil
}

func (b *RuntimeNodeBuilder) Build(ctx *CompileContext, state *NodeRuntimeState) map[string]any {
	// request_param 与 inputs_patterns 的填充口径一致：先按节点输入 name 过滤 analysis
	// 请求，再叠加 state.RequestParam（散列 each 节点按句柄名命中的原始 item 值）。
	requestParam := FilterRequestParamByHandles(ctx.RequestParam, state.Inputs)
	for key, value := range state.RequestParam {
		requestParam[key] = value
	}

	node := map[string]any{
		"analysis_id":      ctx.AnalysisID,
		"node_id":          state.NodeID,
		"node_name":        state.NodeName,
		"script_id":        state.ScriptID,
		"inputs_patterns":  state.Inputs,
		"resolved_inputs":  state.ResolvedInputs,
		"output_patterns":  state.Outputs,
		"resolved_outputs": state.ResolvedOutput,
		// request_param 与 inputs_patterns 的填充口径一致：只保留 analysis 请求里
		// 键与节点输入 name（脚本 form json 的 name）相同的项，而不是整份请求。
		"request_param":           requestParam,
		"input_validation_errors": state.InputErrors,
		"executor":                fmt.Sprintf("%v", firstNonNil(ctx.Params["executor"], "")),
		"max_retry":               state.MaxRetry,
	}
	if state.Kind == "sample" {
		node["sample_id"] = fmt.Sprintf("%v", firstNonNil(state.Sample["sample_id"], ""))
	}
	return node
}
