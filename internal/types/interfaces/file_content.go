package interfaces

import (
	"context"

	"github.com/biox-dev/gobrave/internal/types"
)

// FileContentInput is the input of FileContentService.ReadFileContent.
//
// Offset/Limit use line (text) or row (csv/tsv/xlsx) units so both the preview
// API and the Agent get_file tool read the same way. Zero values keep the old
// preview behavior: Offset 0 and Limit/MaxBytes defaulted by the service.
type FileContentInput struct {
	FilePath string
	// Format optionally overrides the format inferred from FilePath.
	Format string
	// Offset is the 0-based start line/row.
	Offset int
	// Limit is the max lines/rows to return. <=0 uses the service default.
	Limit int
	// MaxBytes caps Content size. <=0 uses the service default.
	MaxBytes int
}

// FilePreviewKind tells the frontend how to render a preview payload.
type FilePreviewKind string

const (
	// FilePreviewKindTable means Content is delimited text that the UI parses
	// into a table (csv/tsv, and xlsx converted to TSV by the backend).
	FilePreviewKindTable FilePreviewKind = "table"
	// FilePreviewKindText means Content is raw text (fasta/gff/md/json/...).
	FilePreviewKindText FilePreviewKind = "text"
	// FilePreviewKindBinary means the file cannot be previewed inline (gz/rds/...).
	FilePreviewKindBinary FilePreviewKind = "binary"
)

// FilePreviewResult is the payload of the file preview API. The backend does not
// build a spreadsheet model: it returns raw delimited text for tables so the UI
// can parse and render it, raw text for text files, and a message for binaries.
type FilePreviewResult struct {
	FilePath string          `json:"file_path"`
	Format   string          `json:"format"`
	Kind     FilePreviewKind `json:"kind"`
	// Delimiter is the column separator when Kind == table.
	Delimiter string `json:"delimiter,omitempty"`
	// Content is the preview payload. Empty for binary files.
	Content string `json:"content,omitempty"`
	// Offset / LineCount / NextOffset describe the requested line/row window:
	// Content covers [offset, offset+line_count). NextOffset is set when there
	// is more data to read.
	Offset     int  `json:"offset,omitempty"`
	LineCount  int  `json:"line_count,omitempty"`
	NextOffset int  `json:"next_offset,omitempty"`
	HasMore    bool `json:"has_more,omitempty"`
	// Truncated reports that Content was cut (line length, row or byte limit).
	Truncated bool `json:"truncated"`
	// Message is a human readable note (e.g. why a binary cannot be previewed).
	Message string `json:"message,omitempty"`
	// File is the go_file row the preview was read from. It is only populated
	// by the by-file-id API so the frontend can render file metadata.
	File *types.File `json:"file,omitempty"`
}

// FileContentService reads local files for previews and paged content.
// It supports Excel (xlsx), csv/tsv and plain text/binary detection.
type FileContentService interface {
	// ReadFileContent reads a file and returns a UI-renderable payload: raw
	// delimited text for tables, raw text for text files, and a message for
	// binaries. Offset/Limit page through large files (used by the Agent
	// get_file tool); zero values keep the plain preview behavior.
	ReadFileContent(ctx context.Context, in FileContentInput) (*FilePreviewResult, error)
}
