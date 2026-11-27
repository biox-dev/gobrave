package utils

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// markdownImagePattern 匹配 markdown 图片语法 ![alt](url)。
var markdownImagePattern = regexp.MustCompile(`!\[([^\]]*)\]\(([^)]+)\)`)

// PrefixMarkdownImageURLs 为 markdown 内容中的相对图片路径拼上 prefix
// （通常是 GetAnalysisURLPrefix 返回的数据目录相对路径）。
// prefix 为空时原样返回；已是绝对路径/URL 的图片保持不变。
//
// 图片路径一旦拼成相对 data 目录的绝对路径（如 /34/analysis_node/.../a.png），
// 内容就与部署无关，展示侧只需统一补上 /data-analysis 即可。
func PrefixMarkdownImageURLs(content, prefix string) string {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return content
	}

	return markdownImagePattern.ReplaceAllStringFunc(content, func(match string) string {
		sub := markdownImagePattern.FindStringSubmatch(match)
		if len(sub) != 3 {
			return match
		}
		alt, url := sub[1], strings.TrimSpace(sub[2])
		if !isRelativeResourceURL(url) {
			return match
		}
		return fmt.Sprintf("![%s](%s%s)", alt, prefix, strings.TrimPrefix(url, "./"))
	})
}

// RewriteMarkdownImageBaseDirPaths 把 markdown 图片 URL 中以 baseDir 开头的本机绝对路径
// 改写为以数据目录为根的路径：图片所在目录经 GetAnalysisURLPrefix 解析成前缀后，
// 再拼回文件名。例如（baseDir=/data2/brave_analysis_workspace）：
//
//	![a](/data2/brave_analysis_workspace/data/36/analysis_node/1/2/output/a.png)
//	-> ![a](/36/analysis_node/1/2/output/a.png)
//
// 报告正文可能被 agent 写成节点输出目录的本机绝对路径，这类路径既暴露部署细节，
// 浏览器也访问不到；改写后与节点产物保持同一种形态（见 dag.NodeCompletionCoordinator），
// 展示侧只需统一补上 /data-analysis 前缀。baseDir 为空或图片不在 baseDir 下时原样返回。
func RewriteMarkdownImageBaseDirPaths(content, baseDir string) string {
	baseDir = strings.TrimSpace(baseDir)
	if baseDir == "" {
		return content
	}
	root := strings.TrimRight(baseDir, string(filepath.Separator)) + string(filepath.Separator)

	return markdownImagePattern.ReplaceAllStringFunc(content, func(match string) string {
		sub := markdownImagePattern.FindStringSubmatch(match)
		if len(sub) != 3 {
			return match
		}
		alt, url := sub[1], strings.TrimSpace(sub[2])
		if !strings.HasPrefix(url, root) {
			return match
		}
		// baseDir 这一段替换成图片目录的数据相对前缀（带尾斜杠），其余路径原样保留。
		prefix := GetAnalysisURLPrefix(baseDir, filepath.Dir(url))
		if prefix == "" {
			return match
		}
		return fmt.Sprintf("![%s](%s%s)", alt, prefix, filepath.Base(url))
	})
}

// isRelativeResourceURL 判断 url 是否为需要拼前缀的相对路径。
// 绝对 URL（带 scheme）、协议相对地址、根路径、锚点与 data URI 均视为非相对。
func isRelativeResourceURL(url string) bool {
	if url == "" {
		return false
	}
	if strings.HasPrefix(url, "/") || strings.HasPrefix(url, "#") {
		return false
	}
	if strings.Contains(url, "://") {
		return false
	}
	lower := strings.ToLower(url)
	if strings.HasPrefix(lower, "data:") || strings.HasPrefix(lower, "mailto:") {
		return false
	}
	return true
}
