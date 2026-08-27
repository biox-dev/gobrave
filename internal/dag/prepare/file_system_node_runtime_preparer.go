package prepare

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/biox-dev/gobrave/internal/logger"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"github.com/biox-dev/gobrave/internal/utils"
)

type FileSystemNodeRuntimePreparer struct {
	analysisRepo    interfaces.AnalysisRepository
	workflowService interfaces.WorkflowService
	workflowRepo    interfaces.WorkflowRepository
	projectRepo     interfaces.ProjectRepository
	storageBase     string
	builders        map[string]RunScriptBuilder
}

// func NewFileSystemNodeRuntimePreparer(
// 	analysisRepo interfaces.AnalysisRepository,
// 	workflowRepo interfaces.WorkflowRepository,
// 	projectRepo interfaces.ProjectRepository,
// 	workflowService interfaces.WorkflowService,
// 	storageBase string,
// ) *FileSystemNodeRuntimePreparer {
// 	return NewFileSystemNodeRuntimePreparerWithBuilders(
// 		analysisRepo,
// 		workflowRepo,
// 		projectRepo,
// 		storageBase,
// 		nil,
// 	)
// }

func NewFileSystemNodeRuntimePreparerWithBuilders(
	analysisRepo interfaces.AnalysisRepository,
	workflowRepo interfaces.WorkflowRepository,
	projectRepo interfaces.ProjectRepository,
	workflowService interfaces.WorkflowService,
	storageBase string,
	builders map[string]RunScriptBuilder,
) *FileSystemNodeRuntimePreparer {
	return &FileSystemNodeRuntimePreparer{
		analysisRepo:    analysisRepo,
		workflowService: workflowService,
		workflowRepo:    workflowRepo,
		projectRepo:     projectRepo,
		storageBase:     strings.TrimSpace(storageBase),
		builders:        cloneRunScriptBuilders(builders),
	}
}

func (p *FileSystemNodeRuntimePreparer) Prepare(ctx context.Context, node *types.AnalysisNode) error {
	if node == nil {
		return fmt.Errorf("analysis node is nil")
	}
	script, err := p.workflowRepo.GetScriptByID(ctx, node.ScriptID)
	if err != nil {
		return fmt.Errorf("load script failed: %w", err)
	}

	// scriptPath := p.resolveScriptPath(script.ScriptID, scriptType)
	project, err := p.projectRepo.GetProjectByID(ctx, script.ProjectID)
	if err != nil {
		return fmt.Errorf("load project failed: %w", err)
	}
	if err := os.MkdirAll(node.OutputDir, 0o755); err != nil {
		return err
	}
	// create cached dir
	nodeCachedDir := filepath.Join(node.WorkspaceDir, "cached")
	if err := os.MkdirAll(nodeCachedDir, 0o755); err != nil {
		return err
	}
	prefix := filepath.Join(p.storageBase, "data", project.ProjectID)

	projectCachedDir := filepath.Join(prefix, "cached")
	if err := os.MkdirAll(projectCachedDir, 0o755); err != nil {
		return err
	}

	if node.AnalysisID == 0 {
		// p.initializeStandaloneNodeArtifacts(ctx, node)

		projectDir := utils.GetProjectDir(p.storageBase, project.ProjectID)

		paramsPayload := cloneAnyMapForNode(map[string]interface{}(node.Params))
		paramsPayload["output_dir"] = node.OutputDir
		paramsPayload["project_dir"] = projectDir
		paramsBytes, err := json.MarshalIndent(paramsPayload, "", "  ")
		if err != nil {
			return err
		}
		paramsBytes = append(paramsBytes, '\n')
		if err := os.WriteFile(node.ParamsPath, paramsBytes, 0o644); err != nil {
			return err
		}

		scriptDir, scriptMainFile, err := p.workflowService.GetScriptFileByScriptID(ctx, script.ID)
		if err != nil {
			return err
		}
		scriptPath := filepath.Join(scriptDir, scriptMainFile)

		if !filepath.IsAbs(scriptPath) {
			scriptPath = filepath.Join(p.storageBase, scriptPath)
		}

		// scriptContent, err := os.ReadFile(scriptPath)
		// if err != nil {
		// 	return err
		// }

		scriptWorkspaceDir := filepath.Join(node.WorkspaceDir, scriptMainFile)
		if _, err := os.Lstat(scriptWorkspaceDir); err != nil {
			if os.IsNotExist(err) {
				if err := os.Symlink(scriptPath, scriptWorkspaceDir); err != nil {
					return err
				}
			} else {
				return err
			}
		}
		// synlink io_schema.json
		ioSchemaPath := filepath.Join(scriptDir, "io_schema.json")
		scriptWorkspaceIoSchemaPath := filepath.Join(node.WorkspaceDir, "io_schema.json")
		if _, err := os.Lstat(ioSchemaPath); err == nil {
			if _, err := os.Lstat(scriptWorkspaceIoSchemaPath); err != nil {

				if os.IsNotExist(err) {
					if err := os.Symlink(ioSchemaPath, scriptWorkspaceIoSchemaPath); err != nil {
						return err
					}
				}
			}
		}

		// runScript, err := BuildRunScript(node, script.ScriptType, scriptPath, string(scriptContent), paramsPayload)
		// if err != nil {
		// 	return err
		// }
		// if err := os.WriteFile(node.CommandPath, []byte(runScript), 0o755); err != nil {
		// 	return err
		// }
		if err := p.WriteCommand(node, script.ScriptType, scriptPath, paramsPayload); err != nil {
			return fmt.Errorf("write command failed: %w", err)
		}

		// if _, err := os.Stat(node.LogPath); err != nil {
		// 	if os.IsNotExist(err) {
		// 		if err := os.WriteFile(node.LogPath, []byte(""), 0o644); err != nil {
		// 			return err
		// 		}
		// 	} else {
		// 		return err
		// 	}
		// }

	} else {
		if strings.TrimSpace(node.AnalysisNodeID) == "" {
			return fmt.Errorf("analysis_node_id is required")
		}
		if node.ScriptID == 0 {
			return fmt.Errorf("script_id is required")
		}

		analysis, err := p.analysisRepo.GetAnalysisByID(ctx, node.AnalysisID)
		if err != nil {
			return fmt.Errorf("load analysis failed: %w", err)
		}

		if err := p.ensureNodePaths(node, analysis); err != nil {
			return err
		}
		if err := os.MkdirAll(node.WorkspaceDir, 0o755); err != nil {
			return fmt.Errorf("create workspace dir failed: %w", err)
		}
		if err := os.MkdirAll(node.OutputDir, 0o755); err != nil {
			return fmt.Errorf("create output dir failed: %w", err)
		}

		// 构建参数
		params, err := p.buildNodeParams(node, analysis)
		if err != nil {
			return err
		}
		if err := writeJSONAtomic(node.ParamsPath, params, 0o644); err != nil {
			return fmt.Errorf("write params json failed: %w", err)
		}
		scriptDir, scriptFile, _ := utils.GetScriptFile(p.storageBase, project.ProjectID, script.ScriptType, script.ScriptID)
		scriptPath := filepath.Join(scriptDir, scriptFile)
		if err := p.WriteCommand(node, script.ScriptType, scriptPath, params); err != nil {
			return fmt.Errorf("write command failed: %w", err)
		}
	}

	if err := cleanDirContents(node.OutputDir, prefix); err != nil {
		return fmt.Errorf("clean output dir failed: %w", err)
	}

	return nil
}

func (p *FileSystemNodeRuntimePreparer) WriteCommand(node *types.AnalysisNode, scriptType, scriptPath string, params map[string]any) error {

	scriptContent, err := os.ReadFile(scriptPath)
	if err != nil {
		return fmt.Errorf("read script file failed: %w", err)
	}
	scriptType = normalizeScriptType(scriptType)
	builder := p.builders[scriptType]
	if builder == nil {
		builder = p.builders["shell"]
	}
	runScript, err := builder.Build(node, scriptPath, string(scriptContent), params)
	if err != nil {
		return fmt.Errorf("build run script failed: %w", err)
	}
	if err := writeTextAtomic(node.CommandPath, runScript, 0o755); err != nil {
		return fmt.Errorf("write run.sh failed: %w", err)
	}
	return nil
}

func (p *FileSystemNodeRuntimePreparer) ensureNodePaths(node *types.AnalysisNode, analysis *types.Analysis) error {
	baseWorkspace := strings.TrimSpace(node.WorkspaceDir)
	if baseWorkspace == "" {
		analysisOutputDir := ""
		if analysis != nil {
			analysisOutputDir = strings.TrimSpace(analysis.OutputDir)
		}
		if analysisOutputDir == "" {
			return fmt.Errorf("node workspace_dir is empty and analysis output_dir is empty")
		}
		baseWorkspace = filepath.Join(analysisOutputDir, fmt.Sprintf("%d", node.ID))
		node.WorkspaceDir = baseWorkspace
	}

	if strings.TrimSpace(node.OutputDir) == "" {
		node.OutputDir = filepath.Join(baseWorkspace, "output")
	}
	if strings.TrimSpace(node.ParamsPath) == "" {
		node.ParamsPath = filepath.Join(baseWorkspace, "params.json")
	}
	if strings.TrimSpace(node.CommandPath) == "" {
		node.CommandPath = filepath.Join(baseWorkspace, "run.sh")
	}

	if err := os.MkdirAll(filepath.Dir(node.ParamsPath), 0o755); err != nil {
		return fmt.Errorf("create params dir failed: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(node.CommandPath), 0o755); err != nil {
		return fmt.Errorf("create command dir failed: %w", err)
	}
	return nil
}

func (p *FileSystemNodeRuntimePreparer) buildNodeParams(node *types.AnalysisNode, analysis *types.Analysis) (map[string]any, error) {
	baseParams := map[string]any{}
	if analysis != nil && strings.TrimSpace(analysis.ParamsPath) != "" {
		raw, err := os.ReadFile(analysis.ParamsPath)
		if err != nil {
			return nil, fmt.Errorf("read analysis params failed: %w", err)
		}
		if len(strings.TrimSpace(string(raw))) > 0 {
			if err := json.Unmarshal(raw, &baseParams); err != nil {
				return nil, fmt.Errorf("parse analysis params failed: %w", err)
			}
		}
	}

	resolvedInputs := map[string]any(node.ResolvedInputs)

	merged := map[string]any{}
	for k, v := range baseParams {
		merged[k] = v
	}
	for k, v := range resolvedInputs {
		merged[k] = v
	}
	merged["output_dir"] = node.OutputDir

	return merged, nil
}
func cloneAnyMapForNode(in map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// func (p *FileSystemNodeRuntimePreparer) initializeStandaloneNodeArtifacts(
// 	ctx context.Context,
// 	// scriptID int64,
// 	node *types.AnalysisNode,
// 	// analysis *types.Analysis,
// 	// artifacts *standaloneNodeArtifacts,
// 	// params map[string]interface{},
// ) error {
// 	// if artifacts == nil {
// 	// 	return fmt.Errorf("artifacts is nil")
// 	// }

// 	if err := os.MkdirAll(node.OutputDir, 0o755); err != nil {
// 		return err
// 	}
// 	// projectDir := utils.GetProjectDir(p.storageBase, projectID)

// 	paramsPayload := cloneAnyMapForNode(map[string]interface{}(node.Params))
// 	paramsPayload["output_dir"] = node.OutputDir
// 	// paramsPayload["project_dir"] = node.projectDir
// 	paramsBytes, err := json.MarshalIndent(paramsPayload, "", "  ")
// 	if err != nil {
// 		return err
// 	}
// 	paramsBytes = append(paramsBytes, '\n')
// 	if err := os.WriteFile(node.ParamsPath, paramsBytes, 0o644); err != nil {
// 		return err
// 	}

// 	script, err := p.workflowService.GetScriptByID(ctx, node.ScriptID)
// 	if err != nil {
// 		return err
// 	}
// 	if script == nil {
// 		return fmt.Errorf("script not found")
// 	}
// 	scriptDir, scriptMainFile, err := p.workflowService.GetScriptFileByScriptID(ctx, script.ID)
// 	if err != nil {
// 		return err
// 	}
// 	scriptPath := filepath.Join(scriptDir, scriptMainFile)
// 	// baseDir := "."
// 	// if h != nil && h.config != nil && h.config.Storage != nil {
// 	// 	if v := strings.TrimSpace(h.config.Storage.BaseDir); v != "" {
// 	// 		baseDir = v
// 	// 	}
// 	// }
// 	if !filepath.IsAbs(scriptPath) {
// 		scriptPath = filepath.Join(p.storageBase, scriptPath)
// 	}

// 	scriptContent, err := os.ReadFile(scriptPath)
// 	if err != nil {
// 		return err
// 	}

// 	scriptWorkspaceDir := filepath.Join(node.WorkspaceDir, scriptMainFile)
// 	if _, err := os.Lstat(scriptWorkspaceDir); err != nil {
// 		if os.IsNotExist(err) {
// 			if err := os.Symlink(scriptPath, scriptWorkspaceDir); err != nil {
// 				return err
// 			}
// 		} else {
// 			return err
// 		}
// 	}
// 	// synlink io_schema.json
// 	ioSchemaPath := filepath.Join(scriptDir, "io_schema.json")
// 	scriptWorkspaceIoSchemaPath := filepath.Join(node.WorkspaceDir, "io_schema.json")
// 	if _, err := os.Lstat(ioSchemaPath); err == nil {
// 		if _, err := os.Lstat(scriptWorkspaceIoSchemaPath); err != nil {

// 			if os.IsNotExist(err) {
// 				if err := os.Symlink(ioSchemaPath, scriptWorkspaceIoSchemaPath); err != nil {
// 					return err
// 				}
// 			}
// 		}
// 	}

// 	// node := &types.AnalysisNode{
// 	// 	ID:           artifacts.ID,
// 	// 	ParamsPath:   artifacts.ParamsPath,
// 	// 	OutputDir:    artifacts.OutputDir,
// 	// 	WorkspaceDir: artifacts.WorkspaceDir,
// 	// 	CommandPath:  artifacts.CommandPath,
// 	// 	LogPath:      artifacts.LogPath,
// 	// }
// 	// runScript, err := buildStandaloneRunScript(node, script.ScriptType, scriptPath, string(scriptContent), paramsPayload)

// 	runScript, err := BuildRunScript(node, script.ScriptType, scriptPath, string(scriptContent), paramsPayload)
// 	if err != nil {
// 		return err
// 	}
// 	if err := os.WriteFile(node.CommandPath, []byte(runScript), 0o755); err != nil {
// 		return err
// 	}

// 	if _, err := os.Stat(node.LogPath); err != nil {
// 		if os.IsNotExist(err) {
// 			if err := os.WriteFile(node.LogPath, []byte(""), 0o644); err != nil {
// 				return err
// 			}
// 		} else {
// 			return err
// 		}
// 	}

// 	return nil
// }

// func buildStandaloneRunScript(
// 	node *types.AnalysisNode,
// 	scriptType string,
// 	scriptPath string,
// 	scriptContent string,
// 	params map[string]interface{},
// ) (string, error) {

// 	return BuildRunScript(node, scriptType, scriptPath, scriptContent, params)
// }

// func (p *FileSystemNodeRuntimePreparer) resolveScriptPath(scriptID string, scriptType string) string {
// 	mainFile := mainFileByScriptType(scriptType)
// 	if strings.TrimSpace(p.storageBase) == "" {
// 		return filepath.Join("pipeline", "script", scriptID, mainFile)
// 	}
// 	return filepath.Join(p.storageBase, "pipeline", "script", scriptID, mainFile)
// }

func normalizeScriptType(scriptType string) string {
	typeName := strings.ToLower(strings.TrimSpace(scriptType))
	switch typeName {
	case "", "bash", "sh":
		return "shell"
	default:
		return typeName
	}
}

func mainFileByScriptType(scriptType string) string {
	switch normalizeScriptType(scriptType) {
	case "r":
		return "main.R"
	case "python":
		return "main.py"
	case "shell":
		return "main.sh"
	default:
		return "main.sh"
	}
}

func cleanDirContents(outputDir, prefix string) error {
	// entries, err := os.ReadDir(dir)
	// if err != nil {
	// 	if os.IsNotExist(err) {
	// 		return nil
	// 	}
	// 	return err
	// }
	// for _, entry := range entries {
	// 	if err := os.RemoveAll(filepath.Join(dir, entry.Name())); err != nil {
	// 		return err
	// 	}
	// }
	// return nil
	//判断是否以prefix开头 如果不是就不删除
	if !strings.HasPrefix(outputDir, prefix) {
		logger.Warnf(context.Background(), "[cleanDirContents] output dir=%s is not under project data dir=%s, skip delete", outputDir, prefix)
		return fmt.Errorf("output dir=%s is not under project data dir=%s", outputDir, prefix)
	}
	// 删除outputDir下的所有内容，，直接判断文件夹是否存在，如果存在就删除，如果不存在就不删除
	if _, err := os.Stat(outputDir); err == nil {
		// 不删除outputDir本身，只删除里面的内容
		files, err := os.ReadDir(outputDir)
		if err != nil {
			logger.Warnf(context.Background(), "[cleanDirContents] failed to read output dir=%s, err=%v", outputDir, err)
			return err
		}
		for _, file := range files {
			filePath := filepath.Join(outputDir, file.Name())
			if err := os.RemoveAll(filePath); err != nil {
				logger.Warnf(context.Background(), "[cleanDirContents] failed to delete file=%s in output dir=%s, err=%v", filePath, outputDir, err)
				return err
			}
		}
	}

	return nil
}

func writeJSONAtomic(path string, value any, mode os.FileMode) error {
	content, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	content = append(content, '\n')
	return writeBytesAtomic(path, content, mode)
}

func writeTextAtomic(path string, content string, mode os.FileMode) error {
	return writeBytesAtomic(path, []byte(content), mode)
}

func writeBytesAtomic(path string, content []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}

	if _, err := tmp.Write(content); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Chmod(tmpPath, mode); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}

func cloneRunScriptBuilders(builders map[string]RunScriptBuilder) map[string]RunScriptBuilder {
	if len(builders) == 0 {
		return NewRunScriptBuilders()
	}

	cloned := make(map[string]RunScriptBuilder, len(builders))
	for scriptType, builder := range builders {
		cloned[scriptType] = builder
	}
	return cloned
}
