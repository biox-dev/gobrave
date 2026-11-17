package utils

import "path/filepath"

// 本文件是「脚本 / 工作流目录布局」的唯一事实源：目录里有哪几个文件、分别叫什么，
// 只在这里定义一次。需要新增目录内文件时先在这里加字段，再由调用方按字段取路径，
// 避免在业务代码里手写 filepath.Join(dir, "some.json") 造成多处路径漂移。
//
// 路径原语（GetScriptFileDir / GetWorkflowFileDir）仍在本文件的依赖函数里，
// 单文件路径函数（ScriptReadmePath / WorkflowIOSchemaPath 等）只是本文件的命名便捷入口，
// 二者共用同一组文件名常量（IOSchemaFileName / ReadmeFileName / DagDefinitionFileName）。

// ScriptFiles 是脚本目录下各文件的绝对路径集合。
//
// Dir / MainFile 由脚本目录与脚本类型决定，其余文件与脚本类型无关：
//   - MainPath：脚本主文件（main.R / main.py / main.sh / ...），模型与节点运行期直接读写；
//   - IOSchema：io_schema.json，输入输出契约（唯一数据源，不落库）；
//   - Readme：README.md，脚本说明文档。
type ScriptFiles struct {
	Dir      string
	MainFile string
	MainPath string
	IOSchema string
	Readme   string
}

// ScriptLayout 计算脚本目录布局。scriptType 决定主文件名（见 mainFileByScriptType）。
func ScriptLayout(baseDir string, projectID int64, scriptType, scriptID string) ScriptFiles {
	dir := GetScriptFileDir(baseDir, projectID, scriptID)
	mainFile := mainFileByScriptType(scriptType)
	return ScriptFiles{
		Dir:      dir,
		MainFile: mainFile,
		MainPath: filepath.Join(dir, mainFile),
		IOSchema: filepath.Join(dir, IOSchemaFileName),
		Readme:   filepath.Join(dir, ReadmeFileName),
	}
}

// WorkflowFiles 是工作流目录下各文件的绝对路径集合。
//
//   - DagDefinition：dag_definition.json，DAG 定义（唯一数据源，不落库）；
//   - IOSchema：io_schema.json，工作流级输入输出契约（唯一数据源，不落库）；
//   - Readme：README.md，工作流说明文档。
type WorkflowFiles struct {
	Dir           string
	DagDefinition string
	IOSchema      string
	Readme        string
}

// WorkflowLayout 计算工作流目录布局。
func WorkflowLayout(baseDir string, projectID int64, workflowID string) WorkflowFiles {
	dir := GetWorkflowFileDir(baseDir, projectID, workflowID)
	return WorkflowFiles{
		Dir:           dir,
		DagDefinition: filepath.Join(dir, DagDefinitionFileName),
		IOSchema:      filepath.Join(dir, IOSchemaFileName),
		Readme:        filepath.Join(dir, ReadmeFileName),
	}
}
