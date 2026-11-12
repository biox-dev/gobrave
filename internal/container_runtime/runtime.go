package containerruntime

import (
	"context"
	"strings"

	"github.com/biox-dev/gobrave/internal/types"
)

type Runtime interface {
	Name() string

	Create(ctx context.Context, spec *types.ContainerSpec) (string, error)

	Start(ctx context.Context, runtimeID string) error

	Stop(ctx context.Context, runtimeID string) error

	Pause(ctx context.Context, runtimeID string) error

	Resume(ctx context.Context, runtimeID string) error

	Delete(ctx context.Context, runtimeID string) error

	Logs(ctx context.Context, runtimeID string, tail int) (string, error)

	SetEventHandler(handler RuntimeEventHandler)

	Exec(ctx context.Context, runtimeID string, cmd []string) (string, error)
}

// RuntimeMonitor is an optional extension interface for reconnecting runtime
// lifecycle monitoring after process restarts.
type RuntimeMonitor interface {
	Monitor(ctx context.Context, runtimeID string) error
}

// RuntimeImageManager is an optional extension interface for runtime-side
// image lifecycle operations used by ImageManager.
type RuntimeImageManager interface {
	EnsureImage(ctx context.Context, image string, pullPolicy string) error
}

// RuntimeInspection carries runtime-specific inspect data used by manager/service.
type RuntimeInspection struct {
	IPAddress string
	NodeName  string
}

// RuntimeInspector is an optional extension interface. Implementations can expose
// inspect metadata (such as container internal IP) without forcing all runtimes.
type RuntimeInspector interface {
	Inspect(ctx context.Context, runtimeID string) (*RuntimeInspection, error)
}

// RuntimeDescription 描述一个运行时的详情信息（对应 docker inspect / kubectl describe）。
type RuntimeDescription struct {
	// Kind 标识资源类型，例如 container / deployment / job / service。
	Kind string `json:"kind"`
	// Name 资源名称（docker 容器名，或 k8s workload/service 名）。
	Name string `json:"name"`
	// Format 描述 Raw 的格式，便于前端渲染，例如 json / yaml / text。
	Format string `json:"format"`
	// Raw 原始 inspect/describe 输出，保持后端运行时的原生格式。
	Raw string `json:"raw"`
}

// RuntimeDescriber 是可选扩展接口。实现该接口的运行时可通过 runtimeID 返回
// 资源的详情（docker inspect / kubectl describe）。
type RuntimeDescriber interface {
	Describe(ctx context.Context, runtimeID string) (*RuntimeDescription, error)
}

// RuntimeHealthChecker 是可选扩展接口：运行时探测自身依赖（docker daemon / k8s apiserver）
// 是否可用，供启动自检与配置检测接口使用。未实现该接口的运行时按「已注册即正常」处理。
type RuntimeHealthChecker interface {
	// HealthCheck 探测运行时依赖是否可用；返回 error 表示不可用（包含具体原因）。
	// ctx 由调用方设置超时，实现应尽量快速返回。
	HealthCheck(ctx context.Context) error
}

// RuntimeEndpointReporter 是可选扩展接口：运行时报告它实际连接到哪个 daemon / apiserver。
// 返回的地址用于前端展示，实现方需自行脱敏（见 MaskEndpoint）。
type RuntimeEndpointReporter interface {
	// Endpoint 返回（脱敏后的）连接地址及地址来源，例如 unix:///var/run/docker.sock + env。
	Endpoint() (address string, source string)
}

// RuntimeVersionReporter 是可选扩展接口：报告对端（docker daemon / apiserver）版本号。
type RuntimeVersionReporter interface {
	ServerVersion(ctx context.Context) (string, error)
}

// RuntimeHealth 描述运行时注册 / 可用性与宿主环境信息。
type RuntimeHealth struct {
	// Name 运行时名称（docker / k8s / k3s）。
	Name string `json:"name"`
	// Registered 是否在运行时注册表中（即启动装配阶段是否成功注册）。
	Registered bool `json:"registered"`
	// Healthy 依赖探测是否通过；未注册时必然为 false。
	Healthy bool `json:"healthy"`
	// Checkable 运行时是否实现了 RuntimeHealthChecker（false 表示只做了注册检查）。
	Checkable bool `json:"checkable"`
	// Error 检测失败原因，正常时为空。
	Error string `json:"error,omitempty"`

	// HostOS 后端进程所在宿主操作系统（runtime.GOOS：linux / windows / darwin）。
	HostOS string `json:"host_os"`
	// HostArch 后端进程所在宿主 CPU 架构（runtime.GOARCH）。
	HostArch string `json:"host_arch"`
	// Endpoint 运行时实际连接地址（已脱敏，密码以 *** 代替）。
	Endpoint string `json:"endpoint,omitempty"`
	// EndpointSource 地址来源：explicit / env / default / kubeconfig / in-cluster。
	EndpointSource string `json:"endpoint_source,omitempty"`
	// ServerVersion 对端版本号（docker daemon / kubernetes apiserver）。
	ServerVersion string `json:"server_version,omitempty"`

	// ProvisionEnabled 是否启用运行时预置（container.docker.provision.enabled）。
	// 为 true 时前端才展示「容器运行时安装向导」入口。
	ProvisionEnabled bool `json:"provision_enabled"`
	// BashExecutionMode bash 工具执行位置：host（宿主机）/ container（容器）。
	BashExecutionMode string `json:"bash_execution_mode"`
	// BashContainerTemplateID container 模式下 bash 工具使用的容器模板 ID。
	BashContainerTemplateID int64 `json:"bash_container_template_id,string"`
}

// MaskEndpoint 把地址里的 user:password@ 部分替换成 user:***@，避免凭据泄露到前端。
func MaskEndpoint(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	idx := strings.Index(raw, "://")
	if idx < 0 {
		return raw
	}
	scheme, rest := raw[:idx+3], raw[idx+3:]
	at := strings.LastIndex(rest, "@")
	if at < 0 {
		return raw
	}
	userInfo := rest[:at]
	if colon := strings.Index(userInfo, ":"); colon >= 0 {
		userInfo = userInfo[:colon] + ":***"
	}
	return scheme + userInfo + rest[at:]
}

type RuntimeEvent struct {
	Type      string
	RuntimeID string
	Message   string
}

type RuntimeEventHandler interface {
	OnEvent(event RuntimeEvent)
}
