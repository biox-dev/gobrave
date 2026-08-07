# SQLite Driver Migration Notes

## 背景

项目原先通过 GORM 的 sqlite 驱动链路使用 SQLite，构建参数设置为 CGO_ENABLED=0 时会触发运行时错误：

- Binary was compiled with CGO_ENABLED=0, go-sqlite3 requires cgo to work

为支持 CGO_ENABLED=0 构建，已切换到 no-cgo 的 SQLite 驱动实现。

## 已修改内容

### 1) 运行时代码改动

- 文件: internal/container/container.go
- 变更:
  - import 从 gorm.io/driver/sqlite 改为 github.com/ncruces/go-sqlite3/gormlite
  - sqlite 分支的 dialector 从 sqlite.Open(dsn) 改为 gormlite.Open(dsn)

### 2) 测试代码改动

- 文件: internal/route/gateway_registry_test.go
- 变更:
  - import 从 gorm.io/driver/sqlite 改为 github.com/ncruces/go-sqlite3/gormlite
  - gorm.Open(sqlite.Open(dsn), ...) 改为 gorm.Open(gormlite.Open(dsn), ...)

### 3) 依赖改动

- 文件: go.mod / go.sum
- 新增主依赖:
  - github.com/ncruces/go-sqlite3/gormlite
- 间接依赖:
  - github.com/ncruces/go-sqlite3
  - github.com/ncruces/go-sqlite3-wasm/v2
  - github.com/ncruces/julianday
- 移除直接依赖:
  - gorm.io/driver/sqlite

### 4) Docker 构建改动

- 文件: Dockerfile
- 变更:
  - Go build 使用 CGO_ENABLED=0

## 当前验证结果

- CGO_ENABLED=0 go build -o /tmp/gobrave-nocgo ./cmd/server 通过
- go test ./internal/route -run GatewayRegistry 通过

## 后续如果要回切到 gorm.io/driver/sqlite

如果后续希望回到 GORM 官方 sqlite 驱动（底层 go-sqlite3，依赖 cgo），按下面步骤操作。

### A. 代码回切

1. 修改 internal/container/container.go

- import 增加 gorm.io/driver/sqlite
- import 移除 github.com/ncruces/go-sqlite3/gormlite
- 将 gormlite.Open(dsn) 改回 sqlite.Open(dsn)

2. 修改 internal/route/gateway_registry_test.go

- import 增加 gorm.io/driver/sqlite
- import 移除 github.com/ncruces/go-sqlite3/gormlite
- 将 gormlite.Open(dsn) 改回 sqlite.Open(dsn)

### B. 依赖回切

在 gobrave 根目录执行：

- go get gorm.io/driver/sqlite@latest
- go mod tidy

可选清理 no-cgo sqlite 相关依赖（若不再被任何包使用）：

- go mod edit -droprequire github.com/ncruces/go-sqlite3/gormlite
- go mod tidy

### C. 构建参数回切

修改 Dockerfile：

- 将 CGO_ENABLED=0 改为 CGO_ENABLED=1

说明：

- 使用 gorm.io/driver/sqlite 时，CGO_ENABLED=0 会在运行期触发 sqlite stub 报错。
- 生产镜像若使用 sqlite，请确保构建与运行环境支持 cgo 所需链路。

### D. 回切后验证

建议执行：

- go test ./internal/route -run GatewayRegistry
- CGO_ENABLED=1 go build -o /tmp/gobrave-cgo ./cmd/server
- docker build -t gobrave:test .

## go.work 模式注意事项

仓库根目录启用了 go.work，多模块会共享工作区解析。依赖排查时建议同时做两种检查：

- 工作区模式: go mod why -m <module>
- 单模块模式: GOWORK=off go mod why -m <module>

这样可以区分“本模块依赖”与“工作区其他模块引入”的差异。
