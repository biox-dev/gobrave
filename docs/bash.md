cp_web.sh
```
rm -rf /ssd1/wy/workspace3/go-project/gobrave/frontend/web/*
cp -r /ssd1/wy/workspace3/go-project/go-brave-ui/dist/* /ssd1/wy/workspace3/go-project/gobrave/frontend/web
```

run.sh
```
#!/bin/bash
PORT=8084

# 1. 检查指定端口是否被占用
PID=$(lsof -t -i:$PORT)

if [ -n "$PID" ]; then
    echo "检测到端口 $PORT 已被 PID $PID 占用，正在发送 SIGTERM 信号尝试优雅关闭..."
    
    # 2. 优雅终止进程（推荐优先使用 kill 默认信号，而非直接 kill -9）
    kill $PID
    
    # 3. 等待进程死亡及资源释放
    WAIT_COUNT=0
    while kill -0 $PID 2>/dev/null; do
        sleep 1
        WAIT_COUNT=$((WAIT_COUNT + 1))
        echo "等待进程退出... (已等待 ${WAIT_COUNT}s)"
        
        # 设置超时保护（例如最多等待 15 秒），防止无限等待
        if [ $WAIT_COUNT -ge 15 ]; then
            echo "进程未在预期时间内退出，强制杀死进程 (kill -9)..."
            kill -9 $PID
            break
        fi
    done
    echo "端口 $PORT 已成功释放。"
else
    echo "端口 $PORT 未被占用，直接启动服务。"
fi


go build -o gobrave ./cmd/server

# export DISABLE_REGISTRATION=true
nohup ./gobrave > gobrave.log 2>&1 &

# tail -f server.log
```

stop.sh
```
#!/bin/bash
PORT=8084

# 1. 检查指定端口是否被占用
PID=$(lsof -t -i:$PORT)

if [ -n "$PID" ]; then
    echo "检测到端口 $PORT 已被 PID $PID 占用，正在发送 SIGTERM 信号尝试优雅关闭..."
    
    # 2. 优雅终止进程（推荐优先使用 kill 默认信号，而非直接 kill -9）
    kill $PID
    
    # 3. 等待进程死亡及资源释放
    WAIT_COUNT=0
    while kill -0 $PID 2>/dev/null; do
        sleep 1
        WAIT_COUNT=$((WAIT_COUNT + 1))
        echo "等待进程退出... (已等待 ${WAIT_COUNT}s)"
        
        # 设置超时保护（例如最多等待 15 秒），防止无限等待
        if [ $WAIT_COUNT -ge 15 ]; then
            echo "进程未在预期时间内退出，强制杀死进程 (kill -9)..."
            kill -9 $PID
            break
        fi
    done
    echo "端口 $PORT 已成功释放。"
else
    echo "端口 $PORT 未被占用，直接启动服务。"
fi



```