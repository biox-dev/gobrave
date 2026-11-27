package utils

// 本文件是「脚本 / 工作流目录内文件」的命名入口说明。
//
// 目录里有哪几个文件、分别叫什么，只由 utils 里的单文件路径函数定义一次，
// 调用方（含 types.Script / types.Workflow 的逐文件方法）一律按单个文件取值，
// 不再用「一次性返回全部路径」的聚合结构，避免在业务代码里手写
// filepath.Join(dir, "some.json") 造成多处路径漂移：
//
// 脚本目录（GetScriptFileDir）：
//   - ScriptMainFilePath：主文件（main.R / main.py / main.sh / main.ipynb / main.qmd）
//   - ScriptIOSchemaPath：io_schema.json
//   - ScriptReadmePath：README.md
//
// 工作流目录（GetWorkflowFileDir）：
//   - WorkflowDagDefinitionPath：dag_definition.json
//   - WorkflowIOSchemaPath：io_schema.json
//   - WorkflowReadmePath：README.md

// ScriptMainFileName 返回脚本主文件名，由脚本类型决定（见 mainFileByScriptType）。
//
// 与 ScriptMainFilePath 共用同一份类型映射，需要「文件名」（如软链到节点工作目录时）
// 用本函数，需要「绝对路径」用 ScriptMainFilePath。
func ScriptMainFileName(scriptType string) string {
	return mainFileByScriptType(scriptType)
}
