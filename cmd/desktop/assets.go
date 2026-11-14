package main

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"

	frontendassets "github.com/biox-dev/gobrave/frontend"
	"github.com/biox-dev/gobrave/internal/logger"
	"github.com/biox-dev/gobrave/internal/utils"
)

// 桌面窗口的前端资源只有一处来源：gobrave 的 frontend/web（与 Web 版完全一致，不分叉）。
//
// 运行时查找顺序：
//  1. -tags embed_frontend 构建时，内嵌 frontend/web（发布/独立分发用）；
//  2. 可执行文件旁的 web/ 或 frontend/web/（部署时可替换前端而不重编二进制）；
//  3. 工作目录下的 frontend/web（源码目录直接 go run）。
//
// 未使用 -tags embed_frontend 时需要磁盘上存在 frontend/web，否则窗口会显示错误页。
func resolveWebFS() fs.FS {
	// frontend.EmbeddedFrontendFS() 由 frontend_assets_embed.go / frontend_assets_none.go
	// 两个 build tag 文件提供：带 embed_frontend 时返回内嵌的 web/，否则返回 nil。
	// 内嵌 FS 保留了 "web/" 前缀，这里用 fs.Sub 下移，使根目录下直接是 index.html。
	if root, ok := frontendassets.EmbeddedFrontendFS(); ok {
		webFS, err := fs.Sub(root, "web")
		if err == nil {
			logger.Infof(context.Background(), "[desktop] 使用内嵌前端资源 (frontend/web)")
			return webFS
		}
		logger.Warnf(context.Background(), "[desktop] 无法从内嵌资源中提取 web 子目录: %v", err)
	}

	for _, candidate := range []string{"web", filepath.Join("frontend", "web")} {
		absDir, err := utils.ResolveExecutableRelativePath(candidate)
		if err != nil {
			continue
		}
		if _, statErr := fs.Stat(os.DirFS(absDir), "index.html"); statErr == nil {
			logger.Infof(context.Background(), "[desktop] 使用磁盘前端资源: %s", absDir)
			return os.DirFS(absDir)
		}
	}

	webDir := filepath.Join("frontend", "web")
	if _, err := os.Stat(filepath.Join(webDir, "index.html")); err != nil {
		logger.Warnf(context.Background(), "[desktop] 未找到前端资源，请先构建 frontend/web（或使用 -tags embed_frontend）")
	}
	logger.Infof(context.Background(), "[desktop] 回退到工作目录: %s", webDir)
	return os.DirFS(webDir)
}
