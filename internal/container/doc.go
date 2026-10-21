// Package container 是进程级依赖装配（composition root）：把业务服务、DAG 运行时
// 与 Agent 能力装配到一起，并提供 BuildContainer 入口。
//
// Agent 内置工具所需的适配实现按领域拆到子包，避免根目录堆积：
//   - agentanalysis：分析节点相关（运行节点、生成参数、更新节点、为脚本创建节点、创建命令型节点）；
//   - agentscript：脚本相关（创建脚本、更新脚本、io_schema 默认模板）。
//
// 与具体领域无关的小型适配（文件读取、项目资源浏览、用户上下文回写、长期记忆保存）
// 仍保留在本包中。
package container
