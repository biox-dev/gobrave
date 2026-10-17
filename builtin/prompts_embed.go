// Package builtin 打包随二进制分发的内置静态资源（目前是内置提示词）。
//
// 为什么用 //go:embed 而不是只在运行时读磁盘：多阶段构建的镜像里通常只拷贝
// 二进制本身，builtin/prompts 不会出现在运行目录；用 embed 后提示词随二进制
// 一起分发，单文件部署（含 Docker 镜像）即可正常工作。
// 同时运行期仍会先读磁盘（见 utils.ReadBuiltinPromptFile），便于运维直接覆盖、
// 调试线上提示词而无需重新构建。
package builtin

import "embed"

// PromptFiles 内嵌 builtin/prompts 下的全部提示词文件，路径形如 "prompts/default.md"。
//
//go:embed prompts
var PromptFiles embed.FS

// PromptFSRoot 是 PromptFiles 中提示词目录的相对路径。
const PromptFSRoot = "prompts"
