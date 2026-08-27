package service

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/biox-dev/gobrave/internal/config"
	"github.com/biox-dev/gobrave/internal/logger"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"github.com/biox-dev/gobrave/internal/utils"
	"github.com/xuri/excelize/v2"
)

var (
	ErrUnsupportedSheetFormat = errors.New("unsupported sheet format")
	ErrInvalidWorkbookData    = errors.New("invalid workbook data")
)

type sheetFileService struct {
	baseDir string
}

type workbookSnapshot struct {
	ID         string                       `json:"id"`
	Name       string                       `json:"name"`
	SheetOrder []string                     `json:"sheetOrder"`
	Sheets     map[string]worksheetSnapshot `json:"sheets"`
}

type worksheetSnapshot struct {
	ID       string                             `json:"id"`
	Name     string                             `json:"name"`
	CellData map[string]map[string]cellSnapshot `json:"cellData"`
}

type cellSnapshot struct {
	V any    `json:"v"`
	F string `json:"f"`
}

func NewSheetFileService(cfg *config.Config) interfaces.SheetFileService {
	baseDir := ""
	if cfg != nil && cfg.Storage != nil {
		configured := strings.TrimSpace(cfg.Storage.BaseDir)
		if configured != "" {
			baseDir = configured
		}
	}

	// resolvedBase, err := utils.ResolveConfiguredPath(baseDir, "sheets")
	// if err != nil {
	// 	resolvedBase = "sheets"
	// }

	return &sheetFileService{baseDir: baseDir}
}

func (s *sheetFileService) ReadWorkbook(ctx context.Context, filePath, format string) (*interfaces.WorkbookReadResult, error) {
	logger.Debugf(ctx, "[SheetFile] read request: file_path=%s format=%s", filePath, format)
	resolvedPath, normalizedFormat, err := s.resolveTargetPath(ctx, filePath, format)
	if err != nil {
		logger.Warnf(ctx, "[SheetFile] read resolve target path failed: file_path=%s format=%s err=%v", filePath, format, err)
		return nil, err
	}

	switch normalizedFormat {
	case "xlsx":
		workbookData, err := s.readExcelWorkbook(resolvedPath)
		if err != nil {
			return nil, err
		}
		return &interfaces.WorkbookReadResult{
			FilePath:     resolvedPath,
			Format:       normalizedFormat,
			WorkbookData: workbookData,
		}, nil
	case "csv":
		workbookData, err := s.readDelimitedWorkbook(resolvedPath, ',')
		if err != nil {
			return nil, err
		}
		return &interfaces.WorkbookReadResult{
			FilePath:     resolvedPath,
			Format:       normalizedFormat,
			WorkbookData: workbookData,
		}, nil
	case "tsv":
		workbookData, err := s.readDelimitedWorkbook(resolvedPath, '\t')
		if err != nil {
			return nil, err
		}
		return &interfaces.WorkbookReadResult{
			FilePath:     resolvedPath,
			Format:       normalizedFormat,
			WorkbookData: workbookData,
		}, nil
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedSheetFormat, normalizedFormat)
	}
}

func (s *sheetFileService) WriteWorkbook(ctx context.Context, filePath, format string, workbookData map[string]any) (*interfaces.WorkbookWriteResult, error) {
	logger.Debugf(ctx, "[SheetFile] write request: file_path=%s format=%s", filePath, format)
	resolvedPath, normalizedFormat, err := s.resolveTargetPath(ctx, filePath, format)

	if err != nil {
		logger.Warnf(ctx, "[SheetFile] write resolve target path failed: file_path=%s format=%s err=%v", filePath, format, err)
		return nil, err
	}

	switch normalizedFormat {
	case "xlsx":
		if err := s.writeExcelWorkbook(resolvedPath, workbookData); err != nil {
			return nil, err
		}
	case "csv":
		if err := s.writeDelimitedWorkbook(resolvedPath, workbookData, ','); err != nil {
			return nil, err
		}
	case "tsv":
		if err := s.writeDelimitedWorkbook(resolvedPath, workbookData, '\t'); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedSheetFormat, normalizedFormat)
	}

	return &interfaces.WorkbookWriteResult{
		FilePath: resolvedPath,
		Format:   normalizedFormat,
	}, nil
}

func (s *sheetFileService) writeDelimitedWorkbook(filePath string, workbookData map[string]any, delimiter rune) error {
	if len(workbookData) == 0 {
		return ErrInvalidWorkbookData
	}

	var snapshot workbookSnapshot
	raw, err := json.Marshal(workbookData)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidWorkbookData, err)
	}
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidWorkbookData, err)
	}
	if len(snapshot.Sheets) == 0 {
		return fmt.Errorf("%w: no sheets", ErrInvalidWorkbookData)
	}

	sheet, err := pickSheetForDelimited(snapshot)
	if err != nil {
		return err
	}

	rows := buildStringRowsFromSheet(sheet)
	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		return err
	}

	file, err := os.Create(filePath)
	if err != nil {
		return err
	}
	defer file.Close()

	writer := csv.NewWriter(file)
	writer.Comma = delimiter
	if err := writer.WriteAll(rows); err != nil {
		return err
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return err
	}

	return nil
}

func (s *sheetFileService) resolveTargetPath(ctx context.Context, filePath, format string) (string, string, error) {
	filePath = strings.TrimSpace(filePath)
	if filePath == "" {
		logger.Warnf(ctx, "[SheetFile] empty file path")
		return "", "", fmt.Errorf("file path is required")
	}

	baseDir := strings.TrimSpace(s.baseDir)
	if baseDir == "" {
		logger.Warnf(ctx, "[SheetFile] storage base dir is not configured")
		return "", "", fmt.Errorf("storage base dir is required")
	}

	absBase, err := filepath.Abs(filepath.Clean(baseDir))
	if err != nil {
		logger.Warnf(ctx, "[SheetFile] resolve absolute base dir failed: base_dir=%s err=%v", baseDir, err)
		return "", "", err
	}
	if err := os.MkdirAll(absBase, 0o755); err != nil {
		logger.Warnf(ctx, "[SheetFile] create base dir failed: base_dir=%s err=%v", absBase, err)
		return "", "", err
	}

	candidate := filePath
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(absBase, candidate)
	}

	resolvedPath, err := utils.SafePathUnderBase(absBase, candidate)
	if err != nil {
		logger.Warnf(ctx, "[SheetFile] path rejected by base dir guard: base_dir=%s candidate=%s err=%v", absBase, candidate, err)
		return "", "", err
	}

	resolvedFormat, err := normalizeSheetFormat(format, resolvedPath)
	if err != nil {
		logger.Warnf(ctx, "[SheetFile] unsupported/invalid format: file_path=%s resolved_path=%s format=%s err=%v", filePath, resolvedPath, format, err)
		return "", "", err
	}

	logger.Debugf(ctx, "[SheetFile] resolved target path: resolved_path=%s format=%s base_dir=%s", resolvedPath, resolvedFormat, absBase)

	return resolvedPath, resolvedFormat, nil
}

func (s *sheetFileService) readDelimitedWorkbook(filePath string, delimiter rune) (map[string]any, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	reader := csv.NewReader(file)
	reader.Comma = delimiter
	reader.FieldsPerRecord = -1
	reader.LazyQuotes = true

	rows, err := reader.ReadAll()
	if err != nil {
		return nil, err
	}

	if len(rows) > 0 && len(rows[0]) > 0 {
		rows[0][0] = strings.TrimPrefix(rows[0][0], "\ufeff")
	}

	return buildSingleSheetWorkbookData(filePath, rows), nil
}

func (s *sheetFileService) readExcelWorkbook(filePath string) (map[string]any, error) {
	f, err := excelize.OpenFile(filePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	sheetNames := f.GetSheetList()
	if len(sheetNames) == 0 {
		sheetNames = []string{"Sheet1"}
	}

	workbookName := strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath))
	if workbookName == "" {
		workbookName = "Workbook"
	}

	workbookID := sanitizeIdentifier(workbookName, "workbook")
	sheets := make(map[string]any, len(sheetNames))
	sheetOrder := make([]string, 0, len(sheetNames))

	for idx, name := range sheetNames {
		sheetID := fmt.Sprintf("sheet-%02d", idx+1)
		sheetOrder = append(sheetOrder, sheetID)

		rows, err := f.GetRows(name)
		if err != nil {
			return nil, err
		}

		cellData := make(map[string]any)
		maxColCount := 0
		for rowIndex, row := range rows {
			if len(row) > maxColCount {
				maxColCount = len(row)
			}

			rowData := make(map[string]any)
			for colIndex, cellValue := range row {
				if strings.TrimSpace(cellValue) == "" {
					continue
				}
				rowData[strconv.Itoa(colIndex)] = map[string]any{"v": cellValue}
			}

			if len(rowData) > 0 {
				cellData[strconv.Itoa(rowIndex)] = rowData
			}
		}

		rowCount := len(rows)
		if rowCount < 200 {
			rowCount = 200
		}
		columnCount := maxColCount
		if columnCount < 26 {
			columnCount = 26
		}

		sheets[sheetID] = map[string]any{
			"id":          sheetID,
			"name":        name,
			"rowCount":    rowCount,
			"columnCount": columnCount,
			"cellData":    cellData,
		}
	}

	return map[string]any{
		"id":         workbookID,
		"name":       workbookName,
		"sheetOrder": sheetOrder,
		"sheets":     sheets,
	}, nil
}

func (s *sheetFileService) writeExcelWorkbook(filePath string, workbookData map[string]any) error {
	if len(workbookData) == 0 {
		return ErrInvalidWorkbookData
	}

	var snapshot workbookSnapshot
	raw, err := json.Marshal(workbookData)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidWorkbookData, err)
	}
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidWorkbookData, err)
	}
	if len(snapshot.Sheets) == 0 {
		return fmt.Errorf("%w: no sheets", ErrInvalidWorkbookData)
	}

	order := snapshot.SheetOrder
	if len(order) == 0 {
		order = make([]string, 0, len(snapshot.Sheets))
		for sheetID := range snapshot.Sheets {
			order = append(order, sheetID)
		}
		sort.Strings(order)
	}

	f := excelize.NewFile()
	createdAnySheet := false

	for _, sheetID := range order {
		sheet, ok := snapshot.Sheets[sheetID]
		if !ok {
			continue
		}

		sheetName := strings.TrimSpace(sheet.Name)
		if sheetName == "" {
			sheetName = sheetID
		}

		if !createdAnySheet {
			f.SetSheetName("Sheet1", sheetName)
			createdAnySheet = true
		} else {
			if _, err := f.NewSheet(sheetName); err != nil {
				return err
			}
		}

		for rowKey, row := range sheet.CellData {
			rowIndex, err := strconv.Atoi(rowKey)
			if err != nil || rowIndex < 0 {
				continue
			}

			for colKey, cell := range row {
				colIndex, err := strconv.Atoi(colKey)
				if err != nil || colIndex < 0 {
					continue
				}

				cellAddress, err := excelize.CoordinatesToCellName(colIndex+1, rowIndex+1)
				if err != nil {
					continue
				}

				if strings.TrimSpace(cell.F) != "" {
					formula := strings.TrimPrefix(strings.TrimSpace(cell.F), "=")
					if formula != "" {
						if err := f.SetCellFormula(sheetName, cellAddress, formula); err != nil {
							return err
						}
					}
					continue
				}

				if cell.V == nil {
					continue
				}

				if err := f.SetCellValue(sheetName, cellAddress, normalizeCellValue(cell.V)); err != nil {
					return err
				}
			}
		}
	}

	if !createdAnySheet {
		return fmt.Errorf("%w: no valid sheet in workbook", ErrInvalidWorkbookData)
	}

	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		return err
	}
	return f.SaveAs(filePath)
}

func normalizeSheetFormat(format, filePath string) (string, error) {
	fmtNormalized := strings.TrimSpace(strings.ToLower(format))
	if fmtNormalized == "" {
		ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(filePath), "."))
		fmtNormalized = ext
	}

	switch fmtNormalized {
	case "xlsx", "csv", "tsv":
		return fmtNormalized, nil
	default:
		return "", fmt.Errorf("%w: %s", ErrUnsupportedSheetFormat, fmtNormalized)
	}
}

func buildSingleSheetWorkbookData(filePath string, rows [][]string) map[string]any {
	workbookName := strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath))
	if workbookName == "" {
		workbookName = "Workbook"
	}

	workbookID := sanitizeIdentifier(workbookName, "workbook")
	sheetID := "sheet-01"

	cellData := make(map[string]any)
	maxColCount := 0
	for rowIndex, row := range rows {
		if len(row) > maxColCount {
			maxColCount = len(row)
		}

		rowData := make(map[string]any)
		for colIndex, cellValue := range row {
			if strings.TrimSpace(cellValue) == "" {
				continue
			}
			rowData[strconv.Itoa(colIndex)] = map[string]any{"v": cellValue}
		}

		if len(rowData) > 0 {
			cellData[strconv.Itoa(rowIndex)] = rowData
		}
	}

	rowCount := len(rows)
	if rowCount < 200 {
		rowCount = 200
	}
	columnCount := maxColCount
	if columnCount < 26 {
		columnCount = 26
	}

	return map[string]any{
		"id":         workbookID,
		"name":       workbookName,
		"sheetOrder": []string{sheetID},
		"sheets": map[string]any{
			sheetID: map[string]any{
				"id":          sheetID,
				"name":        workbookName,
				"rowCount":    rowCount,
				"columnCount": columnCount,
				"cellData":    cellData,
			},
		},
	}
}

func sanitizeIdentifier(name, fallback string) string {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return fallback
	}

	var b strings.Builder
	for _, ch := range trimmed {
		switch {
		case ch >= 'a' && ch <= 'z':
			b.WriteRune(ch)
		case ch >= 'A' && ch <= 'Z':
			b.WriteRune(ch + 32)
		case ch >= '0' && ch <= '9':
			b.WriteRune(ch)
		case ch == '-' || ch == '_':
			b.WriteRune(ch)
		default:
			b.WriteRune('-')
		}
	}

	result := strings.Trim(b.String(), "-_")
	if result == "" {
		return fallback
	}
	return result
}

func normalizeCellValue(v any) any {
	switch val := v.(type) {
	case string, float64, bool, int, int32, int64, uint, uint32, uint64:
		return val
	default:
		return fmt.Sprintf("%v", val)
	}
}

func pickSheetForDelimited(snapshot workbookSnapshot) (worksheetSnapshot, error) {
	if len(snapshot.SheetOrder) > 0 {
		for _, sheetID := range snapshot.SheetOrder {
			sheet, ok := snapshot.Sheets[sheetID]
			if ok {
				return sheet, nil
			}
		}
	}

	keys := make([]string, 0, len(snapshot.Sheets))
	for sheetID := range snapshot.Sheets {
		keys = append(keys, sheetID)
	}
	if len(keys) == 0 {
		return worksheetSnapshot{}, fmt.Errorf("%w: no sheets", ErrInvalidWorkbookData)
	}
	sort.Strings(keys)
	return snapshot.Sheets[keys[0]], nil
}

func buildStringRowsFromSheet(sheet worksheetSnapshot) [][]string {
	maxRow := -1
	maxCol := -1

	for rowKey, row := range sheet.CellData {
		rowIndex, err := strconv.Atoi(rowKey)
		if err != nil || rowIndex < 0 {
			continue
		}
		if rowIndex > maxRow {
			maxRow = rowIndex
		}

		for colKey := range row {
			colIndex, err := strconv.Atoi(colKey)
			if err != nil || colIndex < 0 {
				continue
			}
			if colIndex > maxCol {
				maxCol = colIndex
			}
		}
	}

	if maxRow < 0 || maxCol < 0 {
		return [][]string{}
	}

	rows := make([][]string, maxRow+1)
	for r := 0; r <= maxRow; r++ {
		rows[r] = make([]string, maxCol+1)
		rowData, ok := sheet.CellData[strconv.Itoa(r)]
		if !ok {
			continue
		}

		for c := 0; c <= maxCol; c++ {
			cell, ok := rowData[strconv.Itoa(c)]
			if !ok {
				continue
			}
			rows[r][c] = cellToDelimitedString(cell)
		}
	}

	return rows
}

func cellToDelimitedString(cell cellSnapshot) string {
	if strings.TrimSpace(cell.F) != "" {
		formula := strings.TrimSpace(cell.F)
		if !strings.HasPrefix(formula, "=") {
			formula = "=" + formula
		}
		return formula
	}
	if cell.V == nil {
		return ""
	}
	return fmt.Sprintf("%v", cell.V)
}
