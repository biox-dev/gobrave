// Package agentworkflow 提供 Agent 内置工具所需的「工作流」写能力：
// 创建工作流（create_workflow）与更新工作流（update_workflow）。
//
// 只读能力（get_workflow）在 container 的 projectResourceBrowser 中实现，
// 与 get_script / get_analysis_node 共用同一份读取口径。
//
// 这些实现只依赖 tool 包定义的接口与业务服务，由 container 包在 DI 中装配。
package agentworkflow
