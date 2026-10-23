package tool

import (
	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
)

// 本文件保留 tool 包原有的运行时上下文 API（类型别名 + 函数转发）。
//
// RuntimeContext 及工具执行期的服务能力接口已移到 internal/agent/runtimecontext，
// 以避免 types → tool 的循环依赖；这里仅做兼容转发，供既有工具与上层代码继续使用
// tool.RuntimeContext / tool.WithRuntimeContext 等名称。

type (
	RuntimeContext        = runtimecontext.RuntimeContext
	AnalysisNodeRunner    = runtimecontext.AnalysisNodeRunner
	AnalysisNodeRunResult = runtimecontext.AnalysisNodeRunResult

	AgentAnalysisRunner = runtimecontext.AgentAnalysisRunner
	AgentAnalysisInput  = runtimecontext.AgentAnalysisInput
	AgentAnalysisResult = runtimecontext.AgentAnalysisResult

	FileReader     = runtimecontext.FileReader
	FileReadResult = runtimecontext.FileReadResult
	FileSheetTab   = runtimecontext.FileSheetTab

	ScriptCreator            = runtimecontext.ScriptCreator
	ScriptCreateInput        = runtimecontext.ScriptCreateInput
	ScriptCreateResult       = runtimecontext.ScriptCreateResult
	AnalysisNodeCreator      = runtimecontext.AnalysisNodeCreator
	AnalysisNodeCreateInput  = runtimecontext.AnalysisNodeCreateInput
	AnalysisNodeCreateResult = runtimecontext.AnalysisNodeCreateResult
	ScriptUpdater            = runtimecontext.ScriptUpdater
	ScriptUpdateInput        = runtimecontext.ScriptUpdateInput
	ScriptUpdateResult       = runtimecontext.ScriptUpdateResult

	AnalysisNodeUpdater        = runtimecontext.AnalysisNodeUpdater
	AgentAnalysisUpdateInput   = runtimecontext.AgentAnalysisUpdateInput
	AgentAnalysisUpdateResult  = runtimecontext.AgentAnalysisUpdateResult
	ScriptAnalysisUpdateInput  = runtimecontext.ScriptAnalysisUpdateInput
	ScriptAnalysisUpdateResult = runtimecontext.ScriptAnalysisUpdateResult

	ProjectResourceBrowser  = runtimecontext.ProjectResourceBrowser
	ScriptBrief             = runtimecontext.ScriptBrief
	AnalysisNodeBrief       = runtimecontext.AnalysisNodeBrief
	ContainerTemplateBrief  = runtimecontext.ContainerTemplateBrief
	ContainerTemplateDetail = runtimecontext.ContainerTemplateDetail
	DatasetBrief            = runtimecontext.DatasetBrief

	SkillResolver = runtimecontext.SkillResolver
	SkillResource = runtimecontext.SkillResource
	SkillInfo     = runtimecontext.SkillInfo

	AgentEnvUpdater = runtimecontext.AgentEnvUpdater

	AnalysisNodeParamsBuilder = runtimecontext.AnalysisNodeParamsBuilder
	AnalysisNodeParamsInput   = runtimecontext.AnalysisNodeParamsInput
	AnalysisNodeParamsResult  = runtimecontext.AnalysisNodeParamsResult

	MemorySaver      = runtimecontext.MemorySaver
	MemorySaveInput  = runtimecontext.MemorySaveInput
	MemorySaveResult = runtimecontext.MemorySaveResult
)

var (
	WithRuntimeContext = runtimecontext.WithRuntimeContext
	RuntimeContextFrom = runtimecontext.RuntimeContextFrom
	SystemPromptVar    = runtimecontext.SystemPromptVar

	WithAnalysisNodeRunner = runtimecontext.WithAnalysisNodeRunner
	AnalysisNodeRunnerFrom = runtimecontext.AnalysisNodeRunnerFrom

	WithAgentAnalysisRunner = runtimecontext.WithAgentAnalysisRunner
	AgentAnalysisRunnerFrom = runtimecontext.AgentAnalysisRunnerFrom

	WithFileReader = runtimecontext.WithFileReader
	FileReaderFrom = runtimecontext.FileReaderFrom

	WithScriptUpdater = runtimecontext.WithScriptUpdater
	ScriptUpdaterFrom = runtimecontext.ScriptUpdaterFrom

	WithAnalysisNodeUpdater = runtimecontext.WithAnalysisNodeUpdater
	AnalysisNodeUpdaterFrom = runtimecontext.AnalysisNodeUpdaterFrom

	WithProjectResourceBrowser = runtimecontext.WithProjectResourceBrowser
	ProjectResourceBrowserFrom = runtimecontext.ProjectResourceBrowserFrom

	WithSkillResolver = runtimecontext.WithSkillResolver
	SkillResolverFrom = runtimecontext.SkillResolverFrom

	WithScriptCreator       = runtimecontext.WithScriptCreator
	ScriptCreatorFrom       = runtimecontext.ScriptCreatorFrom
	WithAnalysisNodeCreator = runtimecontext.WithAnalysisNodeCreator
	AnalysisNodeCreatorFrom = runtimecontext.AnalysisNodeCreatorFrom

	WithAgentEnvUpdater = runtimecontext.WithAgentEnvUpdater
	AgentEnvUpdaterFrom = runtimecontext.AgentEnvUpdaterFrom

	WithAnalysisNodeParamsBuilder = runtimecontext.WithAnalysisNodeParamsBuilder
	AnalysisNodeParamsBuilderFrom = runtimecontext.AnalysisNodeParamsBuilderFrom

	WithMemorySaver = runtimecontext.WithMemorySaver
	MemorySaverFrom = runtimecontext.MemorySaverFrom
)
