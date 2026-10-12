package interfaces

import (
	"context"

	"github.com/biox-dev/gobrave/internal/types"
)

type WorkbookReadResult struct {
	FilePath     string         `json:"file_path"`
	Format       string         `json:"format"`
	WorkbookData map[string]any `json:"workbook_data"`
	// File is the go_file row the workbook was read from. It is only populated
	// by ReadWorkbookByFileID so the frontend can render the file metadata
	// without an extra request.
	File *types.File `json:"file,omitempty"`
}

type WorkbookWriteResult struct {
	FilePath string `json:"file_path"`
	Format   string `json:"format"`
}

// SheetFileService defines local sheet file read/write capabilities.
// Current implementation supports Excel (xlsx), while csv/tsv are reserved for future extension.
type SheetFileService interface {
	ReadWorkbook(ctx context.Context, filePath, format string) (*WorkbookReadResult, error)
	WriteWorkbook(ctx context.Context, filePath, format string, workbookData map[string]any) (*WorkbookWriteResult, error)
}
