package coder

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/logging"
	"github.com/biox-dev/gobrave/internal/agent/tool"
	"github.com/biox-dev/gobrave/internal/agent/tool/coder/fileutil"
)

const (
	GlobToolName    = "glob"
	globDescription = `Fast file pattern matching tool that finds files by name and pattern, returning matching paths sorted by modification time (newest first).

WHEN TO USE THIS TOOL:
- Use when you need to find files by name patterns or extensions
- Great for finding specific file types across a directory structure
- Useful for discovering files that match certain naming conventions

HOW TO USE:
- Provide a glob pattern to match against file paths
- Optionally specify a starting directory (defaults to current working directory)
- Results are sorted with most recently modified files first

GLOB PATTERN SYNTAX:
- '*' matches any sequence of non-separator characters
- '**' matches any sequence of characters, including separators
- '?' matches any single non-separator character
- '[...]' matches any character in the brackets
- '[!...]' matches any character not in the brackets

COMMON PATTERN EXAMPLES:
- '*.js' - Find all JavaScript files in the current directory
- '**/*.js' - Find all JavaScript files in any subdirectory
- 'src/**/*.{ts,tsx}' - Find all TypeScript files in the src directory
- '*.{html,css,js}' - Find all HTML, CSS, and JS files

LIMITATIONS:
- Results are limited to 100 files (newest first)
- Does not search file contents (use Grep tool for that)
- Hidden files (starting with '.') are skipped

TIPS:
- For the most useful results, combine with the Grep tool: first find files with Glob, then search their contents with Grep
- When doing iterative exploration that may require multiple rounds of searching, consider using the Agent tool instead
- Always check if results are truncated and refine your search pattern if needed`
)

type GlobParams struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path"`
}

type GlobResponseMetadata struct {
	NumberOfFiles int  `json:"number_of_files"`
	Truncated     bool `json:"truncated"`
}

type globTool struct{}

// NewGlob 返回 glob 工具。
func NewGlob() tool.Tool { return &globTool{} }

// Definition 实现 tool.Tool。
func (g *globTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        GlobToolName,
		Description: globDescription,
		InputSchema: tool.Schema("glob 工具入参", map[string]any{
			"pattern": tool.StringProperty("The glob pattern to match files against"),
			"path":    tool.StringProperty("The directory to search in. Defaults to the current working directory."),
		}, "pattern"),
	}
}

// Execute 实现 tool.Tool。
func (g *globTool) Execute(ctx context.Context, args json.RawMessage) (any, error) {
	var params GlobParams
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, toolError(fmt.Sprintf("error parsing parameters: %s", err))
	}
	if params.Pattern == "" {
		return nil, toolError("pattern is required")
	}

	searchPath := params.Path
	if searchPath == "" {
		searchPath = workingDir(ctx)
	}

	files, truncated, err := globFiles(params.Pattern, searchPath, 100)
	if err != nil {
		return nil, fmt.Errorf("error finding files: %w", err)
	}

	if len(files) == 0 {
		return "No files found", nil
	}
	output := strings.Join(files, "\n")
	if truncated {
		output += "\n\n(Results are truncated. Consider using a more specific path or pattern.)"
	}
	return output, nil
}

func globFiles(pattern, searchPath string, limit int) ([]string, bool, error) {
	cmdRg := fileutil.GetRgCmd(pattern)
	if cmdRg != nil {
		cmdRg.Dir = searchPath
		matches, err := runRipgrep(cmdRg, searchPath, limit)
		if err == nil {
			return matches, len(matches) >= limit && limit > 0, nil
		}
		logging.Warn(fmt.Sprintf("Ripgrep execution failed: %v. Falling back to doublestar.", err))
	}

	return fileutil.GlobWithDoublestar(pattern, searchPath, limit)
}

func runRipgrep(cmd *exec.Cmd, searchRoot string, limit int) ([]string, error) {
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
			return nil, nil
		}
		return nil, fmt.Errorf("ripgrep: %w\n%s", err, out)
	}

	var matches []string
	for _, p := range bytes.Split(out, []byte{0}) {
		if len(p) == 0 {
			continue
		}
		absPath := string(p)
		if !filepath.IsAbs(absPath) {
			absPath = filepath.Join(searchRoot, absPath)
		}
		if fileutil.SkipHidden(absPath) {
			continue
		}
		matches = append(matches, absPath)
	}

	sort.SliceStable(matches, func(i, j int) bool {
		return len(matches[i]) < len(matches[j])
	})

	if limit > 0 && len(matches) > limit {
		matches = matches[:limit]
	}
	return matches, nil
}
