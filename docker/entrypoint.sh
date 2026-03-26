#!/bin/sh
# entrypoint.sh - 生产环境自我修复逻辑

# 1. 确保数据目录存在 (默认为容器标准路径)
mkdir -p /app/data

# 2. 如果缺少核心配置，创建一个基础模板
if [ ! -f "/app/data/config.toml" ]; then
    echo "Creating seed config.toml in /app/data..."
    cat > "/app/data/config.toml" <<EOF
[server]
addr = ":8080"
admin_token = "admin-secret-token"
EOF
fi

# 3. 启动主程序 (它会自动查找 /app/data)
echo "Starting ProxyLLM..."
exec /usr/bin/proxyllm
