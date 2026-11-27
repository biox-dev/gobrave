package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
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
	// fileTreeMaxDepth caps how many directory levels a directory preview
	// expands (1 means only the immediate children of the previewed dir).
	fileTreeMaxDepth = 3
	// fileTreeMaxChildren caps how many entries are listed per directory.
	fileTreeMaxChildren = 500
	// fileTreeMaxNodes caps the total number of nodes in one directory preview.
	fileTreeMaxNodes = 2000
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
		return s.readDirTreeResult(ctx, resolvedPath, normalizedFormat), nil
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

// readDirTreeResult builds a directory preview payload: Tree for the UI to
// render a file tree, plus an indented text listing in Content so text-only
// consumers (the Agent get_file tool) can still read the structure.
func (s *fileContentService) readDirTreeResult(ctx context.Context, dirPath, normalizedFormat string) *interfaces.FilePreviewResult {
	builder := newFileTreeBuilder()
	nodes := builder.build(ctx, dirPath, "", 1)

	format := normalizedFormat
	if format == "" {
		format = "dir"
	}

	result := &interfaces.FilePreviewResult{
		FilePath:  dirPath,
		Format:    format,
		Kind:      interfaces.FilePreviewKindTree,
		Tree:      nodes,
		Content:   renderFileTreeText(nodes),
		Truncated: builder.truncated,
	}
	switch {
	case builder.count == 0:
		result.Message = "目录为空"
	case builder.truncated:
		result.Message = fmt.Sprintf(
			"目录共 %d 个条目（最多展开 %d 层 / %d 个条目），未完全展开。",
			builder.count, fileTreeMaxDepth, fileTreeMaxNodes,
		)
	default:
		result.Message = fmt.Sprintf("目录共 %d 个条目。", builder.count)
	}
	return result
}

// fileTreeBuilder accumulates a bounded directory tree. The budget is shared by
// the whole tree so one huge directory cannot blow the preview payload.
type fileTreeBuilder struct {
	remaining int
	count     int
	truncated bool
}

func newFileTreeBuilder() *fileTreeBuilder {
	return &fileTreeBuilder{remaining: fileTreeMaxNodes}
}

// build lists absDir and, within the depth budget, recurses into subdirectories.
// depth is 1 for the immediate children of the previewed root; relDir is the
// slash path of absDir relative to that root ("" for the root itself).
func (b *fileTreeBuilder) build(ctx context.Context, absDir, relDir string, depth int) []interfaces.FileTreeNode {
	entries, err := os.ReadDir(absDir)
	if err != nil {
		logger.Warnf(ctx, "[FileContent] read dir for preview failed: dir=%s err=%v", absDir, err)
		b.truncated = true
		return nil
	}

	// Directories first, then case-insensitive name order.
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].IsDir() != entries[j].IsDir() {
			return entries[i].IsDir()
		}
		return strings.ToLower(entries[i].Name()) < strings.ToLower(entries[j].Name())
	})

	nodes := make([]interfaces.FileTreeNode, 0, len(entries))
	for _, entry := range entries {
		if b.remaining <= 0 || len(nodes) >= fileTreeMaxChildren {
			b.truncated = true
			break
		}

		info, statErr := entry.Info()
		if statErr != nil {
			continue
		}
		b.remaining--
		b.count++

		relPath := path.Join(relDir, entry.Name())
		node := interfaces.FileTreeNode{
			Name:     entry.Name(),
			Path:     "/" + strings.TrimPrefix(relPath, "/"),
			IsDir:    entry.IsDir(),
			Modified: info.ModTime().Unix(),
		}
		if entry.IsDir() {
			if depth < fileTreeMaxDepth {
				node.Children = b.build(ctx, filepath.Join(absDir, entry.Name()), relPath, depth+1)
			} else {
				node.Truncated = true
			}
		} else {
			size := info.Size()
			node.Size = &size
		}
		nodes = append(nodes, node)
	}
	return nodes
}

// renderFileTreeText renders the tree as an indented listing for text consumers.
func renderFileTreeText(nodes []interfaces.FileTreeNode) string {
	var b strings.Builder
	var walk func(nodes []interfaces.FileTreeNode, prefix string)
	walk = func(nodes []interfaces.FileTreeNode, prefix string) {
		for _, node := range nodes {
			b.WriteString(prefix)
			b.WriteString(node.Name)
			if node.IsDir {
				b.WriteByte('/')
			}
			if node.Truncated {
				b.WriteString(" …")
			}
			b.WriteByte('\n')
			walk(node.Children, prefix+"  ")
		}
	}
	walk(nodes, "")
	return b.String()
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
//
// When offset is inside the file, at least one line is always returned: a line
// wider than the whole byte budget is truncated instead of dropping the page,
// otherwise NextOffset would equal Offset and paging would never advance.
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
		budgetCut := b.Len()+cost > maxBytes
		if budgetCut {
			if lineCount > 0 {
				hasMore = true
				truncated = true
				break
			}
			// Single line exceeds the whole budget: keep a truncated copy so
			// the page is never empty and NextOffset still advances.
			line = truncateForBudget(line, maxBytes-b.Len())
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(line)
		lineCount++
		if budgetCut {
			hasMore = true
			truncated = true
			break
		}

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
//
// When offset is inside the file, at least one record is always returned: a
// record wider than the whole byte budget is truncated so NextOffset advances.
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
		budgetCut := b.Len()+cost > maxBytes
		if budgetCut {
			if lineCount > 0 {
				hasMore = true
				truncated = true
				break
			}
			// A single record exceeds the whole budget: truncate it instead of
			// returning an empty page (which would stall offset-based paging).
			record = truncateRecordForBudget(record, maxBytes-b.Len())
		}
		if err := writer.Write(record); err != nil {
			return nil, err
		}
		lineCount++
		if budgetCut {
			hasMore = true
			truncated = true
			break
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return nil, err
	}

	return newTableContentResult(filePath, format, string(delimiter), b.String(), offset, lineCount, hasMore, truncated, ""), nil
}

// readExcelContent converts the first worksheet to TSV rows
// [offset, offset+limit). A limit <= 0 uses filePreviewDefaultRows.
//
// When offset is inside the sheet, at least one row is always returned: a row
// wider than the whole byte budget is truncated so NextOffset advances.
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
		budgetCut := b.Len()+cost > maxBytes
		if budgetCut {
			if lineCount > 0 {
				hasMore = true
				truncated = true
				break
			}
			// A single row exceeds the whole budget: truncate it instead of
			// returning an empty page (which would stall offset-based paging).
			row = truncateRecordForBudget(row, maxBytes-b.Len())
		}
		if err := writer.Write(row); err != nil {
			return "", 0, false, false, "", err
		}
		lineCount++
		rowIndex++
		if budgetCut {
			hasMore = true
			truncated = true
			break
		}
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

// truncateForBudget shortens s so it occupies at most budget bytes, keeping the
// result valid UTF-8. A budget <= 0 yields an empty string; callers still count
// the line so pagination makes progress.
func truncateForBudget(s string, budget int) string {
	if budget >= len(s) {
		return s
	}
	if budget <= 0 {
		return ""
	}
	return strings.ToValidUTF8(s[:budget], "")
}

// truncateRecordForBudget returns a copy of record whose serialized width fits
// roughly within budget bytes, cutting fields as needed. At least one field is
// always returned so the caller emits a non-empty row and NextOffset advances.
func truncateRecordForBudget(record []string, budget int) []string {
	out := make([]string, 0, len(record))
	remaining := budget
	for i, field := range record {
		if i > 0 {
			remaining-- // delimiter between fields
		}
		if remaining <= 0 {
			break
		}
		if len(field) > remaining {
			out = append(out, truncateForBudget(field, remaining))
			break
		}
		out = append(out, field)
		remaining -= len(field)
	}
	if len(out) == 0 {
		out = append(out, "")
	}
	return out
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
