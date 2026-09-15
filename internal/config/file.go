package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/biox-dev/gobrave/internal/utils"
	"github.com/goccy/go-yaml"
)

// DefaultConfigFileName 是默认的配置文件名。
const DefaultConfigFileName = "config.yml"

// ConfigFilePath 解析 config.yml 的路径，规则与 LoadConfig 保持一致：
// CLI --config > BRAVE_CONFIG_DIR > 当前工作目录。
func ConfigFilePath() (string, error) {
	name := DefaultConfigFileName
	if cliFlags != nil && strings.TrimSpace(cliFlags.ConfigPath) != "" {
		name = strings.TrimSpace(cliFlags.ConfigPath)
	}
	return utils.ResolveExternalPath(name)
}

// ConfigFile 表示 config.yml 的原始 YAML 文档。
// 它保留顶层与嵌套键的顺序，因此更新某个配置段时不会打乱文件中的其他内容。
type ConfigFile struct {
	// Path 是 config.yml 的绝对路径。
	Path string
	// Exists 表示文档被加载时文件是否已经存在。
	Exists bool
	// root 是保留键顺序的文档根节点。
	root yaml.MapSlice
}

// LoadConfigFile 读取 config.yml。
// 文件不存在时返回 Exists=false 的空文档（不视为错误），
// 调用方可以据此回退到代码中的默认配置。
func LoadConfigFile() (*ConfigFile, error) {
	path, err := ConfigFilePath()
	if err != nil {
		return nil, err
	}

	doc := &ConfigFile{Path: path}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return doc, nil
		}
		return nil, fmt.Errorf("failed to read config file %s: %w", path, err)
	}

	doc.Exists = true
	if strings.TrimSpace(string(data)) == "" {
		return doc, nil
	}
	if err := yaml.UnmarshalWithOptions(data, &doc.root, yaml.UseOrderedMap()); err != nil {
		return nil, fmt.Errorf("failed to parse config file %s: %w", path, err)
	}
	return doc, nil
}

// Section 返回某个顶层配置段的原始值，不存在时返回 nil。
func (d *ConfigFile) Section(key string) any {
	if d == nil {
		return nil
	}
	for _, item := range d.root {
		if fmt.Sprint(item.Key) == key {
			return item.Value
		}
	}
	return nil
}

// SetSection 写入或替换一个顶层配置段。
func (d *ConfigFile) SetSection(key string, value any) {
	for i, item := range d.root {
		if fmt.Sprint(item.Key) == key {
			d.root[i].Value = value
			return
		}
	}
	d.root = append(d.root, yaml.MapItem{Key: key, Value: value})
}

// SetSectionMerged 用 value 覆盖某个顶层配置段，同时保留原段落中 value 未声明的键。
//
// 这样即使 config.yml 里存在当前版本结构体尚未建模的键（例如旧版本的
// container.create_queue_enabled），保存其它字段时也不会把它们丢掉。
func (d *ConfigFile) SetSectionMerged(key string, value any) error {
	encoded, err := yaml.Marshal(value)
	if err != nil {
		return fmt.Errorf("failed to encode %s section: %w", key, err)
	}

	var next yaml.MapSlice
	if err := yaml.UnmarshalWithOptions(encoded, &next, yaml.UseOrderedMap()); err != nil {
		return fmt.Errorf("failed to normalize %s section: %w", key, err)
	}

	if existing, ok := d.Section(key).(yaml.MapSlice); ok {
		for _, item := range existing {
			if !mapSliceHasKey(next, fmt.Sprint(item.Key)) {
				next = append(next, item)
			}
		}
	}

	d.SetSection(key, next)
	return nil
}

func mapSliceHasKey(items yaml.MapSlice, key string) bool {
	for _, item := range items {
		if fmt.Sprint(item.Key) == key {
			return true
		}
	}
	return false
}

// Save 把文档写回 config.yml；文件或其父目录不存在时自动创建。
func (d *ConfigFile) Save() error {
	out, err := yaml.Marshal(d.root)
	if err != nil {
		return fmt.Errorf("failed to encode config file %s: %w", d.Path, err)
	}
	if dir := filepath.Dir(d.Path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("failed to create config dir %s: %w", dir, err)
		}
	}
	if err := os.WriteFile(d.Path, out, 0o644); err != nil {
		return fmt.Errorf("failed to write config file %s: %w", d.Path, err)
	}
	return nil
}

// SaveConfigSection 把某个顶层配置段写入 config.yml（不存在则创建），
// 其余配置段保持原样，该段中未建模的键也会保留。返回实际写入的文件路径。
func SaveConfigSection(key string, value any) (string, error) {
	doc, err := LoadConfigFile()
	if err != nil {
		return "", err
	}
	if err := doc.SetSectionMerged(key, value); err != nil {
		return "", err
	}
	if err := doc.Save(); err != nil {
		return "", err
	}
	return doc.Path, nil
}
