package runtimecontext

import (
	"errors"
	"fmt"
	"strings"
)

// 本文件定义「系统资源文件」的统一读写模型。
//
// Agent 对脚本 / 工作流 / 分析节点 / 分析运行这些系统资源的增改查，全部通过工具完成，
// 不再经由 coder 的 view / write / edit 或 bash：
//
//   - get_* 工具按 ResourceFile 读取正文并直接回传；
//   - create_* 工具接受文件正文，由业务实现落盘；
//   - update_* 工具按 ResourceFile 做「字段覆盖（content）」或「文本替换（old_string → new_string）」。
//
// 这样文件路径与落盘口径只在业务实现里维护，工具层只描述「改哪个资源文件的什么内容」。

// ResourceFile 是 Agents 可直接读写的系统资源文件类型。
//
// 取值按资源域复用，但含义明确：
//   - 脚本：script_file（主文件）/ io_schema / readme；
//   - 工作流：dag_definition / io_schema / readme；
//   - 分析节点：command（run.sh）/ params（params.json）；
//   - 分析运行：params（params.json）。
type ResourceFile string

const (
	// ResourceFileScript 是脚本主文件（main.R / main.py / main.sh ...）。
	ResourceFileScript ResourceFile = "script_file"
	// ResourceFileIOSchema 是 io_schema.json（脚本 / 工作流级输入输出契约）。
	ResourceFileIOSchema ResourceFile = "io_schema"
	// ResourceFileReadme 是 README.md（脚本 / 工作流说明文档）。
	ResourceFileReadme ResourceFile = "readme"
	// ResourceFileDagDefinition 是 dag_definition.json（工作流 DAG 定义）。
	ResourceFileDagDefinition ResourceFile = "dag_definition"
	// ResourceFileParams 是 params.json（节点 / 分析运行的运行参数）。
	ResourceFileParams ResourceFile = "params"
	// ResourceFileCommand 是命令型节点的 run.sh。
	ResourceFileCommand ResourceFile = "command"
)

var resourceFileDescriptions = map[ResourceFile]string{
	ResourceFileScript:        "脚本主文件（main.R / main.py / main.sh 等）",
	ResourceFileIOSchema:      "io_schema.json（输入输出契约）",
	ResourceFileReadme:        "README.md（说明文档）",
	ResourceFileDagDefinition: "dag_definition.json（DAG 定义）",
	ResourceFileParams:        "params.json（运行参数）",
	ResourceFileCommand:       "command（命令型节点 run.sh）",
}

// Valid 判断是否为已定义的资源文件类型。
func (f ResourceFile) Valid() bool {
	_, ok := resourceFileDescriptions[f]
	return ok
}

// Description 返回资源文件的中文说明。
func (f ResourceFile) Description() string {
	if desc, ok := resourceFileDescriptions[f]; ok {
		return desc
	}
	return string(f)
}

// resourceFileAliases 把工具回传的「路径字段名」映射为规范的文件类型，
// 便于模型直接传 script_file_path / io_schema_path / readme_file 等字段名。
var resourceFileAliases = map[ResourceFile]ResourceFile{
	"script_file_path":    ResourceFileScript,
	"io_schema_path":      ResourceFileIOSchema,
	"readme_file":         ResourceFileReadme,
	"dag_definition_path": ResourceFileDagDefinition,
	"params_path":         ResourceFileParams,
	"command_path":        ResourceFileCommand,
}

// ParseResourceFile 解析并校验模型给出的文件类型。
//
// raw 为空时返回空串与 nil（表示「未指定」）；allowed 非空时必须是其中之一。
// 同时接受路径字段名别名（script_file_path / io_schema_path / readme_file 等）。
func ParseResourceFile(raw string, allowed ...ResourceFile) (ResourceFile, error) {
	file := ResourceFile(strings.TrimSpace(raw))
	if file == "" {
		return "", nil
	}
	if canonical, ok := resourceFileAliases[file]; ok {
		file = canonical
	}
	if !file.Valid() {
		return "", fmt.Errorf("invalid file %q: expected one of %s", raw, joinResourceFiles(allowedOrAll(allowed)))
	}
	if len(allowed) > 0 {
		for _, candidate := range allowed {
			if file == candidate {
				return file, nil
			}
		}
		return "", fmt.Errorf("invalid file %q for this resource: expected one of %s", raw, joinResourceFiles(allowed))
	}
	return file, nil
}

// ResourceFiles 返回一组资源文件类型的可读名称列表（用于工具 schema 的 enum）。
func ResourceFiles(files ...ResourceFile) []string {
	out := make([]string, 0, len(files))
	for _, file := range files {
		out = append(out, string(file))
	}
	return out
}

// allowedOrAll 返回 allowed；为空时返回全部已定义类型。
func allowedOrAll(allowed []ResourceFile) []ResourceFile {
	if len(allowed) > 0 {
		return allowed
	}
	all := make([]ResourceFile, 0, len(resourceFileDescriptions))
	for file := range resourceFileDescriptions {
		all = append(all, file)
	}
	return all
}

// joinResourceFiles 把文件类型列表拼成 a / b / c 形式，用于错误提示。
func joinResourceFiles(files []ResourceFile) string {
	parts := make([]string, 0, len(files))
	for _, file := range files {
		parts = append(parts, string(file))
	}
	return strings.Join(parts, " / ")
}

// ResourceFileContent 是资源文件的正文读取结果。
type ResourceFileContent struct {
	File    ResourceFile `json:"file"`
	Path    string       `json:"path"`
	Content string       `json:"content"`
	Exists  bool         `json:"exists"`
}

// NewResourceFileContent 依据磁盘读取结果构造正文视图；data 为 nil 表示文件不存在。
func NewResourceFileContent(file ResourceFile, path string, data []byte) ResourceFileContent {
	if data == nil {
		return ResourceFileContent{File: file, Path: path}
	}
	return ResourceFileContent{File: file, Path: path, Content: string(data), Exists: true}
}

// ResourceFileEdit 描述一次资源文件编辑。
//
// 两种模式互斥：
//   - Content != nil：整体覆盖文件正文；
//   - 否则：按 OldString → NewString 做一次唯一文本替换。
type ResourceFileEdit struct {
	File      ResourceFile
	Content   *string
	OldString string
	NewString string
}

// IsZero 表示本次没有给出文件编辑。
func (e ResourceFileEdit) IsZero() bool {
	return e.File == "" && e.Content == nil && e.OldString == "" && e.NewString == ""
}

// Apply 把编辑应用到 before 正文，返回 after 正文。
//
// 语义与 coder 的 edit / write 一致：内容必须真实改变；文本替换要求 old_string 唯一出现。
func (e ResourceFileEdit) Apply(before string) (string, error) {
	if e.Content != nil {
		after := *e.Content
		if after == before {
			return "", errors.New("new content is the same as old content. No changes made")
		}
		return after, nil
	}
	if strings.TrimSpace(e.OldString) == "" {
		return "", errors.New("old_string is required when content is not provided")
	}
	index := strings.Index(before, e.OldString)
	if index == -1 {
		return "", errors.New("old_string not found in file. Make sure it matches exactly, including whitespace and line breaks")
	}
	if last := strings.LastIndex(before, e.OldString); last != index {
		return "", errors.New("old_string appears multiple times in the file. Please provide more context to ensure a unique match")
	}
	after := before[:index] + e.NewString + before[index+len(e.OldString):]
	if after == before {
		return "", errors.New("new content is the same as old content. No changes made")
	}
	return after, nil
}
