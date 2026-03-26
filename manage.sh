#!/bin/bash

# ══════════════════════════════════════════════════════════════════════════════
# ProxyLLM 全能管理脚本 (极简根目录版)
# ══════════════════════════════════════════════════════════════════════════════

set -e

APP_NAME="proxyllm"
SRC_DIR="src"
BIN_DIR="bin"
DATA_DIR="data"
DOCKER_DIR="docker"
DOCKER_IMAGE="proxyllm:latest"

# 颜色定义
BLUE='\033[0;34m'
GREEN='\033[0;32m'
NC='\033[0m'

function log() { echo -e "${BLUE}[INFO]${NC} $1"; }
function success() { echo -e "${GREEN}[SUCCESS]${NC} $1"; }

function build_local() {
    log "正在从 src 编译本地二进制..."
    mkdir -p $BIN_DIR
    cd $SRC_DIR && go build -o ../$BIN_DIR/$APP_NAME ./cmd/proxyllm
    cd ..
    success "本地编译成功: $BIN_DIR/$APP_NAME"
}

function package_docker() {
    log "开始构建并导出镜像包..."
    docker build -t $DOCKER_IMAGE -f $DOCKER_DIR/Dockerfile .
    mkdir -p $BIN_DIR
    docker save $DOCKER_IMAGE | gzip > $BIN_DIR/proxyllm_image.tar.gz
    success "打包完成: $BIN_DIR/proxyllm_image.tar.gz"
}

function docker_run() {
    log "准备数据目录..."
    mkdir -p $DATA_DIR
    log "通过 docker-compose 启动服务..."
    # 使用 -f 指定位于 docker/ 下的 compose 文件
    docker-compose -f $DOCKER_DIR/docker-compose.yml up -d
    success "服务已启动。请访问: http://localhost:8011"
}

case "$1" in
    fmt)            cd $SRC_DIR && go fmt ./... && go mod tidy; cd ..; success "代码重构完成。" ;;
    build)          build_local ;;
    docker-run)     docker_run ;;
    package-docker) package_docker ;;
    clean)          rm -rf $BIN_DIR; success "清理完成。" ;;
    help|*)         echo "用法: ./manage.sh [fmt|build|docker-run|package-docker|clean]" ;;
esac
