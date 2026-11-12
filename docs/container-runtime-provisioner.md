# 容器运行时预置（Provisioner）设计

## 1. 目标

gobrave 在 Windows 上需要一个可用的 docker daemon。目标形态是：**不依赖 Docker
Desktop**，由 gobrave 自己在 `storage.base_dir/wsl` 下导入并管理一个预置好
dockerd 的 Ubuntu WSL 发行版。

同时要求**启动路径与安装路径解耦**：

| 场景 | 行为 |
|---|---|
| 每次启动（desktop / CLI） | 调用一次 `Start`：**已安装**则确保 WSL 与 dockerd 已启动并返回 endpoint；**未安装**则直接返回 `ErrNotInstalled`，不做任何副作用 |
| 安装向导（UI 触发） | 依次调用 `Status` → `Download` → `Install` → `Start`，返回进度与 endpoint |
| 卸载向导（UI 触发） | 调用 `Uninstall`，注销发行版并删除 `wsl` 目录 |

“已安装”是一个**可判定的事实**，不是猜测：`storage.base_dir/wsl/<distro>` 目录
存在且其中 `rootfs.tar` 校验通过、`wsl -l -q` 中能看到该发行版。

## 2. 目录布局

`storage.base_dir` 在 Windows 上形如 `D:\workspace\gobrave\.gobrave`。
发行版相关文件全部收敛在 `<base_dir>/wsl` 下：

```
<base_dir>/wsl/
├── distro/                       # 发行版数据目录（wsl --import 的 --location）
│   ├── ext4.vhdx
│   └── ...
├── cache/
│   ├── gobrave-ubuntu-docker-amd64.tar      # 下载产物（rootfs 包）
│   └── gobrave-ubuntu-docker-amd64.tar.part # 下载中临时文件
├── state.json                    # 安装状态（版本 / 发行版名 / 安装时间 / 端口）
├── dockerd.log                   # dockerd 运行日志（最近若干 MB）
└── dockerd.pid                   # dockerd 进程号（用于 Stop / 存活判断）
```

**为什么 `distro/` 与 `cache/` 分开**：`wsl --unregister` 只删注册信息，
`distro/` 需要显式删除；而下载的 tar 包可缓存复用，重装时不必重新下载。

## 3. 包结构

```
internal/container_runtime/provision/
├── types/types.go          # 共享类型与接口（State/RuntimeStatus/Progress/Provisioner/Config）
├── provisioner.go          # 类型别名再导出，调用方只 import provision 一个包
├── platform_windows.go     # //go:build windows：构造 WSL 实现
├── platform_unix.go        # //go:build !windows：构造本机 daemon 实现
├── manager.go              # Manager：Start 幂等包装 + endpoint 缓存
├── local.go                # Linux/macOS：管理本机 dockerd（systemctl）
└── wsl/                    # Windows + WSL 实现
    ├── wsl.go              # WSLProvisioner：接口实现
    ├── exec.go             # wsl.exe 调用封装：UTF-16 解码、绝对路径、退出码
    ├── distro.go           # 目录布局 + state.json 读写
    ├── download.go         # HTTP 下载 + 断点续传 + sha256 校验
    ├── daemon.go           # dockerd 启停、TCP 探活、endpoint 解析
    └── paths.go            # Windows ↔ WSL 路径换算（/mnt/d/...）
```

### 为什么共享类型放在 `types` 子包

依赖方向必须是单向的：

```
provision ──► provision/types ◄── provision/wsl
```

`provision` 需要按平台构造实现，Windows 实现（`wsl`）又需要接口与数据类型。
若类型定义在 `provision` 里，两者会形成 import cycle。把类型下沉到 `types` 后，
`provision` 对 `wsl` 的依赖只在 `platform_windows.go`（带 build tag）中成立，
Linux 构建里这条边完全不存在，因此 `go build ./...` 在两端都成立。

### 接口设计

```go
type Provisioner interface {
    // Status 只做只读探测，不产生任何副作用。
    Status(ctx context.Context) (*RuntimeStatus, error)

    // Download 下载 rootfs 包到本地缓存（支持断点续传），不安装。
    Download(ctx context.Context, progress ProgressFunc) error

    // Install 导入发行版并安装/配置 dockerd（需要已 Download）。
    Install(ctx context.Context, progress ProgressFunc) error

    // Uninstall 注销发行版并删除数据目录。
    Uninstall(ctx context.Context) error

    // Start 确保运行时可用并返回 endpoint；未安装时返回 ErrNotInstalled。
    // 必须幂等：重复调用只做探活，不重复启动进程。
    Start(ctx context.Context) (endpoint string, err error)

    // Stop 停止 dockerd 与发行版（不改变安装状态）。
    Stop(ctx context.Context) error

    // HealthCheck 探测当前 endpoint 是否可连通（docker Ping 语义）。
    HealthCheck(ctx context.Context) error
}
```

`Start` 返回 endpoint 而不是只返回 error，是因为**连接地址是运行时的产出物**
（WSL2 NAT 模式下 IP 每次启动可能变化），调用方不能预先知道。

## 4. 状态机

```
             ┌──────────────┐
   (初始)    │ NotInstalled │
             └──────┬───────┘
                    │ Download
             ┌──────▼───────┐
             │ Downloaded   │
             └──────┬───────┘
                    │ Install
             ┌──────▼───────┐        Stop        ┌──────────┐
             │  Installed   │◄──────────────────►│ Stopped  │
             └──────┬───────┘                    └────┬─────┘
                    │ Start                           │ Start
             ┌──────▼───────┐                         │
             │   Running    │◄────────────────────────┘
             └──────────────┘
                    │ Uninstall（任意已安装状态）
                    ▼
             ┌──────────────┐
             │ NotInstalled │
             └──────────────┘
```

`RuntimeStatus` 对外暴露的字段即 UI 需要的全部信息：

```go
type RuntimeStatus struct {
    State       State  `json:"state"`         // not_installed/downloaded/installed/stopped/running
    Distro      string `json:"distro"`        // 发行版名（gobrave-ubuntu-docker）
    DataDir     string `json:"data_dir"`      // <base_dir>/wsl
    Endpoint    string `json:"endpoint"`      // tcp://172.x.x.x:2375，为空表示未知
    Version     string `json:"version"`       // rootfs 版本（来自 state.json）
    DownloadURL string `json:"download_url"`  // 供 UI 展示
    Detail      string `json:"detail"`        // 人类可读的当前细节（daemon 版本、错误原因等）
}
```

## 5. Start 的语义（关键）

```
Start(ctx):
  0. 未安装但 wsl -l -q 中已有同名发行版 → 接管（adopt）：确保 dockerd → 启动 → 写接管状态
  1. 若既未安装、系统中也不存在 → return ErrNotInstalled
  2. 已安装但未运行 → wsl -d <distro> -u root 启动 dockerd（常驻）
  3. dockerd 已在运行 → 跳过启动
  4. 解析 endpoint：
       a. 读 .wslconfig 判断 networkingMode=mirrored
       b. mirrored → 试 tcp://127.0.0.1:<port>
       c. 否则取 wsl hostname -I 首个地址 → tcp://<ip>:<port>
       d. 对候选地址逐个 Ping，第一个通的即为 endpoint
  5. 缓存 endpoint 到 Manager，返回
```

**“未安装不做处理”的边界**：发行版既没有 gobrave 的安装状态、WSL 注册表里也
没有同名的才返回 `ErrNotInstalled`；**已有同名发行版（如用户自己装好的
`Ubuntu`）属于已就绪条件，直接接管启动，不下载、不导入**。

### 接管（adopt）已有发行版

配置 `container.docker.provision.distro: Ubuntu` 时，如果 `wsl -l` 已列出
`Ubuntu`，无需 `download_url`：

- `Status`：`not_installed`，`detail` 说明可直接安装（响应不变，UI 逻辑无需改）；
- `Install`：跳过下载与 `wsl --import`，校验发行版可启动 → 缺 `dockerd` 时用
  `apt-get install docker.io`（`dnf`/`yum` 兜底）→ 启动 dockerd；
- `Start`：同理自动接管，首次启动即完成；
- 接管时在 `state.json` 记 `adopted: true`，并在发行版根目录写
  `/.gobrave-docker-runtime` 标记；
- `Uninstall`：只停 daemon、删 `state.json` 与 `distro-external/`，**绝不
  `wsl --unregister` 用户自己的发行版**。

接管占位目录：`<base_dir>/wsl/distro-external/`（仅放 `state.json` 的判定依据，
不含 vhdx），与 `distro/` 隔离，删除逻辑互不影响。

## 5.1 WSL2 网络：Windows 如何访问 dockerd 与容器端口

这是最容易踩坑的部分，两种网络模式的通路完全不同：

| 模式 | dockerd 监听 | Windows 侧 endpoint |
|---|---|---|
| NAT（默认） | **`tcp://0.0.0.0:<port>`** | `tcp://<发行版 IP>:<port>`（`hostname -I` 首个地址） |
| mirrored（Win11 22H2+） | `tcp://0.0.0.0:<port>` | `tcp://127.0.0.1:<port>` 也可直达 |

**必须绑 `0.0.0.0`**：NAT 模式下 Windows 发往发行版 IP 的流量从 eth0 进入，
绑在 `127.0.0.1` 的监听收不到，症状正是「发行版内 `dockerd` 正常、
Windows 连 `127.0.0.1:2375` 被 actively refused」。发行版处于 WSL 私有虚拟
网络（`172.x`），不对局域网暴露，因此绑全接口是可接受的安全边界。

**容器组件（如 RStudio 的 8787）**：容器 IP（`172.17.0.x`）是发行版内 docker0
网桥地址，**Windows 无法路由，不要尝试直连**。由 `container.network_mode` 决定：

| network_mode | docker PortBindings | 路由 Backend | 适用 |
|---|---|---|---|
| `container_ip`（默认） | 无 | `<容器IP>:<端口>` | Linux、Docker Desktop、k8s |
| `host_port` | `0.0.0.0:0 → <端口>`（随机） | `127.0.0.1:<HostPort>` | Windows + WSL2 NAT |

```
host_port 模式等效: docker run -p 0.0.0.0:0:8787
访问:              http://127.0.0.1:<随机端口>
```

关键点：**宿主机端口永远由 docker 随机分配**（`HostPort: ""`），不使用固定
`8787`——否则第二个 RStudio 必然报 `port is already allocated`。实际端口在
`Inspect()` 里从 `NetworkSettings.Ports` 读回（`publishedAddress`），存进
`ContainerInstance.HostPort`，再由 `route/handler.go` 用作路由后端。

`spec.PublishPort` 只在 web 形态、且 `network_mode=host_port` 时为 true；
terminal / dag_node 容器一律不发布端口。

## 6. 与 DockerRuntime 的接入

现状：`DockerRuntime.getClient` 用 `client.FromEnv`，靠 `DOCKER_HOST` 环境变量。

接入方式（**不改 `getClient` 的既有行为**，新增显式注入）：

1. `DockerRuntime` 增加 `WithHost(endpoint string)` 选项：非空时 client 用
   `client.WithHost(endpoint)` 而不是 `FromEnv`。
2. 装配层（`internal/container/container.go` 注册 docker runtime 处）：
   - 创建 `provision.Manager`，调用一次 `Start`；
   - 成功 → `WithHost(endpoint)`，且 PathMapper 的 `wslDaemon=true`；
   - `ErrNotInstalled` → 不注入 host，回退 `FromEnv`（保持“未安装不做处理”），
     同时 PathMapper 保持配置推断的语义。
3. 启动不应阻塞：`Start` 用独立 goroutine + 超时（建议 20s），失败只记日志。
   桌面端首页会调用 `/container/runtime/info` 暴露真实状态给 UI。

## 7. HTTP 接口（安装向导）

复用 `ContainerHandler`，路由挂在 `RegisterContainerRoutes` 下：

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/container/runtime/provision/status` | 返回 `RuntimeStatus` |
| POST | `/container/runtime/provision/download` | 启动下载（异步），返回 202 |
| POST | `/container/runtime/provision/install` | 执行安装（异步），返回 202 |
| POST | `/container/runtime/provision/uninstall` | 卸载（同步，需确认） |
| POST | `/container/runtime/provision/start` | 手动触发 Start（等价启动时行为） |

进度通过 `ProgressFunc` 回调写入进程内的 `provisionState`，由 `status` 轮询读取
（阶段 + 百分比 + 消息），避免为一次性安装引入 SSE / WebSocket 的复杂度。

## 8. Linux 上的“同样结果”

Linux 上不导出 WSL 发行版，但 **Provisioner 接口一致**：

- `Status` 探测本机 dockerd 的 socket / `systemctl is-active docker`；
- `Start` 在本机 dockerd 未运行且已安装时执行 `systemctl start docker`（或在容器内
  场景直接返回不可管理），返回 `unix:///var/run/docker.sock` 对应的 host 字符串；
- `Download` / `Install` / `Uninstall` 返回 `ErrUnsupported`，由 UI 隐藏向导。

这样上层 `Manager.Start`、`DockerRuntime` 装配、`/runtime/provision/*` 接口
在两端完全一致，只有 `Status().State` 的判定来源不同。

## 9. 安全与边界

- dockerd 监听 **`tcp://127.0.0.1:<port>`**（WSL 内绑定环回不可用时退化为
  `0.0.0.0` + Windows 防火墙规则），默认端口 2375 可配置；**不做 TLS 的前提是
  仅绑定环回**，若检测到非环回绑定则拒绝启动并在 `Detail` 说明原因。
- rootfs 包必须校验 sha256（`<url>.sha256` 或随包发布的 checksums 文件）；
  校验失败删除临时文件并返回明确错误。
- `Uninstall` 必须二次确认（handler 层要求请求体带 `confirm: true`）。
- 所有 `wsl.exe` 调用限定超时；`dockerd` 启动使用轮询探活而非固定 sleep。
