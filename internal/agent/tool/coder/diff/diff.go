// Package diff 提供文件补丁解析与应用能力（从 opencode internal/diff 迁移）。
//
// 与 opencode 版本的差异：去掉 chroma / lipgloss / tui/theme 等终端渲染依赖，
// 只保留工具真正需要的部分——生成 unified diff、解析 patch 文本、构建并应用 commit。
package diff

import (
	"strings"

	"github.com/aymanbagabas/go-udiff"
)

// GenerateDiff 生成 before → after 的 unified diff，并返回新增 / 删除行数。
//
// fileName 仅用于 diff 头（a/<name> b/<name>）；相对路径会去掉工作目录前缀。
func GenerateDiff(beforeContent, afterContent, fileName string) (string, int, int) {
	fileName = strings.TrimPrefix(fileName, "/")

	unified := udiff.Unified("a/"+fileName, "b/"+fileName, beforeContent, afterContent)

	additions, removals := 0, 0
	for line := range strings.SplitSeq(unified, "\n") {
		switch {
		case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
			additions++
		case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
			removals++
		}
	}
	return unified, additions, removals
}
