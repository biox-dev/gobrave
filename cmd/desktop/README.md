# cmd/desktop — Brave Wails 桌面壳

用 Wails v3 打开桌面窗口，复用 gobrave 现有前端 `frontend/web`，不复制前端源码。

## 结构

```
cmd/desktop/
├── main.go           入口：启动本地 HTTP 服务 + 打开窗口 + DesktopService
├── assets.go         前端资源解析（embed_frontend 内嵌 > 磁盘 web/ 或 frontend/web）
├── build.sh          构建脚本（同步前端 + gtk3/embed_frontend 编译）
└── backend/
    ├── config.go     后端地址：BRAVE_DESKTOP_BACKEND_URL/HOST/PORT，默认 127.0.0.1:8084
    ├── manager.go    /health 探针，后续换成「库内启动后端」的落点
    ├── proxy.go      静态资源 + 后端路径反向代理 + SPA 回退
    └── server.go     127.0.0.1:0 真实 http.Server（供 WebView 加载）
```

前端资源**只有一处** `gobrave/frontend/web`（与 Web 版同一份产物，不复制副本）。
内嵌能力直接复用 `gobrave/frontend` 包的 `EmbeddedFrontendFS()`
（其 `frontend_assets_embed.go` / `frontend_assets_none.go` 按 `embed_frontend`
tag 自动切换返回内嵌 FS 或 nil）；`assets.go` 只额外做一次 `fs.Sub(root, "web")`
把 `web/` 前缀下移，使 handler 看到的根目录直接是 `index.html`。

未带 `embed_frontend` 时从磁盘 `web/` 或 `frontend/web/` 读取，
部署时可替换前端而不重编二进制。

## 为什么用本地 http.Server 而不是 Wails Assets

WebKitGTK 的 URI-scheme handler 不传递 POST 请求体，`wails://` 下所有 POST 到达
handler 时 `ContentLength=0`，服务端 `ShouldBindJSON` 读到 EOF。
因此改为 `127.0.0.1:0` 起真实 HTTP 服务，窗口 `URL` 指向它，
POST/PUT body、SSE、WebSocket 与浏览器行为一致。

## 构建 / 运行

```bash
# 前端未构建时先跑一次（会用 ../brave-ui 重新构建并同步到 frontend/web）
./cmd/desktop/build.sh --sync

# 常规构建（内嵌 frontend/web 产物）
./cmd/desktop/build.sh

# 运行（后端需另行启动：go run . 监听 :8084）
./bin/brave-desktop

# 开发模式：直接加载 vite dev server
./bin/brave-desktop -dev-url http://localhost:5173
```
必须带 `gtk3` 标签：宿主 GTK 为 4.6.9，Wails v3 默认 GTK4 后端要求 >= 4.10。

## 代理路径

`backend.ProxyPrefixes` 与 `internal/router/router.go` 中 `mountFrontendStatic`
的静态白名单保持一致（`/api` `/health` `/swagger` `/docs` `/audio` `/images`
`/images-data` `/brave-api` `/container` `/apps` `/onlyoffice` `/go-onlyoffice`
`/data-analysis` `/data-project` `/c/analysis/apps`）。新增后端前缀时两边都要改。

## 后续（待实现）

在 `backend.Manager` 中增加 `Start/Stop`，以 golang 类库方式在进程内启动后端
（替代当前「连接已运行后端」的阶段 1 模式）。窗口与前端代码无需改动。
