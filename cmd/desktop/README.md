# cmd/desktop — Brave Wails 桌面壳

用 Wails v3 打开桌面窗口，复用 gobrave 现有前端 `frontend/web`，不复制前端源码。
**后端以 Go 类库方式在同一进程内启动**（`internal/appsrv`），无需另起进程。

## 结构

```
cmd/desktop/
├── main.go           //go:build gtk3 —— 入口（窗口 + 内嵌后端），必须带 gtk3 标签编译
├── assets.go         前端资源解析（内嵌 frontend/web > 可执行文件旁 > 工作目录）
├── build.sh          构建脚本（同步前端 + gtk3/embed_frontend 编译）
└── backend/
    ├── config.go     后端地址：BRAVE_DESKTOP_BACKEND_URL/HOST/PORT，默认 127.0.0.1:8084
    ├── manager.go    /health 探针 + SetBaseURL（回填实际监听端口）
    ├── proxy.go      静态资源 + 后端路径反向代理 + SPA 回退
    └── server.go     WebView 用的 127.0.0.1:0 真实 http.Server
```

> `main.go` 带 `//go:build gtk3`，因此该目录**只在带 gtk3 标签时才参与编译**。
> 不带标签时 `cmd/desktop` 是空包，`go build ./...` / `go vet ./...` 会报
> `function main is undeclared in the main package`——这是预期行为，详见下文「构建标签」。

## 启动流程（main.go）

1. `config.LoadConfigWith(LoadOptions{Desktop: true})` → `appsrv.New(Options{Config: cfg, Address: "127.0.0.1:<port>", InitSnowflake: true})`
   → 构建 DI 容器（`container.BuildContainer`）。
   - `Desktop: true` 在 Windows 上把 `container.docker.provision.enabled` 默认设为
     `true`、`container.network_mode` 默认设为 `host_port`；
     `config.yml` 中显式声明的键仍然优先生效。
2. `srv.Start()` → `cmd.ListenWithRetry` 监听 → 后台 `http.Serve`。
   - `-port 0` 时用随机空闲端口，实际地址由 `srv.Address()` 回读。
3. `backend.Manager.SetBaseURL("http://"+srv.Address())` 让反代/探针指向真实地址。
4. `backend.StartLocalServer(NewHandler(...))` 起页面服务（WebView 加载它）。
5. `app.Run()`；退出时 `defer` 先关页面服务、再 `srv.Shutdown()` 停后端。

## 为什么用本地 http.Server 而不是 Wails Assets

WebKitGTK 的 URI-scheme handler 不传递 POST 请求体，`wails://` 下所有 POST 到达
handler 时 `ContentLength=0`，服务端 `ShouldBindJSON` 读到 EOF。
因此窗口加载 `http://127.0.0.1:<随机端口>/`，POST/PUT body、SSE、WebSocket 与浏览器一致。

## 构建 / 运行

```bash
# 前端未构建时先跑一次（会用 ../brave-ui 重新构建并同步到 frontend/web）
./cmd/desktop/build.sh --sync

# 常规构建（内嵌 frontend/web 产物）
./cmd/desktop/build.sh

# 运行：后端随之在同一进程启动，无需再开 go run .
./bin/brave-desktop            # 默认 127.0.0.1:8084
./bin/brave-desktop -port 0    # 随机空闲端口（不占 8084）
./bin/brave-desktop -port 18084

# 或者直接用 go run（等价于 build.sh 的编译参数）
go run -tags "gtk3 embed_frontend" ./cmd/desktop

# 开发模式：前端 HMR，仅跳过本地静态服务（后端仍内嵌启动）
./bin/brave-desktop -dev-url http://localhost:5173
```

## 构建标签（重要）

### 什么时候必须加 `gtk3`

**编译或运行 `cmd/desktop` 时必须加 `-tags gtk3`**，否则一定失败。原因：

Wails v3 的 Linux 后端有两套实现，用 `gtk3` 标签切换：

| 标签 | Wails 生效的文件 | 需要的 GTK |
|---|---|---|
| 不带 `gtk3`（默认） | `application_linux.go`、`linux_cgo.go` | **GTK4 ≥ 4.10** |
| 带 `gtk3` | `application_linux_gtk3.go`、`linux_cgo_gtk3.go` | **GTK3** |

本机实测版本：

```
pkg-config --modversion gtk4       → 4.6.9    ← 有，但 < 4.10
pkg-config --modversion gtk+-3.0   → 3.24.33  ← 足够
```

不带标签时会走 GTK4 分支，而 4.6.9 缺少 `GtkFileDialog`（GTK 4.10 才引入）等新 API，
编译报错：

```
linux_cgo.h:177:1: error: unknown type name 'GtkFileDialog'
# github.com/wailsapp/wails/v3/pkg/application
linux_cgo.go:1975:5: could not determine what C.add_file_filter refers to
```

### 各条命令的标签要求

| 命令 | 是否需要 `gtk3` | 说明 |
|---|---|---|
| `go build .` / `go run .`（gobrave 主服务） | ❌ 不需要 | 与 desktop 完全无关，任何情况下都不受影响 |
| `go build ./internal/...`、`go test ./internal/...` | ❌ 不需要 | 不涉及 Wails |
| `go build -tags gtk3 ./cmd/desktop` | ✅ **必须** | 编译桌面端 |
| `go run -tags "gtk3 embed_frontend" ./cmd/desktop` | ✅ **必须** | 运行桌面端 |
| `go build ./...` | ❌ 会失败 | `cmd/desktop` 在不带标签时是空包 → `function main is undeclared` |
| `go vet ./...` | ❌ 会失败 | 同上 |

**结论**：`./...` 这类递归命令不适用于本仓库的桌面端；请显式指定包并带上标签：

```bash
# 后端相关（不带标签）
go build .
go test ./internal/... ./cmd/server/...

# 桌面端（必须带标签）
go build -tags "gtk3 embed_frontend" ./cmd/desktop
# 或直接用脚本（推荐）
./cmd/desktop/build.sh
```

### 其他标签

- `-tags embed_frontend`：可选，决定前端资源是否内嵌进二进制
  （不带时回退到磁盘上的 `frontend/web`）。

### 什么时候可以去掉 `gtk3`

升级宿主 GTK4 到 **≥ 4.10** 后，可以去掉 `main.go` 的 `//go:build gtk3` 与所有命令的
`-tags gtk3`，此时 Wails 会走默认的 GTK4 后端。在那之前必须保留。

## 代理路径

`backend.ProxyPrefixes` 与 `internal/router/router.go` 中 `mountFrontendStatic`
的静态白名单保持一致（`/api` `/health` `/swagger` `/docs` `/audio` `/images`
`/images-data` `/brave-api` `/container` `/apps` `/onlyoffice` `/go-onlyoffice`
`/data-analysis` `/data-project` `/c/analysis/apps`）。新增后端前缀时两边都要改。

## 命令行入口也复用了 appsrv

`main.go` 现在只做 `appsrv.ParseCLIFlags()` + `appsrv.New/Start/Wait`，
与桌面端共用同一套启动逻辑（`HandleSignals: true` 时自行处理 SIGINT/SIGTERM）。
