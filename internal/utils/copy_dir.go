package utils

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// CopyDirReplace 用 srcDir 的内容完全替换 dstDir（先删除 dstDir 再重建后拷贝）。
//
// 用途是导出/安装两侧的目录快照与还原，例如把脚本目录快照进 workflow 目录，
// 或把 store 同步回来的脚本目录还原到本地脚本目录。
//
// exclude 里的名称在任意层级都会被跳过（目录则整棵跳过），例如脚本目录自身的 ".git"：
// 把脚本快照进 workflow 目录时不能把脚本仓库一起塞进去。
//
// srcDir 不存在时视为无可拷贝内容，直接返回 nil（导出时脚本尚未落盘不应让整次保存失败）。
func CopyDirReplace(srcDir string, dstDir string, exclude ...string) error {
	skipped := make(map[string]struct{}, len(exclude))
	for _, name := range exclude {
		if name = strings.TrimSpace(name); name != "" {
			skipped[name] = struct{}{}
		}
	}

	info, err := os.Stat(srcDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("source is not a directory: %s", srcDir)
	}

	if err := os.RemoveAll(dstDir); err != nil {
		return err
	}
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return err
	}

	return filepath.Walk(srcDir, func(path string, fileInfo os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relPath, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		if relPath == "." {
			return nil
		}
		if _, ok := skipped[fileInfo.Name()]; ok {
			if fileInfo.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		targetPath := filepath.Join(dstDir, relPath)
		if fileInfo.IsDir() {
			return os.MkdirAll(targetPath, 0o755)
		}

		if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
			return err
		}

		srcFile, err := os.Open(path)
		if err != nil {
			return err
		}
		defer srcFile.Close()

		dstFile, err := os.OpenFile(targetPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, fileInfo.Mode())
		if err != nil {
			return err
		}
		defer dstFile.Close()

		_, err = io.Copy(dstFile, srcFile)
		return err
	})
}
