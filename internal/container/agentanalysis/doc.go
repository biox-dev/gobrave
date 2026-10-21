// Package agentanalysis 提供 Agent 内置工具所需的「分析节点」能力：
// 运行节点（run_analysis_node）、生成节点参数（build_analysis_node_params）、
// 更新节点信息（update_analysis_node）、为已有脚本创建节点（create_script_analysis），
// 以及创建命令型节点并可选运行（create_agent_analysis）。
//
// 这些实现只依赖 tool 包定义的接口与业务服务，由 container 包在 DI 中装配。
package agentanalysis
