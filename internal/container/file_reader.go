package container

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
)

// fileReader 实现 runtimecontext.FileReader：按 go_file 主键解析文件路径，并读取文件内容，
// 供内置工具 get_file 使用（agent 传 @file:<id> 冒号后的裸 ID）。
//
// 它复用 DataService（ID → 文件元信息 / path）与 FileContentService（按行/行分页读取）。
type fileReader struct {
	dataService interfaces.DataService
	fileService interfaces.FileContentService
}

// newFileReader 构建文件读取器并注入工具体系。
func newFileReader(dataService interfaces.DataService, fileService interfaces.FileContentService) runtimecontext.FileReader {
	return &fileReader{
		dataService: dataService,
		fileService: fileService,
	}
}

// ReadFileByID 按 go_file 主键 ID 查询文件元信息（含真实路径）。
func (r *fileReader) ReadFileByID(ctx context.Context, fileID int64) (runtimecontext.FileReadResult, error) {
	if fileID <= 0 {
		return runtimecontext.FileReadResult{FileID: fileID}, fmt.Errorf("invalid file id")
	}
	if r.dataService == nil {
		return runtimecontext.FileReadResult{FileID: fileID}, fmt.Errorf("data service is not configured")
	}

	file, err := r.dataService.GetFileByID(ctx, fileID)
	if err != nil {
		return runtimecontext.FileReadResult{FileID: fileID}, fmt.Errorf("failed to get file by id: %w", err)
	}
	if file == nil {
		return runtimecontext.FileReadResult{FileID: fileID}, fmt.Errorf("file %d not found", fileID)
	}

	return runtimecontext.FileReadResult{
		FileID:   file.ID,
		FileName: file.FileName,
		FilePath: file.Path,
		Format:   normalizeFormat(file.Format, file.Path),
	}, nil
}

// ReadFileContent 按路径读取文件内容：表格（csv/tsv、xlsx 转 TSV）返回分隔文本，
// 文本文件返回按 offset / limit 分页的原文，二进制 / 图片返回说明。
// 格式优先用 in.Format（来自 go_file.format），为空时由 FileContentService 回退到路径后缀。
func (r *fileReader) ReadFileContent(ctx context.Context, in runtimecontext.FileContentRequest) (runtimecontext.FileContentResult, error) {
	if strings.TrimSpace(in.Path) == "" {
		return runtimecontext.FileContentResult{}, fmt.Errorf("file path is required")
	}
	if r.fileService == nil {
		return runtimecontext.FileContentResult{}, fmt.Errorf("file content service is not configured")
	}

	content, err := r.fileService.ReadFileContent(ctx, interfaces.FileContentInput{
		FilePath: in.Path,
		Format:   in.Format,
		Offset:   in.Offset,
		Limit:    in.Limit,
		MaxBytes: in.MaxBytes,
	})
	if err != nil {
		return runtimecontext.FileContentResult{}, fmt.Errorf("failed to read %s: %w", in.Path, err)
	}

	return runtimecontext.FileContentResult{
		Format:     content.Format,
		Kind:       string(content.Kind),
		Delimiter:  content.Delimiter,
		Content:    content.Content,
		Offset:     content.Offset,
		LineCount:  content.LineCount,
		NextOffset: content.NextOffset,
		HasMore:    content.HasMore,
		Truncated:  content.Truncated,
		Message:    content.Message,
	}, nil
}

// normalizeFormat 推断文件格式：优先用 file.Format，为空时回退到路径后缀。
func normalizeFormat(format, path string) string {
	normalized := strings.TrimSpace(strings.ToLower(strings.TrimPrefix(format, ".")))
	if normalized == "" {
		normalized = strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))
	}
	return normalized
}
