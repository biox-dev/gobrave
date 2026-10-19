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
	"github.com/biox-dev/gobrave/internal/agent/tool/coder/diff"
)

type PatchParams struct {
	PatchText string `json:"patch_text"`
}

type PatchResponseMetadata struct {
	FilesChanged []string `json:"files_changed"`
	Additions    int      `json:"additions"`
	Removals     int      `json:"removals"`
}

type patchTool struct {
	lspClients map[string]*lsp.Client
	files      history.Service
}

const (
	PatchToolName    = "patch"
	patchDescription = `Applies a patch to multiple files in one operation. This tool is useful for making coordinated changes across multiple files.

The patch text must follow this format:
*** Begin Patch
*** Update File: /path/to/file
@@ Context line (unique within the file)
 Line to keep
-Line to remove
+Line to add
 Line to keep
*** Add File: /path/to/new/file
+Content of the new file
+More content
*** Delete File: /path/to/file/to/delete
*** End Patch

Before using this tool:
1. Use the FileRead tool to understand the files' contents and context
2. Verify all file paths are correct (use the LS tool)

CRITICAL REQUIREMENTS FOR USING THIS TOOL:

1. UNIQUENESS: Context lines MUST uniquely identify the specific sections you want to change
2. PRECISION: All whitespace, indentation, and surrounding code must match exactly
3. VALIDATION: Ensure edits result in idiomatic, correct code
4. PATHS: Always use absolute file paths (starting with /)

The tool will apply all changes in a single atomic operation.`
)

// NewPatch 返回 patch 工具。
func NewPatch() tool.Tool { return &patchTool{} }

// Definition 实现 tool.Tool。
func (p *patchTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        PatchToolName,
		Description: patchDescription,
		InputSchema: tool.Schema("patch 工具入参", map[string]any{
			"patch_text": tool.StringProperty("The full patch text that describes all changes to be made"),
		}, "patch_text"),
		// patch 涉及多个文件，无法从单个入参路径推导授权范围；
		// 声明为 write 动作，让上层按工具名统一走权限门禁。
		Permission: &tool.Permission{Action: "write"},
	}
}

// Execute 实现 tool.Tool。
func (p *patchTool) Execute(ctx context.Context, args json.RawMessage) (any, error) {
	var params PatchParams
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, toolError("invalid parameters")
	}
	if params.PatchText == "" {
		return nil, toolError("patch_text is required")
	}

	p.lspClients = lspClients(ctx)
	p.files = fileHistory(ctx)
	wd := workingDir(ctx)
	absPath := func(path string) string {
		if filepath.IsAbs(path) {
			return path
		}
		return filepath.Join(wd, path)
	}

	// Identify all files needed for the patch and verify they've been read
	filesToRead := diff.IdentifyFilesNeeded(params.PatchText)
	for _, filePath := range filesToRead {
		path := absPath(filePath)
		if getLastReadTime(path).IsZero() {
			return nil, toolError(fmt.Sprintf("you must read the file %s before patching it. Use the View tool first", filePath))
		}
		fileInfo, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				return nil, toolError(fmt.Sprintf("file not found: %s", path))
			}
			return nil, fmt.Errorf("failed to access file: %w", err)
		}
		if fileInfo.IsDir() {
			return nil, toolError(fmt.Sprintf("path is a directory, not a file: %s", path))
		}
		modTime := fileInfo.ModTime()
		lastRead := getLastReadTime(path)
		if modTime.After(lastRead) {
			return nil, toolError(fmt.Sprintf("file %s has been modified since it was last read (mod time: %s, last read: %s)",
				path, modTime.Format(time.RFC3339), lastRead.Format(time.RFC3339)))
		}
	}

	// Check for new files to ensure they don't already exist
	for _, filePath := range diff.IdentifyFilesAdded(params.PatchText) {
		path := absPath(filePath)
		if _, err := os.Stat(path); err == nil {
			return nil, toolError(fmt.Sprintf("file already exists and cannot be added: %s", path))
		} else if !os.IsNotExist(err) {
			return nil, fmt.Errorf("failed to check file: %w", err)
		}
	}

	// Load all required files
	currentFiles := make(map[string]string)
	for _, filePath := range filesToRead {
		content, err := os.ReadFile(absPath(filePath))
		if err != nil {
			return nil, fmt.Errorf("failed to read file %s: %w", absPath(filePath), err)
		}
		currentFiles[filePath] = string(content)
	}

	patch, fuzz, err := diff.TextToPatch(params.PatchText, currentFiles)
	if err != nil {
		return nil, toolError(fmt.Sprintf("failed to parse patch: %s", err))
	}
	if fuzz > 3 {
		return nil, toolError(fmt.Sprintf("patch contains fuzzy matches (fuzz level: %d). Please make your context lines more precise", fuzz))
	}

	commit, err := diff.PatchToCommit(patch, currentFiles)
	if err != nil {
		return nil, toolError(fmt.Sprintf("failed to create commit from patch: %s", err))
	}

	sid := sessionID(ctx)

	// Apply the changes to the filesystem
	err = diff.ApplyCommit(commit, func(path string, content string) error {
		target := absPath(path)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("failed to create parent directories for %s: %w", target, err)
		}
		return os.WriteFile(target, []byte(content), 0o644)
	}, func(path string) error {
		return os.Remove(absPath(path))
	})
	if err != nil {
		return nil, toolError(fmt.Sprintf("failed to apply patch: %s", err))
	}

	// Update file history for all modified files
	changedFiles := []string{}
	totalAdditions := 0
	totalRemovals := 0

	for path, change := range commit.Changes {
		target := absPath(path)
		changedFiles = append(changedFiles, target)

		oldContent := ""
		if change.OldContent != nil {
			oldContent = *change.OldContent
		}
		newContent := ""
		if change.NewContent != nil {
			newContent = *change.NewContent
		}

		_, additions, removals := diff.GenerateDiff(oldContent, newContent, path)
		totalAdditions += additions
		totalRemovals += removals

		if p.files != nil {
			file, histErr := p.files.GetByPathAndSession(ctx, target, sid)
			if histErr != nil && change.Type != diff.ActionAdd {
				if _, err := p.files.Create(ctx, sid, target, oldContent); err != nil {
					logging.Debug("Error creating file history", "error", err)
				}
			}
			if histErr == nil && file != nil && change.Type != diff.ActionAdd && file.Content != oldContent {
				if _, err := p.files.CreateVersion(ctx, sid, target, oldContent); err != nil {
					logging.Debug("Error creating file history version", "error", err)
				}
			}
			stored := newContent
			if change.Type == diff.ActionDelete {
				stored = ""
			}
			if _, err := p.files.CreateVersion(ctx, sid, target, stored); err != nil {
				logging.Debug("Error creating file history version", "error", err)
			}
		}

		recordFileWrite(target)
		recordFileRead(target)
	}

	for _, filePath := range changedFiles {
		waitForLspDiagnostics(ctx, filePath, p.lspClients)
	}

	result := fmt.Sprintf("Patch applied successfully. %d files changed, %d additions, %d removals",
		len(changedFiles), totalAdditions, totalRemovals)

	diagnosticsText := ""
	for _, filePath := range changedFiles {
		diagnosticsText += getDiagnostics(filePath, p.lspClients)
	}
	if diagnosticsText != "" {
		result += "\n\nDiagnostics:\n" + diagnosticsText
	}
	return result, nil
}
