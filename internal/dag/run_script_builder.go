package dag

import (
	"fmt"
	"path/filepath"

	"github.com/flosch/pongo2/v6"
	"github.com/gobravedev/gobrave/internal/types"
)

type RunScriptBuilder interface {
	Build(node *types.AnalysisNode, scriptPath string, scriptContent string, params map[string]any) (string, error)
}

func BuildRunScript(
	node *types.AnalysisNode,
	scriptType string,
	scriptPath string,
	scriptContent string,
	params map[string]any,
) (string, error) {
	builders := NewRunScriptBuilders()
	builder := builders[normalizeScriptType(scriptType)]
	if builder == nil {
		builder = builders["shell"]
	}
	return builder.Build(node, scriptPath, scriptContent, params)
}

func NewRunScriptBuilders() map[string]RunScriptBuilder {
	return map[string]RunScriptBuilder{
		"r":      RScriptBuilder{},
		"python": PythonScriptBuilder{},
		"shell":  ShellScriptBuilder{},
		"qmd":    QmdScriptBuilder{},
	}
}

type RScriptBuilder struct{}

func (RScriptBuilder) Build(node *types.AnalysisNode, scriptPath string, _ string, _ map[string]any) (string, error) {
	return fmt.Sprintf("#!/usr/bin/env bash\nset -euo pipefail\nRscript %q %q %q\n", scriptPath, node.ParamsPath, node.OutputDir), nil
}

type QmdScriptBuilder struct{}

func (QmdScriptBuilder) Build(node *types.AnalysisNode, scriptPath string, _ string, _ map[string]any) (string, error) {
	// quarto preview chapter_5.qmd --to md --no-watch-inputs --no-browse
	outputFileName := "output.md"
	outputFile := filepath.Join(node.OutputDir, outputFileName)
	return fmt.Sprintf(`#!/usr/bin/env bash
set -euo pipefail
export HOME=$PWD/.home
export XDG_CACHE_HOME=$HOME/.cache
export TMPDIR=$PWD/.tmp
mkdir -p "$TMPDIR"

quarto render main.qmd --to md --output-dir %q --execute-dir %q --output - > output.md
mv output.md %q
`, node.OutputDir, node.WorkspaceDir, outputFile), nil

}

type PythonScriptBuilder struct{}

func (PythonScriptBuilder) Build(node *types.AnalysisNode, scriptPath string, _ string, _ map[string]any) (string, error) {
	return fmt.Sprintf("#!/usr/bin/env bash\nset -euo pipefail\npython %q %q %q\n", scriptPath, node.ParamsPath, node.OutputDir), nil
}

type ShellScriptBuilder struct{}

func (ShellScriptBuilder) Build(_ *types.AnalysisNode, scriptPath string, scriptContent string, params map[string]any) (string, error) {
	rendered, err := renderShellTemplate(scriptContent, params)
	if err != nil {
		return "", err
	}
	return rendered + "\n\n#" + scriptPath + "\n", nil
}

func renderShellTemplate(content string, params map[string]any) (string, error) {
	tpl, err := pongo2.FromString(content)
	if err != nil {
		return "", fmt.Errorf("parse shell template failed: %w", err)
	}

	ctx := pongo2.Context{}
	for k, v := range params {
		ctx[k] = v
	}

	if meta, ok := templateAsMap(ctx["meta"]); ok {
		if _, exists := ctx["meta_file_name"]; !exists {
			if fileName, ok := meta["file_name"]; ok {
				ctx["meta_file_name"] = fileName
			}
		}
	}

	rendered, err := tpl.Execute(ctx)
	if err != nil {
		return "", fmt.Errorf("render shell template failed: %w", err)
	}
	return rendered, nil
}

func templateAsMap(v any) (map[string]any, bool) {
	if v == nil {
		return nil, false
	}
	m, ok := v.(map[string]any)
	return m, ok
}
