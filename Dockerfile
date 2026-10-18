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
# linuxserver.io s6-overlay base image: user `abc` (renumbered to PUID/PGID at
# start), /config is both the persistent volume and the home directory of `abc`.
# 用户 abc 的 UID/GID 在容器启动时按 PUID/PGID 重映射，宿主机挂载目录不会出现权限问题。
FROM lscr.io/linuxserver/baseimage-ubuntu:jammy

ENV PUID=1000
ENV PGID=1000

# 服务监听端口
ENV GOBRAVE_PORT=8082
# 程序工作目录 / 用户数据持久化目录（建议挂载卷）
ENV GOBRAVE_HOME=/config
# gobrave 数据目录（脚本版本仓库、store 等）
ENV GOBRAVE_BASE_DIR=/config/.gobrave
ENV SHELL=/bin/bash

SHELL ["/bin/bash", "-c"]

# gobrave 作为一个 agent 平台，需要 git（脚本仓库 / 克隆项目）、
# python（分析脚本）以及常用的网络与构建工具。
RUN apt-get update \
	&& DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
		build-essential \
		ca-certificates \
		curl \
		git \
		gnupg \
		jq \
		openssh-client \
		python3 \
		python3-pip \
		python3-venv \
		unzip \
		vim \
		xz-utils \
	&& usermod -s /bin/bash abc \
	&& rm -rf /var/lib/apt/lists/*

WORKDIR /app

# 复制 Go 二进制文件
COPY --from=gobuilder /app/gobrave /app/gobrave

# 复制前端构建产物到 frontend/web/ 目录（gobrave 从此目录提供静态文件服务）
COPY --from=frontend /tmp/brave-ui/dist /app/frontend/web

# 复制静态资源（logo 等）
COPY assets/ /app/assets/

# s6-overlay 启动脚本（cont-init.d 初始化 + services.d 守护进程）
COPY root/ /
RUN chmod +x /etc/cont-init.d/10-gobrave /etc/services.d/gobrave/run

WORKDIR /config

EXPOSE 8082

# docker build -t gobrave .
#
# docker run -d --name gobrave \
#   -p 8082:8082 \
#   -e PUID=$(id -u) -e PGID=$(id -g) \
#   -v gobrave-config:/config \
#   gobrave
#
# 配置：把 config.yml 放到挂载卷 /config/config.yml；
#       镜像内也内置了 /app/config.yml（若存在且卷内没有 config.yml 则使用它）。
# 数据：持久化在 /config/.gobrave（由 GOBRAVE_BASE_DIR 指定）。
# 访问 http://localhost:8082/