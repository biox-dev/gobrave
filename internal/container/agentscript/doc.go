// Package agentscript 提供 Agent 内置工具所需的脚本能力：
// 创建脚本（create_script）、更新脚本信息（update_script），以及脚本 io_schema.json
// 的默认模板与清洗逻辑。
//
// 这些实现只依赖 tool 包定义的接口与业务服务，由 container 包在 DI 中装配。
package agentscript
