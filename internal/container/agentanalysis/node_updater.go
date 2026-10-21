package agentanalysis

import (
	"github.com/biox-dev/gobrave/internal/agent/tool"
	"github.com/biox-dev/gobrave/internal/dag/prepare"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
)

// analysisNodeUpdater 实现 tool.AnalysisNodeUpdater：把「更新分析节点」的业务能力
// 暴露给内置工具 update_agent_analysis / update_script_analysis。
//
// 按节点类型细分的更新（命令型 / 脚本型）见 agent_node_updater.go / script_node_updater.go。
// 节点参数由 build_analysis_node_params / run_analysis_node 或 update_script_analysis 负责，
// patterns 由 io_schema 决定，运行状态由编排器维护。
type analysisNodeUpdater struct {
	analysisSvc interfaces.AnalysisService
	// workflowSvc 用于读取脚本最新 io_schema（校验并解析 request_param）。
	workflowSvc interfaces.WorkflowService
	// dataSvc 用于按脚本表单解析 request_param（文件 / 数据库输入等）。
	dataSvc interfaces.DataService
	// containerRepo 用于校验命令节点更新时给出的容器模板是否存在。
	containerRepo interfaces.ContainerRepository
	// preparer 负责在 command / request_param 变更后重新生成 run.sh / params.json 等运行期文件。
	preparer prepare.NodeRuntimePreparer
}

// NewNodeUpdater 构建「更新分析节点」执行器并注入内置工具体系。
func NewNodeUpdater(
	analysisSvc interfaces.AnalysisService,
	workflowSvc interfaces.WorkflowService,
	dataSvc interfaces.DataService,
	containerRepo interfaces.ContainerRepository,
	preparer prepare.NodeRuntimePreparer,
) tool.AnalysisNodeUpdater {
	return &analysisNodeUpdater{
		analysisSvc:   analysisSvc,
		workflowSvc:   workflowSvc,
		dataSvc:       dataSvc,
		containerRepo: containerRepo,
		preparer:      preparer,
	}
}
