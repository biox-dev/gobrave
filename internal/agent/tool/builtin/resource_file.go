package builtin

import (
	"context"
	"fmt"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	"github.com/biox-dev/gobrave/internal/agent/tool"
	"github.com/biox-dev/gobrave/internal/agent/tool/coder/diff"
)

// 本文件提供「系统资源文件」工具层的公共能力：
//
//   - 解析模型给出的 file 类型（限定资源可用的文件集合）；
//   - 把 content / old_string / new_string 组装为 runtimecontext.ResourceFileEdit；
//   - 生成新建 / 修改文件的 diff 预览，并通过 RequirePermission 请求写授权。
//
// 与 create_report / update_report 一致：create / update 工具在函数体内显式调用
// tool.RequirePermission（而不是 WithPermissionPreview 包装），这样既能带上 diff，
// 也能在授权前完成入参校验与「编辑后内容」计算。

// parseResourceFile 解析可选的文件类型；raw 为空时返回空串。
func parseResourceFile(raw, toolName string, allowed ...runtimecontext.ResourceFile) (runtimecontext.ResourceFile, error) {
	file, err := runtimecontext.ParseResourceFile(raw, allowed...)
	if err != nil {
		return "", fmt.Errorf("%s: %w", toolName, err)
	}
	return file, nil
}

// resourceEditFrom 组装一次文件编辑。
//
// 给出了 content / old_string / new_string 但没给 file 时返回错误，避免模型写错目标文件。
func resourceEditFrom(toolName string, file runtimecontext.ResourceFile, content *string, oldString, newString string) (runtimecontext.ResourceFileEdit, error) {
	edit := runtimecontext.ResourceFileEdit{File: file, Content: content, OldString: oldString, NewString: newString}
	hasPayload := content != nil || strings.TrimSpace(oldString) != "" || strings.TrimSpace(newString) != ""
	if hasPayload && file == "" {
		return runtimecontext.ResourceFileEdit{}, fmt.Errorf("%s: file is required when content / old_string / new_string is provided", toolName)
	}
	if !hasPayload && file != "" {
		return runtimecontext.ResourceFileEdit{}, fmt.Errorf("%s: content or old_string is required when file is provided", toolName)
	}
	return edit, nil
}

// authorizeResourceWrite 在写操作执行前请求授权；preview 会作为 Operation.Metadata
// 的 file_path / diff 展示给用户。
func authorizeResourceWrite(ctx context.Context, toolName, description string, preview tool.PermissionPreview) error {
	return tool.RequirePermission(ctx, tool.PermissionRequest{
		ToolName:    toolName,
		Action:      "write",
		Description: description,
		Params:      preview,
	})
}

// previewNewFiles 为「新建文件」生成合并 diff 预览。
//
// paths 按有序的 名称 → 正文 映射（如 "io_schema.json" → 内容）；多个非空文件会拼接成
// 一段 diff，FilePath 取第一个文件，便于前端至少能定位到一个真实路径。
func previewNewFiles(files []newFilePreview) tool.PermissionPreview {
	previews := make([]string, 0, len(files))
	primaryPath := ""
	for _, file := range files {
		if strings.TrimSpace(file.Content) == "" {
			continue
		}
		if primaryPath == "" {
			primaryPath = file.Path
		}
		text, _, _ := diff.GenerateDiff("", file.Content, file.Path)
		previews = append(previews, text)
	}
	return tool.PermissionPreview{FilePath: primaryPath, Diff: strings.Join(previews, "\n")}
}

// newFilePreview 描述一个待新建文件。
type newFilePreview struct {
	Path    string
	Content string
}

// previewFileEdit 生成「before → after」的 unified diff 预览。
func previewFileEdit(path, before, after string) tool.PermissionPreview {
	return textDiffPreview(path, before, after)
}

// resourceFilesSchema 构造 get 工具的 files 参数 schema（数组枚举）。
func resourceFilesSchema(description string, allowed ...runtimecontext.ResourceFile) map[string]any {
	return map[string]any{
		"type":        "array",
		"description": description,
		"items": map[string]any{
			"type": "string",
			"enum": runtimecontext.ResourceFiles(allowed...),
		},
	}
}

// collectResourceFiles 规范化 get 工具的 files 入参：空时返回默认集合，去重并校验。
func collectResourceFiles(raw []string, toolName string, defaults []runtimecontext.ResourceFile, allowed ...runtimecontext.ResourceFile) ([]runtimecontext.ResourceFile, error) {
	if len(raw) == 0 {
		return defaults, nil
	}
	seen := make(map[runtimecontext.ResourceFile]struct{}, len(raw))
	out := make([]runtimecontext.ResourceFile, 0, len(raw))
	for _, item := range raw {
		file, err := parseResourceFile(item, toolName, allowed...)
		if err != nil {
			return nil, err
		}
		if file == "" {
			continue
		}
		if _, ok := seen[file]; ok {
			continue
		}
		seen[file] = struct{}{}
		out = append(out, file)
	}
	return out, nil
}

// readResourceFiles 按文件类型读取正文，结果按 string(file) 索引回传。
func readResourceFiles(files []runtimecontext.ResourceFile, read func(runtimecontext.ResourceFile) (runtimecontext.ResourceFileContent, error)) (map[string]runtimecontext.ResourceFileContent, error) {
	out := make(map[string]runtimecontext.ResourceFileContent, len(files))
	for _, file := range files {
		content, err := read(file)
		if err != nil {
			return nil, err
		}
		out[string(file)] = content
	}
	return out, nil
}
