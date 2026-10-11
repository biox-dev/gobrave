package compiler

import "fmt"

type ExpandStage struct {
	sampleStrategy    NodeExpandStrategy
	singletonStrategy NodeExpandStrategy
}

type NodeExpandStrategy interface {
	Expand(ctx *CompileContext, nid string, node map[string]any) ([]*NodeRuntimeState, error)
}

func (s *ExpandStage) Name() string { return "ExpandStage" }

func (s *ExpandStage) Run(ctx *CompileContext) error {
	if s.sampleStrategy == nil {
		s.sampleStrategy = &SampleExpandStrategy{}
	}
	if s.singletonStrategy == nil {
		s.singletonStrategy = &SingletonExpandStrategy{}
	}

	ctx.NodeStates = map[string][]*NodeRuntimeState{}
	ctx.StateOrder = make([]*NodeRuntimeState, 0)

	for _, nid := range ctx.Order {
		node := ctx.NodeMap[nid]
		kind := ctx.NodeKind[nid]

		var (
			states []*NodeRuntimeState
			err    error
		)
		if kind == "sample" {
			states, err = s.sampleStrategy.Expand(ctx, nid, node)
		} else {
			states, err = s.singletonStrategy.Expand(ctx, nid, node)
		}
		if err != nil {
			return err
		}
		if ctx.Abort {
			return nil
		}

		ctx.NodeStates[nid] = states
		ctx.StateOrder = append(ctx.StateOrder, states...)
	}

	return nil
}

type SampleExpandStrategy struct{}

func (s *SampleExpandStrategy) Expand(ctx *CompileContext, nid string, node map[string]any) ([]*NodeRuntimeState, error) {
	name := nodeName(node)
	nodeIDBase := nodeIDBase(node)
	script := scriptID(node)
	inputs := asMap(node["inputs"])
	outputs := asMap(node["outputs"])
	maxRetry := intOrDefault(node["max_retry"], 3)
	nodeParamsDefaults := resolveNodeParamsDefaults(ctx.Params, node, script, name)

	nodeSamples := make([]map[string]any, 0)
	nodeSampleLabels := make([]string, 0)

	scatter := scatterField(node)
	if scatter != "" {
		rawSamples, ok := ctx.Params[scatter].([]any)
		if !ok {
			ctx.Abort = true
			ctx.AnalysisNodes = []map[string]any{}
			ctx.AnalysisEdges = []map[string]any{}
			return nil, nil
		}
		for i, item := range rawSamples {
			sample, ok := item.(map[string]any)
			if !ok {
				continue
			}
			nodeSamples = append(nodeSamples, cloneMap(sample))
			nodeSampleLabels = append(nodeSampleLabels, sampleLabel(sample, i))
		}
	} else {
		upstreamLabels := deriveUpstreamSampleLabels(ctx, nid)
		if len(upstreamLabels) > 0 {
			nodeSampleLabels = append(nodeSampleLabels, upstreamLabels...)
			for _, label := range nodeSampleLabels {
				samplePayload := cloneMap(ctx.Params)
				for _, inEdge := range ctx.Incoming[nid] {
					src := fmt.Sprintf("%v", firstNonNil(inEdge["source"], ""))
					if ctx.NodeKind[src] != "sample" {
						continue
					}
					srcSamples := ctx.NodeSamplesMap[src]
					if srcSamples == nil {
						continue
					}
					if payload, ok := srcSamples[label]; ok {
						for k, v := range payload {
							samplePayload[k] = v
						}
						break
					}
				}
				nodeSamples = append(nodeSamples, samplePayload)
			}
		} else {
			nodeSamples = append(nodeSamples, cloneMap(ctx.Params))
			analysisName := fmt.Sprintf("%v", firstNonNil(ctx.Params["analysis_name"], ""))
			nodeSampleLabels = append(nodeSampleLabels, analysisName)
		}
	}

	ctx.NodeLabel[nid] = append([]string(nil), nodeSampleLabels...)
	ctx.NodeSamplesMap[nid] = map[string]map[string]any{}
	for i, label := range nodeSampleLabels {
		if i < len(nodeSamples) {
			ctx.NodeSamplesMap[nid][label] = cloneMap(nodeSamples[i])
		}
	}

	states := make([]*NodeRuntimeState, 0, len(nodeSamples))
	for i, sample := range nodeSamples {
		label := ""
		if i < len(nodeSampleLabels) {
			label = nodeSampleLabels[i]
		}
		state := &NodeRuntimeState{
			OriginalNodeID: nid,
			Node:           node,
			Kind:           "sample",
			NodeIDBase:     nodeIDBase,
			NodeID:         buildNodeID(nodeIDBase, label),
			NodeName:       buildNodeName(name, label),
			ScriptID:       script,
			SampleLabel:    label,
			Sample:         cloneMap(sample),
			Inputs:         cloneMap(inputs),
			Outputs:        cloneMap(outputs),
			ResolvedInputs: cloneMap(nodeParamsDefaults),
			ResolvedOutput: map[string]any{},
			InputErrors:    make([]string, 0),
			MaxRetry:       maxRetry,
		}
		states = append(states, state)
	}
	return states, nil
}

type SingletonExpandStrategy struct{}

func (s *SingletonExpandStrategy) Expand(ctx *CompileContext, nid string, node map[string]any) ([]*NodeRuntimeState, error) {
	name := nodeName(node)
	nodeIDBase := nodeIDBase(node)
	script := scriptID(node)
	inputs := asMap(node["inputs"])
	outputs := asMap(node["outputs"])
	maxRetry := intOrDefault(node["max_retry"], 3)
	nodeParamsDefaults := resolveNodeParamsDefaults(ctx.Params, node, script, name)
	isAggregate := ctx.NodeKind[nid] == "aggregate"

	instanceLabel := ""
	if isAggregate {
		instanceLabel = "merged"
	} else {
		instanceLabel = fmt.Sprintf("%v", firstNonNil(ctx.Params["analysis_name"], ""))
	}

	state := &NodeRuntimeState{
		OriginalNodeID: nid,
		Node:           node,
		Kind:           ctx.NodeKind[nid],
		NodeIDBase:     nodeIDBase,
		NodeID:         buildNodeID(nodeIDBase, ""),
		NodeName:       buildNodeName(name, instanceLabel),
		ScriptID:       script,
		SampleLabel:    instanceLabel,
		Sample:         cloneMap(ctx.Params),
		Inputs:         cloneMap(inputs),
		Outputs:        cloneMap(outputs),
		ResolvedInputs: cloneMap(nodeParamsDefaults),
		ResolvedOutput: map[string]any{},
		InputErrors:    make([]string, 0),
		MaxRetry:       maxRetry,
	}
	return []*NodeRuntimeState{state}, nil
}

func resolveNodeParamsDefaults(params map[string]any, node map[string]any, script string, name string) map[string]any {
	nodeParams := asMap(params["node_params"])
	if v, ok := nodeParams[script].(map[string]any); ok {
		return cloneMap(v)
	}
	if v, ok := nodeParams[name].(map[string]any); ok {
		return cloneMap(v)
	}
	if v, ok := node["params"].(map[string]any); ok {
		return cloneMap(v)
	}
	return map[string]any{}
}
