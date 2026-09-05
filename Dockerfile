# ============================
# Stage 1: Build frontend (brave-ui)
# ============================
FROM node:22-bookworm-slim AS frontend

RUN apt-get update && apt-get -o Acquire::Retries=5 install -y --no-install-recommends \
        git \
        curl \
        ca-certificates \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /tmp/brave-ui

# 浅克隆仓库（只获取最新 commit）
RUN git clone --depth 1 https://github.com/gobravedev/brave-ui.git .
ENV NODE_OPTIONS="--max-old-space-size=4096"

# 安装依赖并构建
RUN yarn install --frozen-lockfile && yarn build

# ============================
# Stage 2: Build Go backend
# ============================
FROM golang:1.25-bookworm AS gobuilder

WORKDIR /app

# 先复制依赖文件，利用 Docker 缓存层
COPY go.mod go.sum ./
RUN go mod download

# 复制源码并构建
COPY . .

RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o gobrave .

# ============================
# Stage 3: Final container
# ============================
FROM debian:bookworm-slim

RUN apt-get update && apt-get -o Acquire::Retries=5 install -y --no-install-recommends \
        ca-certificates \
        tzdata \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

# 复制 Go 二进制文件
COPY --from=gobuilder /app/gobrave /app/gobrave

# 复制前端构建产物到 frontend/web/ 目录（gobrave 从此目录提供静态文件服务）
COPY --from=frontend /tmp/brave-ui/dist /app/frontend/web

# 复制 Docker 环境配置文件
# COPY config.docker.yml /app/config.yml

# 复制静态资源（logo 等）
COPY assets/ /app/assets/

EXPOSE 8082

CMD ["/app/gobrave"]

# docker build -t registry.cn-hangzhou.aliyuncs.com/wybioinfo/gobrave .