# Build stage
FROM docker.1ms.run/library/golang:1.24-alpine AS builder
RUN apk add --no-cache gcc musl-dev

WORKDIR /build

# 1. 拷贝源码
COPY src/go.mod src/go.sum ./
ENV GOPROXY=https://goproxy.cn,direct
RUN go mod download

COPY src/ .

# 2. 编译
RUN CGO_ENABLED=1 GOOS=linux go build -ldflags="-w -s" -o proxyllm ./cmd/proxyllm

# Final stage
FROM docker.1ms.run/library/alpine:latest
RUN apk add --no-cache tzdata ca-certificates
WORKDIR /app

# 1. 拷贝二进制
COPY --from=builder /build/proxyllm /usr/bin/proxyllm

# 2. 拷贝启动脚本 (相对于构建上下文)
COPY docker/entrypoint.sh /usr/bin/entrypoint.sh
RUN chmod +x /usr/bin/entrypoint.sh

# 默认环境变量
ENV PROXYLLM_DB_PATH=/app/data/proxyllm.db
ENV PROXYLLM_SERVER_ADDR=:8080

EXPOSE 8080

ENTRYPOINT ["/usr/bin/entrypoint.sh"]
