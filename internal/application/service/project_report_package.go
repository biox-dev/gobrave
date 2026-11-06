package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/utils"
)

// projectReportResultsDirName 是报告条目打包结果的固定子目录名。
// 打包目录固定为 <报告工作目录>/results，每次打包都会先清空再重建。
const projectReportResultsDirName = "results"

// projectReportItemReadmeFilename 是 custom 条目的正文文件名。
const projectReportItemReadmeFilename = "README.md"

// maxProjectReportItemDirNameRunes 限制目录名长度，避免超出文件系统限制。
const maxProjectReportItemDirNameRunes = 120

// PackageProjectReportItems 把报告条目树按层级打包到报告工作目录下的 results 子目录。
//
// 目录层级与 ListProjectReportItem 返回的树一致，每个条目的目录名即其标题。
// 目录内容按 owner_type 决定：
//   - custom：正文写入该目录的 README.md；
//   - analysis_node：拷贝节点 OutputDir 下的全部内容到该目录；
//   - ai_summary：拷贝摘要正文文件（utils.GetAISummaryFile）到该目录；
//   - analysis：暂不处理，仅创建目录。
//
// 返回打包结果目录的绝对路径。重复调用会覆盖上一次的打包结果。
func (s *projectService) PackageProjectReportItems(ctx context.Context, userID int64, reportID int64) (string, error) {
	report, err := s.loadOwnedProjectReport(ctx, userID, reportID)
	if err != nil {
		return "", err
	}

	reportDir, err := s.projectReportDir(report)
	if err != nil {
		return "", err
	}

	items, err := s.ListProjectReportItemsByReportID(ctx, userID, reportID)
	if err != nil {
		return "", err
	}

	resultsDir := filepath.Join(reportDir, projectReportResultsDirName)
	// 先清空旧结果，保证目录内容与当前条目树完全一致。
	if err := os.RemoveAll(resultsDir); err != nil {
		return "", err
	}
	if err := os.MkdirAll(resultsDir, 0o755); err != nil {
		return "", err
	}

	children := projectReportItemChildren(items)
	visited := make(map[int64]bool, len(items))
	rootUsedNames := make(map[string]struct{})

	var walk func(parentID int64, parentDir string, usedNames map[string]struct{}) error
	walk = func(parentID int64, parentDir string, usedNames map[string]struct{}) error {
		for _, item := range children[parentID] {
			if visited[item.ID] {
				continue
			}
			visited[item.ID] = true

			itemDir := filepath.Join(parentDir, uniqueProjectReportItemDirName(item.Title, usedNames))
			if err := os.MkdirAll(itemDir, 0o755); err != nil {
				return err
			}
			if err := s.packageProjectReportItem(ctx, item, itemDir); err != nil {
				return fmt.Errorf("package project report item %d (%s): %w", item.ID, item.OwnerType, err)
			}
			if err := walk(item.ID, itemDir, make(map[string]struct{})); err != nil {
				return err
			}
		}
		return nil
	}

	if err := walk(0, resultsDir, rootUsedNames); err != nil {
		return "", err
	}

	// 环或不可达节点的兜底：提升为根目录下的条目，并继续处理其后代，
	// 与 flattenProjectReportItems 的容错语义保持一致。
	for _, item := range items {
		if visited[item.ID] {
			continue
		}
		visited[item.ID] = true

		itemDir := filepath.Join(resultsDir, uniqueProjectReportItemDirName(item.Title, rootUsedNames))
		if err := os.MkdirAll(itemDir, 0o755); err != nil {
			return "", err
		}
		if err := s.packageProjectReportItem(ctx, item, itemDir); err != nil {
			return "", fmt.Errorf("package project report item %d (%s): %w", item.ID, item.OwnerType, err)
		}
		if err := walk(item.ID, itemDir, make(map[string]struct{})); err != nil {
			return "", err
		}
	}

	return resultsDir, nil
}

// projectReportItemChildren 按 ParentID 分组条目，父节点缺失或自引用时提升为根节点。
func projectReportItemChildren(items []*types.ProjectReportItem) map[int64][]*types.ProjectReportItem {
	existing := make(map[int64]bool, len(items))
	for _, item := range items {
		existing[item.ID] = true
	}

	children := make(map[int64][]*types.ProjectReportItem, len(items))
	for _, item := range items {
		parentID := item.ParentID
		if parentID == item.ID || !existing[parentID] {
			parentID = 0
		}
		children[parentID] = append(children[parentID], item)
	}
	return children
}

// packageProjectReportItem 按 owner_type 把单个条目的内容写入其目录。
func (s *projectService) packageProjectReportItem(ctx context.Context, item *types.ProjectReportItem, dir string) error {
	switch item.OwnerType {
	case types.ProjectReportItemOwnerCustom:
		return os.WriteFile(filepath.Join(dir, projectReportItemReadmeFilename), []byte(item.Content), 0o644)
	case types.ProjectReportItemOwnerAnalysisNode:
		return s.copyAnalysisNodeOutput(ctx, item.OwnerID, dir)
	case types.ProjectReportItemOwnerAISummary:
		return s.copyAISummaryFile(ctx, item.OwnerID, dir)
	case types.ProjectReportItemOwnerAnalysis:
		// analysis 类型暂不处理，仅保留目录占位。
		return nil
	default:
		return nil
	}
}

// copyAnalysisNodeOutput 把节点输出目录下的全部内容拷贝到目标目录。
// 节点尚未运行或输出目录不存在时视为空内容，不报错。
func (s *projectService) copyAnalysisNodeOutput(ctx context.Context, nodeID int64, dir string) error {
	if s.analysisRepo == nil {
		return errors.New("analysis repository is unavailable")
	}

	node, err := s.analysisRepo.GetAnalysisNodeByID(ctx, nodeID)
	if err != nil {
		return err
	}

	outputDir := strings.TrimSpace(node.OutputDir)
	if outputDir == "" && strings.TrimSpace(node.WorkspaceDir) != "" {
		outputDir = utils.NodeLayoutFor(node.WorkspaceDir).OutputDir
	}
	if outputDir == "" {
		return nil
	}

	info, err := os.Stat(outputDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !info.IsDir() {
		return nil
	}

	return utils.CopyDir(outputDir, dir)
}

// copyAISummaryFile 把摘要正文文件拷贝到目标目录，文件名保持为 summary.md。
// 摘要正文文件不存在时（尚未生成）视为空内容，不报错。
func (s *projectService) copyAISummaryFile(ctx context.Context, summaryID int64, dir string) error {
	if s.summaryRepo == nil {
		return errors.New("ai summary repository is unavailable")
	}

	summary, err := s.summaryRepo.GetAISummaryByID(ctx, summaryID)
	if err != nil {
		return err
	}

	projectID := s.resolveSummaryProjectID(ctx, summary)
	if projectID == 0 {
		return nil
	}

	src := utils.GetAISummaryFile(s.storageBaseDir(), projectID, summary.ID)
	info, err := os.Stat(src)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if info.IsDir() {
		return nil
	}

	return utils.CopyFile(src, filepath.Join(dir, filepath.Base(src)), info.Mode())
}

// uniqueProjectReportItemDirName 生成同级目录内不重复的目录名。
// 标题清洗后为空时回退为 "Untitled"；重复时追加 "-2"、"-3" 等后缀。
func uniqueProjectReportItemDirName(title string, usedNames map[string]struct{}) string {
	base := sanitizeProjectReportItemDirName(title)
	name := base
	for i := 2; ; i++ {
		if _, exists := usedNames[name]; !exists {
			break
		}
		name = fmt.Sprintf("%s-%d", base, i)
	}
	usedNames[name] = struct{}{}
	return name
}

// sanitizeProjectReportItemDirName 把条目标题转换为安全的单层目录名。
func sanitizeProjectReportItemDirName(title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return "Untitled"
	}

	// 路径分隔符与控制字符一律替换为下划线，避免逃逸出父目录或生成非法文件名。
	name := strings.Map(func(r rune) rune {
		switch {
		case r == '/' || r == '\\' || r == 0:
			return '_'
		case r < 0x20 || r == 0x7f:
			return '_'
		default:
			return r
		}
	}, title)
	name = strings.Trim(name, " .")
	if name == "" {
		return "Untitled"
	}

	if runes := []rune(name); len(runes) > maxProjectReportItemDirNameRunes {
		name = strings.Trim(string(runes[:maxProjectReportItemDirNameRunes]), " .")
		if name == "" {
			return "Untitled"
		}
	}
	return name
}
