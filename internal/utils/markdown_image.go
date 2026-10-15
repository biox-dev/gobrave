package utils

import (
	"fmt"
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
