package compiler

import "fmt"

type RuntimeCompiler struct {
	stages []Stage
}

func NewRuntimeCompiler() *RuntimeCompiler {
	return &RuntimeCompiler{
		stages: []Stage{
			&TopologyStage{},
			&ClassifyStage{},
			&ExpandStage{},
			&ResolveStage{},
			&OutputStage{},
			&EdgeStage{},
			&DecorateStage{},
		},
	}
}

func (c *RuntimeCompiler) Compile(analysisID int64, params map[string]any, requestParam map[string]any, dagDefinition map[string]any) (map[string]any, error) {
	ctx := NewCompileContext(analysisID, params, requestParam, dagDefinition)
	for _, stage := range c.stages {
		if ctx.Abort {
			break
		}
		if err := stage.Run(ctx); err != nil {
			return nil, fmt.Errorf("%s failed: %w", stage.Name(), err)
		}
	}
	return map[string]any{
		"analysis_nodes": ctx.AnalysisNodes,
		"analysis_edges": ctx.AnalysisEdges,
	}, nil
}

// BuildRuntimeTasks 展开 DAG 定义成 runtime 节点/边。
//
// requestParam 是本次分析的原始 request_param（analysis.request_param 解析而来）。
// 编译器把它作为每个节点的 analysis_node.request_param 原样下发，语义上与
// params 派生 resolved_inputs 一致：两者都是由编译器为节点生成的字段。
func BuildRuntimeTasks(analysisID int64, params map[string]any, requestParam map[string]any, dagDefinition map[string]any) (map[string]any, error) {
	return NewRuntimeCompiler().Compile(analysisID, params, requestParam, dagDefinition)
}

// func build_runtime_tasks(analysisID string, params map[string]any, dagDefinition map[string]any) (map[string]any, error) {
// 	return BuildRuntimeTasks(analysisID, params, dagDefinition)
// }
