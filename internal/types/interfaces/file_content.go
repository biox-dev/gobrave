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
	// FilePreviewKindTree means the path is a directory and Tree holds its
	// (bounded) directory listing so the UI can render a file tree.
	FilePreviewKindTree FilePreviewKind = "tree"
)

// FileTreeNode is one entry of a directory preview tree (Kind == tree).
type FileTreeNode struct {
	// Name is the entry name (base name only).
	Name string `json:"name"`
	// Path is the slash path relative to the previewed directory, e.g.
	// "/sub/file.txt". File names reported by the tree are not go_file IDs.
	Path string `json:"path"`
	// IsDir reports whether the node is a directory.
	IsDir bool `json:"is_dir"`
	// Size is set for files only.
	Size *int64 `json:"size,omitempty"`
	// Modified is the entry mtime as a Unix timestamp.
	Modified int64 `json:"modified,omitempty"`
	// Children holds the entries of a directory node. Empty when the node is a
	// file, the directory is empty, or it was not expanded.
	Children []FileTreeNode `json:"children,omitempty"`
	// Truncated marks a directory whose children were not fully listed (depth
	// or entry budget reached).
	Truncated bool `json:"truncated,omitempty"`
}

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
	// Tree is the directory listing when Kind == tree.
	Tree []FileTreeNode `json:"tree,omitempty"`
	// Message is a human readable note (e.g. why a binary cannot be previewed).
	Message string `json:"message,omitempty"`
	// File is the go_file row the preview was read from. It is only populated
	// by the by-file-id API so the frontend can render file metadata.
	File *types.File `json:"file,omitempty"`
}

// FileContentService reads local files for previews and paged content.
// It supports Excel (xlsx), csv/tsv and plain text/binary detection, and
// returns a bounded directory tree when the path is a directory.
type FileContentService interface {
	// ReadFileContent reads a file and returns a UI-renderable payload: raw
	// delimited text for tables, raw text for text files, a message for
	// binaries, and a directory tree for directories. Offset/Limit page through
	// large files (used by the Agent get_file tool); zero values keep the plain
	// preview behavior.
	ReadFileContent(ctx context.Context, in FileContentInput) (*FilePreviewResult, error)
}
