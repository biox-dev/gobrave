package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/biox-dev/gobrave/internal/config"
	"github.com/biox-dev/gobrave/internal/logger"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"github.com/biox-dev/gobrave/internal/utils"
	"github.com/xuri/excelize/v2"
)

const (
	// filePreviewMaxBytes caps how much text is returned per read when the
	// caller does not pass a byte budget.
	filePreviewMaxBytes = 5 << 20 // 5 MiB
	// filePreviewDefaultRows caps how many Excel rows are returned when the
	// caller does not pass a limit (keeps the old preview behavior).
	filePreviewDefaultRows = 2000
	// filePreviewMaxLineLength truncates overly wide lines/rows so a single
	// line cannot blow the context budget.
	filePreviewMaxLineLength = 2000
	// filePreviewBinarySampleSize is the prefix scanned for binary content.
	filePreviewBinarySampleSize = 8192
)

// binaryPreviewFormats are formats that cannot be rendered as text in the UI.
var binaryPreviewFormats = map[string]bool{
	"gz": true, "bgz": true, "bz2": true, "xz": true, "zip": true, "tar": true,
	"tgz": true, "7z": true, "rar": true, "zst": true,
	"rds": true, "rdata": true, "rda": true,
	"bam": true, "bai": true, "cram": true, "crai": true, "bcf": true, "tbi": true, "csi": true,
	"pdf": true, "png": true, "jpg": true, "jpeg": true, "gif": true, "bmp": true,
	"webp": true, "tif": true, "tiff": true, "ico": true, "svgz": true,
	"doc": true, "docx": true, "ppt": true, "pptx": true, "xls": true, "xlsb": true,
	"ods": true, "h5": true, "hdf5": true, "he5": true,
	"feather": true, "parquet": true, "arrow": true, "npz": true, "npy": true,
	"pkl": true, "pickle": true, "joblib": true, "db": true, "sqlite": true, "sqlite3": true,
	"bw": true, "bigwig": true, "woff": true, "woff2": true, "ttf": true, "otf": true,
	"exe": true, "dll": true, "so": true, "bin": true,
}

const (
	binaryPreviewMessage = "该文件为二进制格式，暂不支持在线预览"
	imagePreviewMessage  = "图片文件，无法以文本展示；file_path 可直接交给容器命令或其它工具处理。"
)

// binaryContentMessage returns a message explaining why a format cannot be
// rendered as text. Images get a more specific hint.
func binaryContentMessage(format string) string {
	switch format {
	case "png", "jpg", "jpeg", "gif", "bmp", "webp", "tif", "tiff", "ico", "svgz":
		return imagePreviewMessage
	default:
		return binaryPreviewMessage
	}
}

type fileContentService struct {
	baseDir string
}

func NewFileContentService(cfg *config.Config) interfaces.FileContentService {
	baseDir := ""
	if cfg != nil && cfg.Storage != nil {
		configured := strings.TrimSpace(cfg.Storage.BaseDir)
		if configured != "" {
			baseDir = configured
		}
	}

	return &fileContentService{baseDir: baseDir}
}

// ReadFileContent reads a local file and returns a UI-renderable payload.
//
// Tables (csv/tsv, xlsx converted to TSV) are returned as delimited text the UI
// parses, text files as raw text, and binary files as a message. Offset/Limit
// page through the file by line/row so the Agent get_file tool can read large
// files incrementally; the preview API passes zero values.
func (s *fileContentService) ReadFileContent(ctx context.Context, in interfaces.FileContentInput) (*interfaces.FilePreviewResult, error) {
	logger.Debugf(ctx, "[FileContent] read content request: file_path=%s format=%s offset=%d limit=%d", in.FilePath, in.Format, in.Offset, in.Limit)
	resolvedPath, err := s.resolvePath(ctx, in.FilePath)
	if err != nil {
		return nil, err
	}

	normalizedFormat := normalizePreviewFormat(in.Format, resolvedPath)
	offset := in.Offset
	if offset < 0 {
		offset = 0
	}
	maxBytes := in.MaxBytes
	if maxBytes <= 0 {
		maxBytes = filePreviewMaxBytes
	}
	logger.Debugf(ctx, "[FileContent] read content resolved: resolved_path=%s format=%s", resolvedPath, normalizedFormat)

	info, err := os.Stat(resolvedPath)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("%s is a directory, not a file", resolvedPath)
	}

	switch normalizedFormat {
	case "xlsx", "xlsm":
		content, lineCount, hasMore, truncated, sheetNote, err := s.readExcelContent(resolvedPath, offset, in.Limit, maxBytes)
		if err != nil {
			return nil, err
		}
		return newTableContentResult(resolvedPath, normalizedFormat, "\t", content, offset, lineCount, hasMore, truncated, sheetNote), nil
	case "csv":
		return s.readDelimitedContent(resolvedPath, normalizedFormat, ',', offset, in.Limit, maxBytes)
	case "tsv", "tab":
		return s.readDelimitedContent(resolvedPath, normalizedFormat, '\t', offset, in.Limit, maxBytes)
	}

	if binaryPreviewFormats[normalizedFormat] {
		return &interfaces.FilePreviewResult{
			FilePath: resolvedPath,
			Format:   normalizedFormat,
			Kind:     interfaces.FilePreviewKindBinary,
			Message:  binaryContentMessage(normalizedFormat),
		}, nil
	}

	content, lineCount, hasMore, truncated, err := s.readTextContent(resolvedPath, offset, in.Limit, maxBytes)
	if err != nil {
		return nil, err
	}
	// Unknown extension: sniff the content so accidental binaries do not end up
	// rendered as mojibake text in the UI.
	if looksBinary([]byte(content)) {
		return &interfaces.FilePreviewResult{
			FilePath: resolvedPath,
			Format:   normalizedFormat,
			Kind:     interfaces.FilePreviewKindBinary,
			Message:  binaryContentMessage(normalizedFormat),
		}, nil
	}

	result := &interfaces.FilePreviewResult{
		FilePath:  resolvedPath,
		Format:    normalizedFormat,
		Kind:      interfaces.FilePreviewKindText,
		Content:   content,
		Offset:    offset,
		LineCount: lineCount,
		Truncated: truncated,
		HasMore:   hasMore,
	}
	if hasMore {
		result.NextOffset = offset + lineCount
	}
	if lineCount == 0 && !hasMore && offset > 0 {
		result.Message = fmt.Sprintf("offset %d 超出文件行数，文件已读取完毕。", offset)
	}
	return result, nil
}

// newTableContentResult builds a table result and derives the paging fields.
func newTableContentResult(filePath, format, delimiter, content string, offset, lineCount int, hasMore, truncated bool, message string) *interfaces.FilePreviewResult {
	if message == "" && lineCount == 0 && !hasMore && offset > 0 {
		message = fmt.Sprintf("offset %d 超出文件行数，文件已读取完毕。", offset)
	}
	result := &interfaces.FilePreviewResult{
		FilePath:  filePath,
		Format:    format,
		Kind:      interfaces.FilePreviewKindTable,
		Delimiter: delimiter,
		Content:   content,
		Offset:    offset,
		LineCount: lineCount,
		Truncated: truncated,
		HasMore:   hasMore,
		Message:   message,
	}
	if hasMore {
		result.NextOffset = offset + lineCount
	}
	return result
}

// readTextContent reads lines [offset, offset+limit) from a text file. A limit
// <= 0 means no row limit (bounded by maxBytes). It returns the lines read,
// whether more content remains, and whether Content was cut.
func (s *fileContentService) readTextContent(filePath string, offset, limit, maxBytes int) (string, int, bool, bool, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", 0, false, false, err
	}
	defer file.Close()

	// bufio.Reader (not Scanner) so a single very long line cannot hit a token
	// limit; the line is truncated to filePreviewMaxLineLength below anyway.
	reader := bufio.NewReaderSize(file, 64*1024)

	for i := 0; i < offset; i++ {
		if _, err := reader.ReadString('\n'); err != nil {
			if err == io.EOF {
				return "", 0, false, false, nil
			}
			return "", 0, false, false, err
		}
	}

	var b strings.Builder
	lineCount := 0
	truncated := false
	hasMore := false
	for {
		raw, readErr := reader.ReadString('\n')
		if len(raw) == 0 && readErr != nil {
			if readErr == io.EOF {
				break
			}
			return "", 0, false, false, readErr
		}
		if limit > 0 && lineCount >= limit {
			hasMore = true
			truncated = true
			break
		}

		line := strings.TrimSuffix(strings.TrimSuffix(raw, "\n"), "\r")
		if lineCount == 0 && offset == 0 {
			line = strings.TrimPrefix(line, "\ufeff")
		}
		if len(line) > filePreviewMaxLineLength {
			line = line[:filePreviewMaxLineLength] + "..."
			truncated = true
		}
		cost := len(line)
		if b.Len() > 0 {
			cost++
		}
		if b.Len()+cost > maxBytes {
			hasMore = true
			truncated = true
			break
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(line)
		lineCount++

		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return "", 0, false, false, readErr
		}
	}

	return b.String(), lineCount, hasMore, truncated, nil
}

// readDelimitedContent reads rows [offset, offset+limit) from a csv/tsv file and
// re-serializes them with the same delimiter.
func (s *fileContentService) readDelimitedContent(filePath, format string, delimiter rune, offset, limit, maxBytes int) (*interfaces.FilePreviewResult, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	reader := csv.NewReader(file)
	reader.Comma = delimiter
	reader.FieldsPerRecord = -1
	reader.LazyQuotes = true

	var b strings.Builder
	writer := csv.NewWriter(&b)
	writer.Comma = delimiter

	lineCount := 0
	hasMore := false
	truncated := false
	skipped := 0
	for {
		record, readErr := reader.Read()
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, readErr
		}
		if skipped < offset {
			skipped++
			continue
		}
		if limit > 0 && lineCount >= limit {
			hasMore = true
			truncated = true
			break
		}

		if lineCount == 0 && len(record) > 0 {
			record[0] = strings.TrimPrefix(record[0], "\ufeff")
		}
		cost := 0
		for _, field := range record {
			cost += len(field) + 1
		}
		if b.Len()+cost > maxBytes {
			hasMore = true
			truncated = true
			break
		}
		if err := writer.Write(record); err != nil {
			return nil, err
		}
		lineCount++
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return nil, err
	}

	return newTableContentResult(filePath, format, string(delimiter), b.String(), offset, lineCount, hasMore, truncated, ""), nil
}

// readExcelContent converts the first worksheet to TSV rows
// [offset, offset+limit). A limit <= 0 uses filePreviewDefaultRows.
func (s *fileContentService) readExcelContent(filePath string, offset, limit, maxBytes int) (string, int, bool, bool, string, error) {
	f, err := excelize.OpenFile(filePath)
	if err != nil {
		return "", 0, false, false, "", err
	}
	defer f.Close()

	sheetNames := f.GetSheetList()
	if len(sheetNames) == 0 {
		return "", 0, false, false, "", nil
	}
	if limit <= 0 {
		limit = filePreviewDefaultRows
	}

	rows, err := f.Rows(sheetNames[0])
	if err != nil {
		return "", 0, false, false, "", err
	}
	defer rows.Close()

	var b strings.Builder
	writer := csv.NewWriter(&b)
	writer.Comma = '\t'

	rowIndex := 0
	lineCount := 0
	hasMore := false
	truncated := false
	for rows.Next() {
		if rowIndex < offset {
			rowIndex++
			continue
		}
		if lineCount >= limit {
			hasMore = true
			truncated = true
			break
		}

		row, err := rows.Columns()
		if err != nil {
			return "", 0, false, false, "", err
		}
		cost := 0
		for _, field := range row {
			cost += len(field) + 1
		}
		if b.Len()+cost > maxBytes {
			hasMore = true
			truncated = true
			break
		}
		if err := writer.Write(row); err != nil {
			return "", 0, false, false, "", err
		}
		lineCount++
		rowIndex++
	}
	if err := rows.Error(); err != nil {
		return "", 0, false, false, "", err
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return "", 0, false, false, "", err
	}

	note := ""
	if len(sheetNames) > 1 {
		note = "仅显示第一个工作表：" + sheetNames[0]
	}
	return b.String(), lineCount, hasMore, truncated, note, nil
}

func normalizePreviewFormat(format, filePath string) string {
	normalized := strings.TrimSpace(strings.ToLower(format))
	normalized = strings.TrimPrefix(normalized, ".")
	if normalized == "" {
		normalized = strings.ToLower(strings.TrimPrefix(filepath.Ext(filePath), "."))
	}
	return normalized
}

// looksBinary reports whether the sample looks like binary data (NUL bytes or a
// high ratio of control characters).
func looksBinary(sample []byte) bool {
	if len(sample) > filePreviewBinarySampleSize {
		sample = sample[:filePreviewBinarySampleSize]
	}
	if len(sample) == 0 {
		return false
	}
	if bytes.IndexByte(sample, 0) >= 0 {
		return true
	}

	control := 0
	for _, b := range sample {
		if b < 0x09 || (b > 0x0d && b < 0x20) {
			control++
		}
	}
	return control*100/len(sample) > 5
}

// resolvePath resolves a file path against the configured storage base dir and
// rejects paths that escape it. It does not validate the file format, so it can
// be reused by the preview API for any text/binary format.
func (s *fileContentService) resolvePath(ctx context.Context, filePath string) (string, error) {
	filePath = strings.TrimSpace(filePath)
	if filePath == "" {
		logger.Warnf(ctx, "[FileContent] empty file path")
		return "", fmt.Errorf("file path is required")
	}

	baseDir := strings.TrimSpace(s.baseDir)
	if baseDir == "" {
		logger.Warnf(ctx, "[FileContent] storage base dir is not configured")
		return "", fmt.Errorf("storage base dir is required")
	}

	absBase, err := filepath.Abs(filepath.Clean(baseDir))
	if err != nil {
		logger.Warnf(ctx, "[FileContent] resolve absolute base dir failed: base_dir=%s err=%v", baseDir, err)
		return "", err
	}
	if err := os.MkdirAll(absBase, 0o755); err != nil {
		logger.Warnf(ctx, "[FileContent] create base dir failed: base_dir=%s err=%v", absBase, err)
		return "", err
	}

	candidate := filePath
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(absBase, candidate)
	}

	resolvedPath, err := utils.SafePathUnderBase(absBase, candidate)
	if err != nil {
		logger.Warnf(ctx, "[FileContent] path rejected by base dir guard: base_dir=%s candidate=%s err=%v", absBase, candidate, err)
		return "", err
	}

	return resolvedPath, nil
}
