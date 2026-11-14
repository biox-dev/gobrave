// Package windows 为桌面端提供「多窗口」能力。
//
// 设计要点：
//
//   - 前端统一调用 DesktopService.OpenWindow(rawURL)，由本包决定「开新窗口 / 复用窗口 / 交给系统浏览器」；
//   - 窗口按 **规范化后的绝对 URL** 去重：同一条 URL 再次打开只做 Show + Focus，
//     不会重复创建（例如反复点同一个应用的 Open）；
//   - 窗口关闭（WindowClosing）时从表中移除，避免拿到已销毁的窗口句柄；
//   - 非本应用同源（外部 http/https）的 URL 交给系统默认浏览器，避免把第三方页面塞进 WebView。
//
// 之所以要「规范化」而不是直接用原始字符串：前端可能传相对路径（/c/analysis/apps/123/），
// 也可能是绝对地址；两者指向同一个页面时必须命中同一个窗口。
package windows

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync"

	"github.com/biox-dev/gobrave/internal/logger"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

// shellOrigin 是本应用的「外壳」地址（WebView 加载的本地 http://127.0.0.1:<port>）。
//
// 应用会话（path_prefix 形如 /c/analysis/apps/<id>）挂在同一个 origin 上，
// 由外壳的本地 http.Server 反代到内嵌后端，因此同源判断用 Origin 即可。
type shellOrigin struct {
	Scheme string
	Host   string
}

// Options 描述窗口管理器依赖的外部参数。
type Options struct {
	// App 是 Wails 应用实例，用于创建 WebviewWindow / 打开系统浏览器。
	App *application.App
	// ShellURL 是主窗口加载的地址（pageURL），用于解析相对路径与判定同源。
	ShellURL string
	// TitlePrefix 是新窗口标题前缀，例如 "Brave"。
	TitlePrefix string
	// MarkerJS 是页面加载后注入的脚本，用于让前端识别「运行在桌面壳里」。
	// 必须与主窗口使用同一段脚本（见 main.go 的 desktopMarkerJS）。
	MarkerJS string
	// DefaultWidth / DefaultHeight 是未命中规则时的窗口尺寸。
	DefaultWidth  int
	DefaultHeight int
}

// 不同页面类型的推荐窗口尺寸。key 是 URL 路径前缀（相对应用根）。
var windowProfiles = []struct {
	Prefix string
	Title  string
	Width  int
	Height int
}{
	// Jupyter / Notebook 这类需要大画布的应用占满大屏。
	{Prefix: "/c/analysis/apps/", Title: "App Session", Width: 1440, Height: 900},
	{Prefix: "/apps/", Title: "App Session", Width: 1440, Height: 900},
}

// minWindowWidth / minWindowHeight 避免小屏下窗口尺寸被压得过小。
const (
	minWindowWidth  = 640
	minWindowHeight = 480
)

// Manager 管理桌面端创建的所有原生窗口。
//
// 所有导出方法都可被前端（Wails binding）并发调用，内部用互斥锁串行化。
type Manager struct {
	app         *application.App
	titlePrefix string
	markerJS    string
	origin      shellOrigin
	width       int
	height      int

	mu sync.Mutex
	// windows 以「规范化 URL」为 key，记录仍然存活的窗口。
	windows map[string]application.Window
}

// NewManager 创建窗口管理器。
func NewManager(opts Options) *Manager {
	prefix := strings.TrimSpace(opts.TitlePrefix)
	if prefix == "" {
		prefix = "Brave"
	}

	width, height := opts.DefaultWidth, opts.DefaultHeight
	if width < minWindowWidth {
		width = 1200
	}
	if height < minWindowHeight {
		height = 800
	}

	return &Manager{
		app:         opts.App,
		titlePrefix: prefix,
		markerJS:    opts.MarkerJS,
		origin:      parseOrigin(opts.ShellURL),
		width:       width,
		height:      height,
		windows:     make(map[string]application.Window),
	}
}

// OpenResult 是 Open 的返回值，便于前端判断实际发生了什么（便于调试 / 埋点）。
type OpenResult struct {
	// URL 是最终使用的绝对地址。
	URL string `json:"url"`
	// Action 取值：created / focused / external。
	Action string `json:"action"`
	// Name 是窗口名称（external 时为空）。
	Name string `json:"name"`
}

// Open 打开 rawURL：本应用页面开原生窗口，外部地址交给系统浏览器。
//
// 返回的 error 仅表示「参数非法」或「Wails 创建窗口失败」；
// 复用已有窗口属于正常路径。
func (m *Manager) Open(rawURL string) (OpenResult, error) {
	if m == nil || m.app == nil {
		return OpenResult{}, fmt.Errorf("窗口管理器未初始化")
	}

	abs, err := m.resolve(rawURL)
	if err != nil {
		return OpenResult{}, err
	}

	// 外部链接（不同 host / 不同 scheme）：交给系统默认浏览器。
	if !m.isInternal(abs) {
		if err := m.app.Browser.OpenURL(abs.String()); err != nil {
			return OpenResult{URL: abs.String(), Action: "external"}, err
		}
		return OpenResult{URL: abs.String(), Action: "external"}, nil
	}

	key := abs.String()
	title, width, height := m.profileFor(abs)

	m.mu.Lock()
	defer m.mu.Unlock()

	if win, ok := m.windows[key]; ok {
		// 已存在：还原（最小化 / 隐藏）后置顶，不重复创建。
		if win.IsMinimised() {
			win.UnMinimise()
		}
		win.Show()
		win.Focus()
		return OpenResult{URL: key, Action: "focused", Name: win.Name()}, nil
	}

	name := fmt.Sprintf("app-window-%d", len(m.windows)+1)
	win := m.app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:            name,
		Title:           title,
		URL:             key,
		Width:           width,
		Height:          height,
		MinWidth:        minWindowWidth,
		MinHeight:       minWindowHeight,
		InitialPosition: application.WindowCentered,
		Mac: application.MacWindow{
			InvisibleTitleBarHeight: 40,
			Backdrop:                application.MacBackdropTranslucent,
			TitleBar:                application.MacTitleBarHiddenInset,
		},
		BackgroundColour: application.NewRGB(6, 7, 15),
		JS:               m.markerJS,
	})
	if win == nil {
		return OpenResult{}, fmt.Errorf("创建窗口失败: %s", key)
	}

	m.windows[key] = win

	// 窗口关闭后移除句柄：否则后续 Open 会 focus 一个已销毁的窗口。
	win.OnWindowEvent(events.Common.WindowClosing, func(_ *application.WindowEvent) {
		m.forget(key)
	})

	return OpenResult{URL: key, Action: "created", Name: name}, nil
}

// Focus 激活指定 URL 对应的窗口（不存在时返回 found=false）。
func (m *Manager) Focus(rawURL string) (bool, error) {
	abs, err := m.resolve(rawURL)
	if err != nil {
		return false, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	win, ok := m.windows[abs.String()]
	if !ok {
		return false, nil
	}
	if win.IsMinimised() {
		win.UnMinimise()
	}
	win.Show()
	win.Focus()
	return true, nil
}

// Close 关闭指定 URL 对应的窗口（不存在时忽略）。
func (m *Manager) Close(rawURL string) error {
	abs, err := m.resolve(rawURL)
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	win, ok := m.windows[abs.String()]
	if !ok {
		return nil
	}
	win.Close()
	delete(m.windows, abs.String())
	return nil
}

// List 返回当前打开的应用窗口（规范化 URL）列表，便于前端做调试视图。
func (m *Manager) List() []string {
	m.mu.Lock()
	defer m.mu.Unlock()

	result := make([]string, 0, len(m.windows))
	for key := range m.windows {
		result = append(result, key)
	}
	return result
}

// OpenExternal 强制用系统默认浏览器打开（即使同源）。
func (m *Manager) OpenExternal(rawURL string) error {
	abs, err := m.resolve(rawURL)
	if err != nil {
		return err
	}
	return m.app.Browser.OpenURL(abs.String())
}

// forget 由一个 URL 的窗口关闭时调用，删除映射。
func (m *Manager) forget(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.windows, key)
}

// resolve 把前端传入的 URL 规范化为绝对地址。
//
// 规则：
//   - "http(s)://..." 原样解析；
//   - "/xxx" 或 "xxx" 视为外壳 origin 下的相对路径；
//   - 其它 scheme（如 mailto:）原样返回，后续按外部链接处理。
func (m *Manager) resolve(rawURL string) (*url.URL, error) {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return nil, fmt.Errorf("URL 不能为空")
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return nil, fmt.Errorf("解析 URL 失败: %w", err)
	}
	if parsed.Scheme != "" {
		return parsed, nil
	}

	// 相对路径：拼到外壳 origin 上。
	var base url.URL
	base.Scheme = m.origin.Scheme
	base.Host = m.origin.Host
	if strings.HasPrefix(trimmed, "/") {
		return base.ResolveReference(parsed), nil
	}
	base.Path = "/"
	return base.ResolveReference(parsed), nil
}

// isInternal 判断 URL 是否属于本应用（同 scheme + 同 host）。
func (m *Manager) isInternal(u *url.URL) bool {
	if m.origin.Scheme == "" || m.origin.Host == "" {
		// 无法判断时保守处理：当作外部链接，避免把任意页面塞进 WebView。
		return false
	}
	return strings.EqualFold(u.Scheme, m.origin.Scheme) &&
		strings.EqualFold(u.Host, m.origin.Host)
}

// profileFor 按路径前缀挑选窗口标题与尺寸。
func (m *Manager) profileFor(u *url.URL) (title string, width int, height int) {
	title, width, height = m.titlePrefix, m.width, m.height
	for _, profile := range windowProfiles {
		if strings.HasPrefix(u.Path, profile.Prefix) {
			return profile.Title, profile.Width, profile.Height
		}
	}
	return title, width, height
}

// parseOrigin 从外壳 URL 中提取 scheme / host。
func parseOrigin(raw string) shellOrigin {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		logger.Warnf(context.Background(), "[windows] 无法解析外壳地址 %q: %v", raw, err)
		return shellOrigin{}
	}
	scheme := u.Scheme
	if scheme == "" {
		scheme = "http"
	}
	return shellOrigin{Scheme: scheme, Host: u.Host}
}
