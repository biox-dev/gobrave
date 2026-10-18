package config

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Section 描述一个可以通过可视化界面编辑的配置段。
//
// 新增一个可编辑的配置段只需要在 sections 中注册一次，
// HTTP 层无需新增接口，前端只需新增一个 section 组件。
type Section struct {
	// Key 是配置段在 config.yml 中的顶层键名，同时也是 API 中使用的段标识。
	Key string
	// Label 是用于错误提示的中文名称。
	Label string
	// Effective 返回当前生效的配置段值（用于表单回填与落盘）。
	Effective func(cfg *Config) any
	// Decode 解析并归一化请求体，返回待落盘的值；不会修改 cfg。
	Decode func(raw json.RawMessage) (any, error)
	// Apply 把已经成功落盘的值写入内存配置，使其立即生效。
	Apply func(cfg *Config, value any) error
}

// sections 是可视化配置段的注册表。
var sections = []*Section{
	newSection(
		"database",
		"数据库",
		func(c *Config) *DatabaseConfig {
			if c.Database == nil {
				c.Database = &DatabaseConfig{}
			}
			return c.Database
		},
		normalizeDatabaseConfig,
	),
	newSection(
		"container",
		"容器",
		func(c *Config) *ContainerConfig {
			if c.Container == nil {
				c.Container = DefaultContainerConfig()
			}
			return c.Container
		},
		normalizeContainerConfig,
	),
	newSection(
		"agent",
		"AI Agent",
		func(c *Config) *AgentConfig {
			if c.Agent == nil {
				c.Agent = &AgentConfig{Providers: map[string]ModelProviderConfig{}}
			}
			return c.Agent
		},
		normalizeAgentConfig,
	),
}

// RegisteredSections 返回所有已注册的可视化配置段。
func RegisteredSections() []*Section {
	return sections
}

// FindSection 按 key 查找已注册的配置段。
func FindSection(key string) (*Section, bool) {
	for _, s := range sections {
		if s.Key == key {
			return s, true
		}
	}
	return nil, false
}

// newSection 为具体的配置段结构体构建 Section。
// 泛型参数保证 Decode 返回值的类型安全，避免在注册表里到处写类型断言。
func newSection[T any](key, label string, target func(*Config) *T, normalize func(*T) error) *Section {
	return &Section{
		Key:   key,
		Label: label,
		Effective: func(cfg *Config) any {
			return target(cfg)
		},
		Decode: func(raw json.RawMessage) (any, error) {
			value := new(T)
			if err := json.Unmarshal(raw, value); err != nil {
				return nil, fmt.Errorf("invalid %s config: %w", label, err)
			}
			if normalize != nil {
				if err := normalize(value); err != nil {
					return nil, err
				}
			}
			return value, nil
		},
		Apply: func(cfg *Config, value any) error {
			typed, ok := value.(*T)
			if !ok {
				return fmt.Errorf("unexpected %s config type %T", label, value)
			}
			*target(cfg) = *typed
			return nil
		},
	}
}

// normalizeDatabaseConfig 清理数据库配置中的空白并补全驱动默认值。
func normalizeDatabaseConfig(db *DatabaseConfig) error {
	if db == nil {
		return fmt.Errorf("database config is required")
	}

	db.Driver = strings.ToLower(strings.TrimSpace(db.Driver))
	if db.Driver == "" {
		db.Driver = "sqlite"
	}
	db.Host = strings.TrimSpace(db.Host)
	db.Port = strings.TrimSpace(db.Port)
	db.User = strings.TrimSpace(db.User)
	db.Name = strings.TrimSpace(db.Name)
	db.SSLMode = strings.TrimSpace(db.SSLMode)
	db.Path = strings.TrimSpace(db.Path)
	// 非法/空取值统一收敛到默认级别，避免把无效值写回 config.yml。
	db.LogLevel = normalizeDatabaseLogLevel(db.LogLevel)

	switch db.Driver {
	case "sqlite":
		// path 留空时由 initDatabase 回退到 storage.base_dir/db/gobrave.db。
		return nil
	case "postgres", "mysql":
		if db.Host == "" || db.Port == "" || db.User == "" || db.Name == "" {
			return fmt.Errorf("host, port, user and name are required for %s", db.Driver)
		}
		return nil
	default:
		return fmt.Errorf("unsupported database driver %q, expected one of %s",
			db.Driver, strings.Join(supportedDatabaseDrivers, ", "))
	}
}

// supportedDatabaseDrivers 与 initDatabase 支持的驱动保持一致。
var supportedDatabaseDrivers = []string{"sqlite", "postgres", "mysql"}

// normalizeContainerConfig 归一化并校验容器配置。
// 与 LoadConfig 中的处理保持一致，避免「UI 保存」与「文件加载」产生不同结果。
func normalizeContainerConfig(c *ContainerConfig) error {
	if c == nil {
		return fmt.Errorf("container config is required")
	}

	c.DefaultRuntime = normalizeContainerRuntime(c.DefaultRuntime)
	c.Runtimes = normalizeContainerRuntimes(c.Runtimes)
	c.DagNodeCleanupOnFailed = normalizeContainerCleanupPolicy(c.DagNodeCleanupOnFailed, "stop")
	c.DagNodeCleanupOnDagFinished = normalizeContainerCleanupPolicy(c.DagNodeCleanupOnDagFinished, "delete")

	if c.Kubernetes == nil {
		c.Kubernetes = DefaultKubernetesRuntimeConfig()
	}
	c.Kubernetes.Namespace = strings.TrimSpace(c.Kubernetes.Namespace)
	if c.Kubernetes.Namespace == "" {
		c.Kubernetes.Namespace = "default"
	}
	c.Kubernetes.Kubeconfig = strings.TrimSpace(c.Kubernetes.Kubeconfig)

	// 队列为空表示沿用管理器内置的默认值，负数没有意义。
	if c.CreateQueueMaxConcurrency < 0 {
		return fmt.Errorf("create_queue_max_concurrency must be >= 0")
	}
	if c.CreateQueueMaxPending < 0 {
		return fmt.Errorf("create_queue_max_pending must be >= 0")
	}

	// ResolveContainerRuntime 优先使用 default_runtime，因此它必须被注册在 runtimes 中，
	// 否则默认运行时不会被启动时注册，创建容器时无法解析运行时。
	if len(c.Runtimes) > 0 && !containsString(c.Runtimes, c.DefaultRuntime) {
		return fmt.Errorf("default_runtime %q must be included in runtimes %v", c.DefaultRuntime, c.Runtimes)
	}

	return nil
}

func containsString(values []string, target string) bool {
	for _, v := range values {
		if v == target {
			return true
		}
	}
	return false
}

// normalizeAgentConfig 归一化 AI Agent 的模型提供商配置。
//
// providers 的 key 是模型名（与 Profile.model 对应），统一转为小写并去掉首尾空白；
// 值中的 model_id 为空时回退到 key，provider 为空时回退到 extra.type / extra.provider。
//
// 然后以「厂商预置表」为底做合并（见 DefaultModelProviderConfig / MergeModelProviderConfig）：
// 用户只需要给出 provider（厂商类型）+ model/api_model（模型名）+ api_key，其余
// base_url / 上下文窗口 / 最大输出 / 是否支持推理 / 价格等元信息自动补齐。
func normalizeAgentConfig(a *AgentConfig) error {
	if a == nil {
		return fmt.Errorf("agent config is required")
	}

	providers := make(map[string]ModelProviderConfig, len(a.Providers))
	for name, pc := range a.Providers {
		key := strings.ToLower(strings.TrimSpace(name))
		if key == "" {
			return fmt.Errorf("agent provider name must not be empty")
		}

		pc.ModelID = strings.TrimSpace(pc.ModelID)
		if pc.ModelID == "" {
			pc.ModelID = key
		}
		pc.Provider = strings.ToLower(strings.TrimSpace(pc.Provider))
		pc.APIModel = strings.TrimSpace(pc.APIModel)
		pc.BaseURL = strings.TrimSpace(pc.BaseURL)
		pc.APIKey = strings.TrimSpace(pc.APIKey)
		pc.Name = strings.TrimSpace(pc.Name)
		pc.ReasoningEffort = strings.ToLower(strings.TrimSpace(pc.ReasoningEffort))
		if len(pc.Extra) == 0 {
			pc.Extra = nil
		} else {
			extra := make(map[string]string, len(pc.Extra))
			for k, v := range pc.Extra {
				extra[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
			}
			pc.Extra = extra
		}

		// 厂商类型回退：provider 字段 → extra.provider → extra.type。
		if pc.Provider == "" {
			pc.Provider = strings.ToLower(strings.TrimSpace(pc.Extra["provider"]))
		}
		if pc.Provider == "" {
			pc.Provider = strings.ToLower(strings.TrimSpace(pc.Extra["type"]))
		}
		if pc.Provider == "" {
			return fmt.Errorf("agent provider %q: provider (vendor) is required", key)
		}
		if _, ok := FindModelVendor(pc.Provider); !ok {
			return fmt.Errorf("agent provider %q: unsupported vendor %q, expected one of %s",
				key, pc.Provider, strings.Join(modelVendorTypes(), ", "))
		}

		// 模型能力元信息：厂商预置表兜底，用户显式填写的字段优先。
		pc = MergeModelProviderConfig(DefaultModelProviderConfig(pc.Provider, key), pc)
		if pc.APIModel == "" {
			pc.APIModel = pc.ModelID
		}

		if _, ok := providers[key]; ok {
			return fmt.Errorf("duplicate agent provider %q", key)
		}
		providers[key] = pc
	}
	a.Providers = providers

	return nil
}

// modelVendorTypes 返回内置厂商类型列表（用于错误提示）。
func modelVendorTypes() []string {
	vendors := ModelVendors()
	types := make([]string, 0, len(vendors))
	for _, v := range vendors {
		types = append(types, v.Type)
	}
	return types
}
