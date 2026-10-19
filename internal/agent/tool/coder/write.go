package coder

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/biox-dev/gobrave/internal/agent/history"
	"github.com/biox-dev/gobrave/internal/agent/logging"
	"github.com/biox-dev/gobrave/internal/agent/lsp"
	"github.com/biox-dev/gobrave/internal/agent/tool"
)

type WriteParams struct {
	FilePath string `json:"file_path"`
	Content  string `json:"content"`
}

type WritePermissionsParams struct {
	FilePath string `json:"file_path"`
	Diff     string `json:"diff"`
}

type writeTool struct {
	lspClients map[string]*lsp.Client
	files      history.Service
}

type WriteResponseMetadata struct {
	Diff      string `json:"diff"`
	Additions int    `json:"additions"`
	Removals  int    `json:"removals"`
}

const (
	WriteToolName    = "write"
	writeDescription = `File writing tool that creates or updates files in the filesystem, allowing you to save or modify text content.

WHEN TO USE THIS TOOL:
- Use when you need to create a new file
- Helpful for updating existing files with modified content
- Perfect for saving generated code, configurations, or text data

HOW TO USE:
- Provide the path to the file you want to write
- Include the content to be written to the file
- The tool will create any necessary parent directories

FEATURES:
- Can create new files or overwrite existing ones
- Creates parent directories automatically if they don't exist
- Checks if the file has been modified since last read for safety
- Avoids unnecessary writes when content hasn't changed

LIMITATIONS:
- You should read a file before writing to it to avoid conflicts
- Cannot append to files (rewrites the entire file)


TIPS:
- Use the View tool first to examine existing files before modifying them
- Use the LS tool to verify the correct location when creating new files
- Combine with Glob and Grep tools to find and modify multiple files
- Always include descriptive comments when making changes to existing code`
)

// NewWrite 返回 write 工具。
func NewWrite() tool.Tool { return &writeTool{} }

// Definition 实现 tool.Tool。
func (w *writeTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        WriteToolName,
		Description: writeDescription,
		InputSchema: tool.Schema("write 工具入参", map[string]any{
			"file_path": tool.StringProperty("The path to the file to write"),
			"content":   tool.StringProperty("The content to write to the file"),
		}, "file_path", "content"),
		Permission: &tool.Permission{Action: "write", PathFrom: "file_path"},
	}
}

// Execute 实现 tool.Tool。
func (w *writeTool) Execute(ctx context.Context, args json.RawMessage) (any, error) {
	params := WriteParams{}
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, toolError(fmt.Sprintf("error parsing parameters: %s", err))
	}
	if params.FilePath == "" {
		return nil, toolError("file_path is required")
	}
	if params.Content == "" {
		return nil, toolError("content is required")
	}

	filePath := params.FilePath
	if !filepath.IsAbs(filePath) {
		filePath = filepath.Join(workingDir(ctx), filePath)
	}

	fileInfo, err := os.Stat(filePath)
	if err == nil {
		if fileInfo.IsDir() {
			return nil, toolError(fmt.Sprintf("Path is a directory, not a file: %s", filePath))
		}

		modTime := fileInfo.ModTime()
		lastRead := getLastReadTime(filePath)
		if modTime.After(lastRead) {
			return nil, toolError(fmt.Sprintf("File %s has been modified since it was last read.\nLast modification: %s\nLast read: %s\n\nPlease read the file again before modifying it.",
				filePath, modTime.Format(time.RFC3339), lastRead.Format(time.RFC3339)))
		}

		oldContent, readErr := os.ReadFile(filePath)
		if readErr == nil && string(oldContent) == params.Content {
			return nil, toolError(fmt.Sprintf("File %s already contains the exact content. No changes made.", filePath))
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("error checking file: %w", err)
	}

	if err = os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		return nil, fmt.Errorf("error creating directory: %w", err)
	}

	oldContent := ""
	if fileInfo != nil && !fileInfo.IsDir() {
		oldBytes, readErr := os.ReadFile(filePath)
		if readErr == nil {
			oldContent = string(oldBytes)
		}
	}

	sid := sessionID(ctx)

	if err = os.WriteFile(filePath, []byte(params.Content), 0o644); err != nil {
		return nil, fmt.Errorf("error writing file: %w", err)
	}

	w.files = fileHistory(ctx)
	w.lspClients = lspClients(ctx)
	if w.files != nil {
		file, histErr := w.files.GetByPathAndSession(ctx, filePath, sid)
		if histErr != nil {
			_, histErr = w.files.Create(ctx, sid, filePath, oldContent)
			if histErr != nil {
				return nil, fmt.Errorf("error creating file history: %w", histErr)
			}
		}
		if file != nil && file.Content != oldContent {
			// 用户手动改过内容：先补一条中间版本
			if _, err := w.files.CreateVersion(ctx, sid, filePath, oldContent); err != nil {
				logging.Debug("Error creating file history version", "error", err)
			}
		}
		if _, err := w.files.CreateVersion(ctx, sid, filePath, params.Content); err != nil {
			logging.Debug("Error creating file history version", "error", err)
		}
	}

	recordFileWrite(filePath)
	recordFileRead(filePath)
	waitForLspDiagnostics(ctx, filePath, w.lspClients)

	result := fmt.Sprintf("<result>\nFile successfully written: %s\n</result>", filePath)
	result += getDiagnostics(filePath, w.lspClients)
	return result, nil
}
