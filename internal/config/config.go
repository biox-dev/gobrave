package config

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gobravedev/gobrave/internal/logger"
	"github.com/gobravedev/gobrave/internal/utils"
	"github.com/goccy/go-yaml"
)

// CLIFlags holds command-line arguments that can override config.yml values.
type CLIFlags struct {
	ConfigPath string // --config, config file path
	Port       int    // --port, server port
	Host       string // --host, server host
	LogPath    string // --log-path, server log path
	DBDriver   string // --db-driver, database driver (sqlite/postgres)
	DBHost     string // --db-host, database host
	DBPort     string // --db-port, database port
	DBUser     string // --db-user, database user
	DBPassword string // --db-password, database password
	DBName     string // --db-name, database name
	DBPath     string // --db-path, sqlite database file path
	DBSSLMode  string // --db-ssl-mode, database ssl mode
	Runtime    string // --runtime, container runtime (docker/k8s/k3s)
	BaseDir    string // --base-dir, storage base directory

	// disableRegistration points to a bool set by --disable-registration.
	// nil means the flag was not provided on the command line.
	disableRegistration *bool
}

// DisableRegistrationSet reports whether --disable-registration was explicitly
// provided on the command line and returns its value.
func (f *CLIFlags) DisableRegistrationSet() (value bool, ok bool) {
	if f.disableRegistration == nil {
		return false, false
	}
	return *f.disableRegistration, true
}

// cliFlags holds the parsed command-line overrides.
// Set via SetCLIFlags before LoadConfig is called.
var cliFlags *CLIFlags

// SetCLIFlags sets the CLI overrides to be applied by LoadConfig.
// Call this before LoadConfig is invoked (e.g. in main.go).
func SetCLIFlags(f *CLIFlags) {
	cliFlags = f
}

// ParseCLIFlags parses command-line flags into a CLIFlags struct.
// It does NOT call flag.Parse() — the caller should do that.
func ParseCLIFlags() *CLIFlags {
	f := &CLIFlags{}
	flag.StringVar(&f.ConfigPath, "config", "", "Path to config.yml file")
	flag.IntVar(&f.Port, "port", 0, "Server port (overrides config.yml)")
	flag.StringVar(&f.Host, "host", "", "Server host (overrides config.yml)")
	flag.StringVar(&f.LogPath, "log-path", "", "Server log path (overrides config.yml)")
	flag.StringVar(&f.DBDriver, "db-driver", "", "Database driver: sqlite or postgres")
	flag.StringVar(&f.DBHost, "db-host", "", "Database host")
	flag.StringVar(&f.DBPort, "db-port", "", "Database port")
	flag.StringVar(&f.DBUser, "db-user", "", "Database user")
	flag.StringVar(&f.DBPassword, "db-password", "", "Database password")
	flag.StringVar(&f.DBName, "db-name", "", "Database name")
	flag.StringVar(&f.DBPath, "db-path", "", "SQLite database file path")
	flag.StringVar(&f.DBSSLMode, "db-ssl-mode", "", "Database SSL mode")
	flag.StringVar(&f.Runtime, "runtime", "", "Container runtime: docker, k8s, k3s")
	flag.StringVar(&f.BaseDir, "base-dir", "", "Storage base directory (overrides config.yml)")
	flag.Func("disable-registration", "Disable user registration (true/false)", func(s string) error {
		v, err := strconv.ParseBool(s)
		if err != nil {
			return fmt.Errorf("invalid boolean value %q for --disable-registration: %v", s, err)
		}
		f.disableRegistration = &v
		return nil
	})
	return f
}

type Config struct {
	Server    *ServerConfig    `yaml:"server"   json:"server"`
	Database  *DatabaseConfig  `yaml:"database" json:"database"`
	Feed      *FeedConfig      `yaml:"feed"     json:"feed"`
	Proxy     *ProxyConfig     `yaml:"proxy"    json:"proxy"`
	Route     *RouteConfig     `yaml:"route"    json:"route"`
	Storage   *StorageConfig   `yaml:"storage"  json:"storage"`
	Realtime  *RealtimeConfig  `yaml:"realtime" json:"realtime"`
	LLM       *LLMConfig       `yaml:"llm" json:"llm"`
	Container *ContainerConfig `yaml:"container" json:"container"`
	// Ingest   *IngestConfig   `yaml:"ingest"   json:"ingest"`
	Tenant      *TenantConfig `yaml:"tenant"   json:"tenant"`
	DebugConfig *DebugConfig  `yaml:"debug"    json:"debug"`
	User        *UserConfig   `yaml:"user"     json:"user"`

	// Audio  *AudioConfig  `yaml`
}
type DebugConfig struct {
	EnableDagOrchestrator bool `yaml:"enable_dag_orchestrator" json:"enable_dag_orchestrator"`
}

type UserConfig struct {
	DisableRegistration bool `yaml:"disable_registration" json:"disable_registration"`
}
type LLMConfig struct {
	CLIURL      string             `yaml:"cli_url" json:"cli_url"`
	Model       string             `yaml:"model" json:"model"`
	GitHubToken string             `yaml:"github_token" json:"github_token"`
	Provider    *LLMProviderConfig `yaml:"provider" json:"provider"`
}

type LLMProviderConfig struct {
	Type        string `yaml:"type" json:"type"`
	BaseURL     string `yaml:"base_url" json:"base_url"`
	APIKey      string `yaml:"api_key" json:"api_key"`
	BearerToken string `yaml:"bearer_token" json:"bearer_token"`
}

type RealtimeConfig struct {
	Transport             string `yaml:"transport" json:"transport"`
	MaxConnectionsPerUser int    `yaml:"max_connections_per_user" json:"max_connections_per_user"`
	AckTimeoutSeconds     int    `yaml:"ack_timeout_seconds" json:"ack_timeout_seconds"`
	AckMaxRetries         int    `yaml:"ack_max_retries" json:"ack_max_retries"`
}

type ContainerConfig struct {
	Runtime                             string                   `yaml:"runtime" json:"runtime"`
	Kubernetes                          *KubernetesRuntimeConfig `yaml:"kubernetes" json:"kubernetes"`
	RefreshImageStatusOnStart           bool                     `yaml:"refresh_image_status_on_start" json:"refresh_image_status_on_start"`
	RecoverRunningDagOnStart            bool                     `yaml:"recover_running_dag_on_start" json:"recover_running_dag_on_start"`
	CleanupDagNodeContainersBeforeStart bool                     `yaml:"cleanup_dag_node_containers_before_start" json:"cleanup_dag_node_containers_before_start"`
	DeleteContainerOnNodeSuccess        bool                     `yaml:"delete_container_on_node_success" json:"delete_container_on_node_success"`
	DagNodeCleanupOnFailed              string                   `yaml:"dag_node_cleanup_on_failed" json:"dag_node_cleanup_on_failed"`
	DagNodeCleanupOnDagFinished         string                   `yaml:"dag_node_cleanup_on_dag_finished" json:"dag_node_cleanup_on_dag_finished"`
	// CreateQueueMaxConcurrency limits how many containers can be created concurrently.
	CreateQueueMaxConcurrency int `yaml:"create_queue_max_concurrency" json:"create_queue_max_concurrency"`
	// CreateQueueMaxPending limits how many creation requests can wait in the queue.
	CreateQueueMaxPending int `yaml:"create_queue_max_pending" json:"create_queue_max_pending"`
}

type KubernetesRuntimeConfig struct {
	Namespace  string `yaml:"namespace" json:"namespace"`
	Kubeconfig string `yaml:"kubeconfig" json:"kubeconfig"`
	InCluster  bool   `yaml:"in_cluster" json:"in_cluster"`
}

type StorageConfig struct {
	// ImageDir string `yaml:"image_dir" json:"image_dir"`
	BaseDir string `yaml:"base_dir" json:"base_dir"`
}

type ProxyConfig struct {
	BraveAPI   string `yaml:"brave_api" json:"brave_api"`
	Container  string `yaml:"container" json:"container"`
	OnlyOffice string `yaml:"onlyoffice" json:"onlyoffice"`
}

type RouteConfig struct {
	Registry   string                 `yaml:"registry"     json:"registry"`
	AppsPrefix string                 `yaml:"apps_prefix"  json:"apps_prefix"`
	Traefik    *TraefikRouteConfig    `yaml:"traefik"      json:"traefik"`
	K8sIngress *K8sIngressRouteConfig `yaml:"k8s_ingress"  json:"k8s_ingress"`
}

const defaultAppsPrefix = "/apps"

// resolveDefaultBaseDir returns the default storage base directory.
// Priority: GOBRAVE_BASE_DIR env > $HOME/.gobrave
func resolveDefaultBaseDir() string {
	if dir := strings.TrimSpace(os.Getenv("GOBRAVE_BASE_DIR")); dir != "" {
		return dir
	}
	homeDir := ""
	if u, err := user.Current(); err == nil {
		homeDir = u.HomeDir
	}
	if homeDir == "" {
		if d, err := os.UserHomeDir(); err == nil {
			homeDir = d
		}
	}
	if homeDir == "" {
		return ".gobrave"
	}
	return filepath.Join(homeDir, ".gobrave")
}

type TraefikRouteConfig struct {
	Provider      string `yaml:"provider"        json:"provider"`
	BaseURL       string `yaml:"base_url"        json:"base_url"`
	UpsertPath    string `yaml:"upsert_path"     json:"upsert_path"`
	DeletePath    string `yaml:"delete_path"     json:"delete_path"`
	AuthToken     string `yaml:"auth_token"      json:"auth_token"`
	TimeoutSecond int    `yaml:"timeout_second"  json:"timeout_second"`
	FilePath      string `yaml:"file_path"       json:"file_path"`
}

type K8sIngressRouteConfig struct {
	Namespace        string            `yaml:"namespace"          json:"namespace"`
	Kubeconfig       string            `yaml:"kubeconfig"         json:"kubeconfig"`
	InCluster        bool              `yaml:"in_cluster"         json:"in_cluster"`
	IngressClassName string            `yaml:"ingress_class_name" json:"ingress_class_name"`
	Host             string            `yaml:"host"               json:"host"`
	PathType         string            `yaml:"path_type"          json:"path_type"`
	Annotations      map[string]string `yaml:"annotations"        json:"annotations"`
}

// ServerConfig 服务器配置
type ServerConfig struct {
	Port int `yaml:"port"             json:"port"`
	// GRPCPort        int           `yaml:"grpc_port"        json:"grpc_port"`
	Host            string        `yaml:"host"             json:"host"`
	LogPath         string        `yaml:"log_path"         json:"log_path"`
	ShutdownTimeout time.Duration `yaml:"shutdown_timeout" json:"shutdown_timeout" default:"30s"`
}

// DatabaseConfig 数据库配置
type DatabaseConfig struct {
	Driver   string `yaml:"driver"   json:"driver"`
	Host     string `yaml:"host"     json:"host"`
	Port     string `yaml:"port"     json:"port"`
	User     string `yaml:"user"     json:"user"`
	Password string `yaml:"password" json:"password"`
	Name     string `yaml:"name"     json:"name"`
	SSLMode  string `yaml:"ssl_mode" json:"ssl_mode"`
	Path     string `yaml:"path"     json:"path"`
}

type AudioConfig struct {
	Dir string
}

// FeedConfig feed 异步构建配置
type FeedConfig struct {
	WorkerCount      int  `yaml:"worker_count"      json:"worker_count"`
	QueueSize        int  `yaml:"queue_size"        json:"queue_size"`
	BackfillEnabled  bool `yaml:"backfill_enabled"  json:"backfill_enabled"`
	BackfillBatch    int  `yaml:"backfill_batch"    json:"backfill_batch"`
	RetryMaxAttempts int  `yaml:"retry_max_attempts" json:"retry_max_attempts"`
	RetryBaseDelayMs int  `yaml:"retry_base_delay_ms" json:"retry_base_delay_ms"`
	RetryMaxDelayMs  int  `yaml:"retry_max_delay_ms"  json:"retry_max_delay_ms"`
}

// type IngestConfig struct {
// 	Enabled                 bool   `yaml:"enabled" json:"enabled"`
// 	FetchIntervalSec        int    `yaml:"fetch_interval_sec" json:"fetch_interval_sec"`
// 	HTTPTimeoutSec          int    `yaml:"http_timeout_sec" json:"http_timeout_sec"`
// 	FetchWorkers            int    `yaml:"fetch_workers" json:"fetch_workers"`
// 	ParserGRPCAddr          string `yaml:"parser_grpc_addr" json:"parser_grpc_addr"`
// 	ParserGRPCInsecure      *bool  `yaml:"parser_grpc_insecure" json:"parser_grpc_insecure"`
// 	ParserGRPCTimeoutSec    int    `yaml:"parser_grpc_timeout_sec" json:"parser_grpc_timeout_sec"`
// 	ParserDispatchBatchSize int    `yaml:"parser_dispatch_batch_size" json:"parser_dispatch_batch_size"`
// 	ParserCallbackSecret    string `yaml:"parser_callback_secret" json:"parser_callback_secret"`
// }

type TenantConfig struct {
	AesKey string `yaml:"aes_key" json:"aes_key"`
}

func LoadConfig() (*Config, error) {
	cfg := &Config{
		Server: &ServerConfig{
			Port: 8082,
			// GRPCPort:        9092,
			Host:            "0.0.0.0",
			LogPath:         "logs/server.log",
			ShutdownTimeout: 30 * time.Second,
		},
		Database: &DatabaseConfig{
			Driver:  "sqlite",
			Host:    "127.0.0.1",
			Port:    "5432",
			User:    "postgres",
			Name:    "postgres",
			SSLMode: "disable",
			Path:    "",
		},
		Feed: &FeedConfig{
			WorkerCount:      4,
			QueueSize:        2048,
			BackfillEnabled:  false,
			BackfillBatch:    500,
			RetryMaxAttempts: 5,
			RetryBaseDelayMs: 100,
			RetryMaxDelayMs:  5000,
		},
		Proxy: &ProxyConfig{
			BraveAPI:   "http://localhost:5000",
			Container:  "http://localhost:8089",
			OnlyOffice: "http://localhost:8080",
		},
		Route: &RouteConfig{
			Registry:   "gateway",
			AppsPrefix: defaultAppsPrefix,
			Traefik: &TraefikRouteConfig{
				Provider:      "api",
				BaseURL:       "",
				UpsertPath:    "/api/providers/http/routes/{route_key}",
				DeletePath:    "/api/providers/http/routes/{route_key}",
				AuthToken:     "",
				TimeoutSecond: 5,
				FilePath:      "",
			},
			K8sIngress: &K8sIngressRouteConfig{
				Namespace:        "default",
				Kubeconfig:       "",
				InCluster:        false,
				IngressClassName: "",
				Host:             "",
				PathType:         "Prefix",
				Annotations:      map[string]string{},
			},
		},
		Storage: &StorageConfig{
			// ImageDir: "",
			BaseDir: resolveDefaultBaseDir(),
		},
		Realtime: &RealtimeConfig{
			Transport:             "ws",
			MaxConnectionsPerUser: 2,
			AckTimeoutSeconds:     10,
			AckMaxRetries:         3,
		},
		LLM: &LLMConfig{
			CLIURL:      "localhost:4321",
			Model:       "",
			GitHubToken: "",
			Provider: &LLMProviderConfig{
				Type:        "",
				BaseURL:     "",
				APIKey:      "",
				BearerToken: "",
			},
		},
		Container: &ContainerConfig{
			Runtime:                             "docker",
			Kubernetes:                          &KubernetesRuntimeConfig{Namespace: "default"},
			RefreshImageStatusOnStart:           true,
			RecoverRunningDagOnStart:            true,
			CleanupDagNodeContainersBeforeStart: true,
			DeleteContainerOnNodeSuccess:        true,
			DagNodeCleanupOnFailed:              "stop",
			DagNodeCleanupOnDagFinished:         "delete",
			CreateQueueMaxConcurrency:           3,
			CreateQueueMaxPending:               50,
		},
		// Ingest: &IngestConfig{
		// 	Enabled:                 true,
		// 	FetchIntervalSec:        300,
		// 	HTTPTimeoutSec:          15,
		// 	FetchWorkers:            1,
		// 	ParserGRPCAddr:          "127.0.0.1:50051",
		// 	ParserGRPCTimeoutSec:    8,
		// 	ParserDispatchBatchSize: 100,
		// 	ParserCallbackSecret:    "",
		// },
		Tenant: &TenantConfig{
			AesKey: "your-aes-key-here",
		},
		DebugConfig: &DebugConfig{
			EnableDagOrchestrator: false,
		},
		User: &UserConfig{
			DisableRegistration: false,
		},
	}

	// Resolve config file path: CLI --config > BRAVE_CONFIG_DIR > cwd/config.yml
	defaultConfigFile := "config.yml"
	if cliFlags != nil && strings.TrimSpace(cliFlags.ConfigPath) != "" {
		defaultConfigFile = strings.TrimSpace(cliFlags.ConfigPath)
	}
	configPath, err := utils.ResolveExternalPath(defaultConfigFile)
	logger.Infof(context.Background(), "Resolved config.yml path: %s", configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve config path: %w", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			// Apply CLI overrides even when config file is missing
			applyCLIOverrides(cfg)
			return cfg, nil
		}
		return nil, fmt.Errorf("failed to read config file %s: %w", configPath, err)
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file %s: %w", configPath, err)
	}

	// Apply CLI overrides on top of config.yml values
	applyCLIOverrides(cfg)

	if cfg.Storage == nil {
		cfg.Storage = &StorageConfig{BaseDir: resolveDefaultBaseDir()}
	}
	if strings.TrimSpace(cfg.Storage.BaseDir) == "" {
		cfg.Storage.BaseDir = resolveDefaultBaseDir()
	}
	if cfg.Container == nil {
		cfg.Container = &ContainerConfig{
			Runtime:                             "docker",
			Kubernetes:                          &KubernetesRuntimeConfig{Namespace: "default"},
			RefreshImageStatusOnStart:           true,
			RecoverRunningDagOnStart:            true,
			CleanupDagNodeContainersBeforeStart: true,
			DeleteContainerOnNodeSuccess:        false,
			DagNodeCleanupOnFailed:              "stop",
			DagNodeCleanupOnDagFinished:         "delete",
		}
	}
	cfg.Container.Runtime = normalizeContainerRuntime(cfg.Container.Runtime)
	if cfg.Container.Kubernetes == nil {
		cfg.Container.Kubernetes = &KubernetesRuntimeConfig{Namespace: "default"}
	}
	if strings.TrimSpace(cfg.Container.Kubernetes.Namespace) == "" {
		cfg.Container.Kubernetes.Namespace = "default"
	}
	cfg.Container.Kubernetes.Kubeconfig = strings.TrimSpace(cfg.Container.Kubernetes.Kubeconfig)
	if cfg.Realtime == nil {
		cfg.Realtime = &RealtimeConfig{
			Transport:             "ws",
			MaxConnectionsPerUser: 2,
			AckTimeoutSeconds:     10,
			AckMaxRetries:         3,
		}
	}
	if cfg.LLM == nil {
		cfg.LLM = &LLMConfig{CLIURL: "localhost:4321", Provider: &LLMProviderConfig{}}
	}
	if cfg.LLM.Provider == nil {
		cfg.LLM.Provider = &LLMProviderConfig{}
	}
	cfg.LLM.CLIURL = strings.TrimSpace(cfg.LLM.CLIURL)
	if cfg.LLM.CLIURL == "" {
		cfg.LLM.CLIURL = "localhost:4321"
	}
	cfg.LLM.Model = strings.TrimSpace(cfg.LLM.Model)
	cfg.LLM.GitHubToken = strings.TrimSpace(cfg.LLM.GitHubToken)
	cfg.LLM.Provider.Type = strings.TrimSpace(cfg.LLM.Provider.Type)
	cfg.LLM.Provider.BaseURL = strings.TrimSpace(cfg.LLM.Provider.BaseURL)
	cfg.LLM.Provider.APIKey = strings.TrimSpace(cfg.LLM.Provider.APIKey)
	cfg.LLM.Provider.BearerToken = strings.TrimSpace(cfg.LLM.Provider.BearerToken)

	cfg.Realtime.Transport = normalizeRealtimeTransport(cfg.Realtime.Transport)
	if cfg.Realtime.MaxConnectionsPerUser <= 0 {
		cfg.Realtime.MaxConnectionsPerUser = 2
	}
	if cfg.Realtime.AckTimeoutSeconds <= 0 {
		cfg.Realtime.AckTimeoutSeconds = 10
	}
	if cfg.Realtime.AckMaxRetries < 0 {
		cfg.Realtime.AckMaxRetries = 3
	}
	if cfg.DebugConfig == nil {
		cfg.DebugConfig = &DebugConfig{}
	}
	if cfg.User == nil {
		cfg.User = &UserConfig{}
	}

	cfg.Container.DagNodeCleanupOnFailed = normalizeContainerCleanupPolicy(cfg.Container.DagNodeCleanupOnFailed, "stop")
	cfg.Container.DagNodeCleanupOnDagFinished = normalizeContainerCleanupPolicy(cfg.Container.DagNodeCleanupOnDagFinished, "delete")

	if cfg.Route == nil {
		cfg.Route = &RouteConfig{Registry: "gateway"}
	}
	if strings.TrimSpace(cfg.Route.Registry) == "" {
		cfg.Route.Registry = "gateway"
	}
	cfg.Route.AppsPrefix = normalizePathPrefix(cfg.Route.AppsPrefix, defaultAppsPrefix)
	if cfg.Route.Traefik == nil {
		cfg.Route.Traefik = &TraefikRouteConfig{}
	}
	if strings.TrimSpace(cfg.Route.Traefik.Provider) == "" {
		cfg.Route.Traefik.Provider = "api"
	}
	if cfg.Route.Traefik.TimeoutSecond <= 0 {
		cfg.Route.Traefik.TimeoutSecond = 5
	}
	if strings.TrimSpace(cfg.Route.Traefik.UpsertPath) == "" {
		cfg.Route.Traefik.UpsertPath = "/api/providers/http/routes/{route_key}"
	}
	if strings.TrimSpace(cfg.Route.Traefik.DeletePath) == "" {
		cfg.Route.Traefik.DeletePath = "/api/providers/http/routes/{route_key}"
	}
	if cfg.Route.K8sIngress == nil {
		cfg.Route.K8sIngress = &K8sIngressRouteConfig{}
	}
	if strings.TrimSpace(cfg.Route.K8sIngress.Namespace) == "" {
		cfg.Route.K8sIngress.Namespace = "default"
	}
	cfg.Route.K8sIngress.Kubeconfig = strings.TrimSpace(cfg.Route.K8sIngress.Kubeconfig)
	cfg.Route.K8sIngress.IngressClassName = strings.TrimSpace(cfg.Route.K8sIngress.IngressClassName)
	cfg.Route.K8sIngress.Host = strings.TrimSpace(cfg.Route.K8sIngress.Host)
	cfg.Route.K8sIngress.PathType = normalizeIngressPathType(cfg.Route.K8sIngress.PathType)
	if cfg.Route.K8sIngress.Annotations == nil {
		cfg.Route.K8sIngress.Annotations = map[string]string{}
	}

	TENANT_AES_KEY := cfg.Tenant.AesKey
	os.Setenv("TENANT_AES_KEY", TENANT_AES_KEY)

	return cfg, nil
}

func ResolveAppsPathPrefix(cfg *Config) string {
	if cfg == nil || cfg.Route == nil {
		return defaultAppsPrefix
	}
	return normalizePathPrefix(cfg.Route.AppsPrefix, defaultAppsPrefix)
}

func ResolveContainerRuntime(cfg *Config) string {
	if cfg == nil || cfg.Container == nil {
		return "docker"
	}
	return normalizeContainerRuntime(cfg.Container.Runtime)
}

func normalizePathPrefix(value, fallback string) string {
	prefix := strings.TrimSpace(value)
	if prefix == "" {
		prefix = fallback
	}
	if !strings.HasPrefix(prefix, "/") {
		prefix = "/" + prefix
	}
	prefix = strings.TrimRight(prefix, "/")
	if prefix == "" {
		return fallback
	}
	return prefix
}

func normalizeContainerCleanupPolicy(value string, fallback string) string {
	v := strings.TrimSpace(strings.ToLower(value))
	switch v {
	case "none", "stop", "delete":
		return v
	default:
		return fallback
	}
}

func normalizeContainerRuntime(value string) string {
	v := strings.TrimSpace(strings.ToLower(value))
	switch v {
	case "", "docker":
		return "docker"
	case "k8s", "kubernetes":
		return "k8s"
	case "k3s":
		return "k3s"
	default:
		return "docker"
	}
}

func normalizeIngressPathType(value string) string {
	v := strings.TrimSpace(strings.ToLower(value))
	switch v {
	case "exact":
		return "Exact"
	case "prefix", "", "implementation-specific", "implementationspecific":
		if v == "implementation-specific" || v == "implementationspecific" {
			return "ImplementationSpecific"
		}
		return "Prefix"
	default:
		return "Prefix"
	}
}

func normalizeRealtimeTransport(value string) string {
	v := strings.TrimSpace(strings.ToLower(value))
	switch v {
	case "ws", "sse":
		return v
	default:
		return "ws"
	}
}

// applyCLIOverrides applies command-line flag values on top of the config.
// Only non-zero / non-empty values from CLI flags override the config.
func applyCLIOverrides(cfg *Config) {
	if cliFlags == nil {
		return
	}
	f := cliFlags

	// Server
	if f.Port != 0 {
		cfg.Server.Port = f.Port
	}
	if f.Host != "" {
		cfg.Server.Host = f.Host
	}
	if f.LogPath != "" {
		cfg.Server.LogPath = f.LogPath
	}

	// Database
	if cfg.Database == nil {
		cfg.Database = &DatabaseConfig{}
	}
	if f.DBDriver != "" {
		cfg.Database.Driver = f.DBDriver
	}
	if f.DBHost != "" {
		cfg.Database.Host = f.DBHost
	}
	if f.DBPort != "" {
		cfg.Database.Port = f.DBPort
	}
	if f.DBUser != "" {
		cfg.Database.User = f.DBUser
	}
	if f.DBPassword != "" {
		cfg.Database.Password = f.DBPassword
	}
	if f.DBName != "" {
		cfg.Database.Name = f.DBName
	}
	if f.DBPath != "" {
		cfg.Database.Path = f.DBPath
	}
	if f.DBSSLMode != "" {
		cfg.Database.SSLMode = f.DBSSLMode
	}

	// Container runtime
	if f.Runtime != "" {
		if cfg.Container == nil {
			cfg.Container = &ContainerConfig{}
		}
		cfg.Container.Runtime = f.Runtime
	}

	// Storage
	if f.BaseDir != "" {
		if cfg.Storage == nil {
			cfg.Storage = &StorageConfig{}
		}
		cfg.Storage.BaseDir = f.BaseDir
	}

	// Debug / User
	if v, ok := f.DisableRegistrationSet(); ok {
		if cfg.User == nil {
			cfg.User = &UserConfig{}
		}
		cfg.User.DisableRegistration = v
	}
}
