package handler

import (
	"context"

	dagruntime "github.com/biox-dev/gobrave/internal/dag"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
)

// analysisRequestParser 实现 dagruntime.NodeRequestParser：把 DAG 节点运行期重写后的
// request_param（形如 `{"handle": {"files": "<id>"}}`）依据脚本表单解析成结构化配方。
//
// 它复用静态分析保存时使用的 buildParseAnalysisResult，因此运行期节点与静态分析节点
// 在 resolved_inputs 上得到的是同一套字段（assay 行、accept_formats 命中的文件路径等）。
type analysisRequestParser struct {
	dataService     interfaces.DataService
	workflowService interfaces.WorkflowService
}

// NewNodeRequestParser 构造 dag 运行期需要的 request_param 解析器（注入 NodeDispatcher /
// NodeCompletionCoordinator 的 RuntimeEngine）。接口类型由 dag 包定义（consumer-side），
// 实现放在 handler 包，从而避免 dag -> handler 的循环依赖。
func NewNodeRequestParser(dataService interfaces.DataService, workflowService interfaces.WorkflowService) dagruntime.NodeRequestParser {
	return &analysisRequestParser{dataService: dataService, workflowService: workflowService}
}

// ParseRequestParam 读取脚本表单（io_schema.inputs），再走一遍与保存分析时相同的解析
// 逻辑，把 request_param 还原成结构化配方。脚本没有表单定义时返回 nil（不做解析）。
func (p *analysisRequestParser) ParseRequestParam(
	ctx context.Context,
	scriptID int64,
	requestParam map[string]any,
) (map[string]any, error) {
	if p == nil || p.workflowService == nil || p.dataService == nil || len(requestParam) == 0 {
		return nil, nil
	}

	formJSONWrap, _, err := p.workflowService.GetScriptFormJSONByID(ctx, scriptID)
	if err != nil {
		return nil, err
	}
	if len(formJSONWrap) == 0 {
		return nil, nil
	}

	return buildParseAnalysisResult(ctx, p.dataService, requestParam, formJSONWrap)
}
