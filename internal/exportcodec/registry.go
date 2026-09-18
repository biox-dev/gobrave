package exportcodec

import (
	"errors"
	"sort"
	"strings"
)

// ErrUnsupportedVersion 表示导出文件（script.json / workflow.json）顶层的 version
// 没有注册对应的 Codec，调用方（InstallScript/InstallWorkflow）应转成 400 而不是 500。
var ErrUnsupportedVersion = errors.New("unsupported export file version")

// Registry 是版本号到 Codec 的注册表，风格与 internal/container_runtime/factory.go 的
// Registry 保持一致（NewRegistry / Register / Get / List）。
//
// 与 container_runtime.Registry 的区别只有键的含义：那里的键是 runtime 名字（docker/k8s），
// 这里的键是导出文件格式版本（codec.Version()，如 "v1"）。
//
// 生命周期：由 DI 容器在启动期装配（internal/container/container.go），
// 之后只被读取，因此自身不加锁；Codec 实现必须无状态、并发安全。
type Registry struct {
	codecs map[string]Codec
}

// NewRegistry 创建空注册表。
func NewRegistry() *Registry {
	return &Registry{
		codecs: map[string]Codec{},
	}
}

// Register 按 codec.Version() 注册。
//
// 只有启动期装配会调用；同版本重复注册以最后一次为准（便于测试里替换实现）。
func (r *Registry) Register(codec Codec) {
	if r == nil || codec == nil {
		return
	}
	version := strings.TrimSpace(codec.Version())
	if version == "" {
		return
	}
	r.codecs[version] = codec
}

// Get 按版本号取 Codec，未注册时返回 nil（与 container_runtime.Registry.Get 一致：
// 由调用方判空决定如何报错，例如转 400）。nil receiver 安全。
func (r *Registry) Get(version string) Codec {
	if r == nil {
		return nil
	}
	return r.codecs[strings.TrimSpace(version)]
}

// List 返回已注册的全部 Codec（顺序不定），便于启动日志与装配自检。
func (r *Registry) List() []Codec {
	if r == nil {
		return nil
	}
	items := make([]Codec, 0, len(r.codecs))
	for _, codec := range r.codecs {
		items = append(items, codec)
	}
	return items
}

// Versions 返回已注册的版本号（已排序），便于启动日志与测试断言「版本 X 已装配」。
func (r *Registry) Versions() []string {
	if r == nil {
		return nil
	}
	versions := make([]string, 0, len(r.codecs))
	for version := range r.codecs {
		versions = append(versions, version)
	}
	sort.Strings(versions)
	return versions
}
