// Package builtin 打包随二进制分发的内置静态资源（内置提示词、内置技能与内置容器模板）。
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

// SkillFiles 内嵌 builtin/skills 下的全部内置技能，路径形如
// "skills/analysis-script-workflow/SKILL.md"。
//
// 与 PromptFiles 一样：二进制自带一份兜底副本，运行期优先读磁盘，
// 便于直接修改部署目录里的技能文件而无需重新构建。
//
//go:embed skills
var SkillFiles embed.FS

// SkillFSRoot 是 SkillFiles 中内置技能目录的相对路径。
const SkillFSRoot = "skills"

// ContainerTemplateFiles 内嵌 builtin/container_templates 下的全部内置容器模板，
// 路径形如 "container_templates/rstudio-tidyverse-r.json"。
//
// 每个文件就是一个导出格式的容器资产包（container_template_specs /
// container_template_definitions 两个平铺列表），
// 启动时由 service.InstallBuiltinContainerTemplates 逐个导入数据库。
// 与提示词 / 技能一致：运行期优先读磁盘，缺失时回退到内嵌副本。
//
//go:embed container_templates
var ContainerTemplateFiles embed.FS

// ContainerTemplateFSRoot 是 ContainerTemplateFiles 中内置容器模板目录的相对路径。
const ContainerTemplateFSRoot = "container_templates"
