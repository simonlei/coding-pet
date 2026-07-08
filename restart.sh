#!/usr/bin/env bash
#
# 重新编译并在后台重启 coding-pet 的 agent 与 server。
# 已在运行的旧进程会被杀掉后重启。
#
# 用法:
#   ./restart.sh                # 编译并重启 agent + server
#   ./restart.sh server         # 只重启 server
#   ./restart.sh agent          # 只重启 agent
#
# 可通过环境变量覆盖运行参数(见下方默认值)。

set -euo pipefail

# 切到脚本所在目录(即项目根目录)
cd "$(dirname "$(readlink -f "$0")")"

# ---- 可配置参数 ----------------------------------------------------------
SERVER_PORT="${SERVER_PORT:-3000}"
SERVER_TOKEN="${SERVER_TOKEN:-}"               # server 端认证 token,空则不校验

AGENT_SERVER="${AGENT_SERVER:-http://127.0.0.1:${SERVER_PORT}}"  # agent 上报地址
AGENT_TOKEN="${AGENT_TOKEN:-${SERVER_TOKEN}}"  # agent 上报 token
AGENT_ID="${AGENT_ID:-}"                       # 机器唯一 ID,空则用 hostname
AGENT_INTERVAL="${AGENT_INTERVAL:-5s}"         # 上报间隔
# -------------------------------------------------------------------------

LOG_DIR="${LOG_DIR:-$PWD}"
mkdir -p "$LOG_DIR"

TARGET="${1:-all}"

# 杀掉匹配的旧进程
kill_proc() {
    local name="$1"
    local pids
    pids="$(pgrep -f "./${name}" || true)"
    if [[ -n "$pids" ]]; then
        echo "[restart] 停止已有 ${name} (pid: ${pids//$'\n'/ })"
        # shellcheck disable=SC2086
        kill $pids 2>/dev/null || true
        sleep 1
        # 仍存活则强杀
        pids="$(pgrep -f "./${name}" || true)"
        if [[ -n "$pids" ]]; then
            # shellcheck disable=SC2086
            kill -9 $pids 2>/dev/null || true
        fi
    fi
}

start_server() {
    kill_proc coding-pet-server
    local args=(--port "$SERVER_PORT")
    [[ -n "$SERVER_TOKEN" ]] && args+=(--token "$SERVER_TOKEN")
    echo "[restart] 启动 server: ./coding-pet-server ${args[*]}"
    nohup ./coding-pet-server "${args[@]}" > "$LOG_DIR/coding-pet-server.log" 2>&1 &
    echo "[restart] server pid=$! 日志=$LOG_DIR/coding-pet-server.log"
}

start_agent() {
    kill_proc coding-pet-agent
    local args=(--server "$AGENT_SERVER" --interval "$AGENT_INTERVAL")
    [[ -n "$AGENT_TOKEN" ]] && args+=(--token "$AGENT_TOKEN")
    [[ -n "$AGENT_ID" ]] && args+=(--id "$AGENT_ID")
    echo "[restart] 启动 agent: ./coding-pet-agent ${args[*]}"
    nohup ./coding-pet-agent "${args[@]}" > "$LOG_DIR/coding-pet-agent.log" 2>&1 &
    echo "[restart] agent pid=$! 日志=$LOG_DIR/coding-pet-agent.log"
}

# ---- 编译 ----------------------------------------------------------------
case "$TARGET" in
    server) echo "[restart] 编译 server..."; make build-server ;;
    agent)  echo "[restart] 编译 agent...";  make build-agent ;;
    all)    echo "[restart] 编译 agent + server..."; make build ;;
    *) echo "用法: $0 [all|server|agent]" >&2; exit 1 ;;
esac

# ---- 重启 ----------------------------------------------------------------
case "$TARGET" in
    server) start_server ;;
    agent)  start_agent ;;
    all)    start_server; start_agent ;;
esac

sleep 1
echo "[restart] 完成。当前进程:"
pgrep -af "./coding-pet-" || echo "  (无)"
