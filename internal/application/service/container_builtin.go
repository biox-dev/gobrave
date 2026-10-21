package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/biox-dev/gobrave/builtin"
	"github.com/biox-dev/gobrave/internal/exportcodec"
	"github.com/biox-dev/gobrave/internal/logger"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
)

// 内置容器模板：源码位于仓库 builtin/container_templates/*.json，
// 由 go:embed 打包进二进制；系统每次启动都会把它们按主键 upsert 进数据库。
//
// 为什么用「导出格式的 JSON 文件」而不是在 Go 代码里硬编码结构体：
//   - 一个文件就是一份完整的容器资产包（container_template_specs /
//     container_template_definitions），与 script.json / workflow.json 里的容器资产结构完全一致，
//     可以直接从「容器模板导出」接口拿到的 JSON 粘贴过来；
//   - 导入逻辑直接复用 exportcodec.InstallContainerAssets（运行配置 → 绑定行，
//     按主键 upsert），不再维护第二套写入逻辑；
//   - 新增内置模板 = 往目录里放一个 JSON 文件，无需改代码。
//
// 导入语义：以文件为准。已存在的行会被文件内容覆盖（相当于每次启动重新安装一遍内置模板），
// is_default=true 的绑定行额外走互斥的默认标记流程（与 ImportContainerTemplate 一致）。
// 单个文件解析 / 写入失败只记录日志，不影响其它模板，也不阻断启动。

// BuiltinContainerTemplateDir 是内置容器模板源码目录（相对工作目录，与内置提示词一致）。
const BuiltinContainerTemplateDir = "builtin/container_templates"

// builtinContainerTemplateBaseDir 是运行期查找内置容器模板的磁盘目录（空串表示按工作目录解析）。
// 仅用于测试或自定义部署目录；非并发安全，应在服务启动前调用。
var builtinContainerTemplateBaseDir string

// SetBuiltinContainerTemplateDir 覆盖内置容器模板源码目录（空串表示恢复默认）。
// 仅用于测试或自定义部署目录；非并发安全，应在服务启动前调用。
func SetBuiltinContainerTemplateDir(dir string) {
	builtinContainerTemplateBaseDir = strings.TrimSpace(dir)
}

// InstallBuiltinContainerTemplates 把内置容器模板逐个导入数据库（系统启动时调用）。
//
// 读取顺序与内置提示词 / 技能一致：先读磁盘 <base>/builtin/container_templates/*.json
// （便于运维直接修改部署目录里的文件），目录不存在时回退到编译进二进制的内嵌副本。
//
// 每个文件是一次独立的 InstallContainerAssets（各自一个事务内按主键 upsert），
// 某个文件失败时记录日志并继续处理后面的文件；返回第一个错误，便于测试断言。
func InstallBuiltinContainerTemplates(ctx context.Context, svc interfaces.ContainerService) error {
	if svc == nil {
		return nil
	}

	bundles, err := loadBuiltinContainerTemplates()
	if err != nil {
		logger.Warnf(ctx, "[Container] Failed to load builtin container templates: %v", err)
		return err
	}

	var firstErr error
	for _, item := range bundles {
		if _, err := exportcodec.InstallContainerAssets(ctx, svc,
			item.Bundle.ContainerTemplateSpecs,
			item.Bundle.ContainerTemplateDefinitions); err != nil {
			logger.Warnf(ctx, "[Container] Failed to install builtin container template %q: %v", item.Name, err)
			if firstErr == nil {
				firstErr = fmt.Errorf("install builtin container template %q: %w", item.Name, err)
			}
			continue
		}

		// 文件里标记为默认的绑定行走互斥的默认标记流程（默认模板全局唯一）。
		for _, definition := range item.Bundle.ContainerTemplateDefinitions {
			if !definition.IsDefault {
				continue
			}
			if err := svc.SetDefaultContainerTemplate(ctx, definition.ID); err != nil {
				logger.Warnf(ctx, "[Container] Failed to set default for builtin container template %q: %v", item.Name, err)
				if firstErr == nil {
					firstErr = fmt.Errorf("set default for builtin container template %q: %w", item.Name, err)
				}
			}
			break
		}

		logger.Debugf(ctx, "[Container] Installed builtin container template %q (specs=%d definitions=%d)",
			item.Name, len(item.Bundle.ContainerTemplateSpecs), len(item.Bundle.ContainerTemplateDefinitions))
	}
	return firstErr
}

// builtinContainerTemplate 是一个待导入的内置容器模板。
type builtinContainerTemplate struct {
	// Name 是模板文件名（不含 .json），仅用于日志与错误信息。
	Name   string
	Bundle *types.ContainerTemplateBundle
}

// loadBuiltinContainerTemplates 读出全部内置容器模板（磁盘优先，缺失时回退到内嵌副本）。
// 文件名按字典序返回，保证导入顺序稳定、日志可复现。
func loadBuiltinContainerTemplates() ([]builtinContainerTemplate, error) {
	dir := builtinContainerTemplateBaseDir
	if dir == "" {
		dir = BuiltinContainerTemplateDir
	}

	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}

	// 磁盘上有目录：以磁盘文件为准（运维可直接覆盖 / 增删）。
	if err == nil {
		out := make([]builtinContainerTemplate, 0, len(entries))
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			item, err := readBuiltinContainerTemplateFile(filepath.Join(dir, entry.Name()), entry.Name())
			if err != nil {
				return nil, err
			}
			out = append(out, item)
		}
		if len(out) > 0 {
			return out, nil
		}
	}

	// 磁盘目录不存在或里面没有 .json：回退到编译进二进制的内嵌副本。
	return loadEmbeddedContainerTemplates()
}

// loadEmbeddedContainerTemplates 读取 go:embed 内嵌的内置容器模板。
func loadEmbeddedContainerTemplates() ([]builtinContainerTemplate, error) {
	entries, err := fs.ReadDir(builtin.ContainerTemplateFiles, builtin.ContainerTemplateFSRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)

	out := make([]builtinContainerTemplate, 0, len(names))
	for _, name := range names {
		content, err := fs.ReadFile(builtin.ContainerTemplateFiles, path.Join(builtin.ContainerTemplateFSRoot, name))
		if err != nil {
			return nil, err
		}
		bundle, err := decodeContainerTemplateBundle(content)
		if err != nil {
			return nil, fmt.Errorf("builtin container template %q: %w", name, err)
		}
		out = append(out, builtinContainerTemplate{Name: strings.TrimSuffix(name, ".json"), Bundle: bundle})
	}
	return out, nil
}

// readBuiltinContainerTemplateFile 读取并解析一个磁盘上的内置容器模板文件。
func readBuiltinContainerTemplateFile(filePath, name string) (builtinContainerTemplate, error) {
	content, err := os.ReadFile(filePath)
	if err != nil {
		return builtinContainerTemplate{}, err
	}
	bundle, err := decodeContainerTemplateBundle(content)
	if err != nil {
		return builtinContainerTemplate{}, fmt.Errorf("builtin container template %s: %w", filePath, err)
	}
	return builtinContainerTemplate{Name: strings.TrimSuffix(name, ".json"), Bundle: bundle}, nil
}

// decodeContainerTemplateBundle 把 JSON 解析成容器资产包。
//
// 允许省略某个列表（缺失时按空列表处理），使文件可以只写需要的那部分；
// 解析失败时返回错误（内置模板是代码资产，写错应当被发现而不是静默跳过）。
func decodeContainerTemplateBundle(content []byte) (*types.ContainerTemplateBundle, error) {
	bundle := types.NewContainerTemplateBundle()
	if err := json.Unmarshal(content, bundle); err != nil {
		return nil, err
	}
	if bundle.ContainerTemplateSpecs == nil {
		bundle.ContainerTemplateSpecs = make([]types.ContainerTemplateSpec, 0)
	}
	if bundle.ContainerTemplateDefinitions == nil {
		bundle.ContainerTemplateDefinitions = make([]types.ContainerTemplateDefinition, 0)
	}
	return bundle, nil
}
