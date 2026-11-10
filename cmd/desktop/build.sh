#!/bin/bash
# 构建 brave 桌面客户端（Wails v3）。
#
# 前端固定复用 gobrave 的 frontend/web（与 Web 版同一份产物，不复制副本）：
#   1. 若 frontend/web 不存在（或指定 --sync 时）用 brave-ui 重新构建；
#   2. 以 -tags embed_frontend 编译，把 frontend/web 内嵌进二进制。
#
# 注意：宿主 GTK 为 4.6.9，Wails v3 默认 GTK4 后端需要 >= 4.10，
# 因此必须带 gtk3 构建标签。
set -euo pipefail

# 进入 gobrave 仓库根目录（本文件位于 cmd/desktop/ 下）。
cd "$(dirname "$0")/../.."

# 重建前端：用 brave-ui 的产物覆盖 gobrave/frontend/web。
if [[ "${1:-}" == "--sync" || ! -f frontend/web/index.html ]]; then
  echo "==> 重新构建前端 (../brave-ui)"
  ( cd ../brave-ui && npm run build )
  rm -rf frontend/web
  mkdir -p frontend/web
  cp -r ../brave-ui/dist/. frontend/web/
fi

echo "==> 编译桌面二进制 (tags: gtk3 embed_frontend)"
mkdir -p bin
go build -tags "gtk3 embed_frontend" -o bin/brave-desktop ./cmd/desktop
go run -tags "gtk3 embed_frontend"  ./cmd/desktop

echo "==> 完成: bin/brave-desktop"