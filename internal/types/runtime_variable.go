package types

// RuntimeVariableCategory 对运行时变量按领域分组，用于前端分类展示。
type RuntimeVariableCategory string

const (
	RuntimeVariableCategorySystem     RuntimeVariableCategory = "system"
	RuntimeVariableCategoryPackage    RuntimeVariableCategory = "package"
	RuntimeVariableCategoryProject    RuntimeVariableCategory = "project"
	RuntimeVariableCategoryWorkspace  RuntimeVariableCategory = "workspace"
	RuntimeVariableCategoryAppSession RuntimeVariableCategory = "app_session"
	RuntimeVariableCategoryDagNode    RuntimeVariableCategory = "dag_node"
)

// RuntimeVariableSource 表示变量取值的来源。
type RuntimeVariableSource string

const (
	RuntimeVariableSourceStatic   RuntimeVariableSource = "static"   // 固定/派生路径
	RuntimeVariableSourceEnv      RuntimeVariableSource = "env"      // 来自进程环境变量
	RuntimeVariableSourceDB       RuntimeVariableSource = "db"       // 来自 session/node/project 记录
	RuntimeVariableSourceComputed RuntimeVariableSource = "computed" // 运行时计算
)

// RuntimeVariable 是一条已求值的运行时变量，附带展示元数据。
type RuntimeVariable struct {
	Name        string                  `json:"name"`
	Value       string                  `json:"value"`
	Category    RuntimeVariableCategory `json:"category"`
	Description string                  `json:"description"`
	Source      RuntimeVariableSource   `json:"source"`
}

// RuntimeVariableDefinition 是某个变量名的声明式元数据（单一事实来源）。
type RuntimeVariableDefinition struct {
	Name        string                  `json:"name"`
	Category    RuntimeVariableCategory `json:"category"`
	Description string                  `json:"description"`
	Source      RuntimeVariableSource   `json:"source"`
}
