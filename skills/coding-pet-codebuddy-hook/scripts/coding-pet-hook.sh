#!/usr/bin/env bash
# coding-pet-hook.sh —— CodeBuddy IDE Hook 通用转发脚本（macOS / Linux）
#                       上报会话事件给 coding-pet 本地 agent。
#
# 官方规范：https://www.codebuddy.cn/docs/ide/Features/Hooks
# 事件通过 stdin 传入 JSON（含 hook_event_name / session_id / cwd 等），
# 脚本必须通过 stdout 返回 {"continue":true,...} 决策 JSON。
#
# 本脚本对所有 7 种事件（SessionStart / SessionEnd / UserPromptSubmit /
# PreToolUse / PostToolUse / Stop / PreCompact）通用：
#   1. 把整块 stdin JSON 转发到本地 agent 的 /ide/hook 端点；
#   2. 输出统一的 allow/continue 决策 JSON，不阻塞 IDE。
#
# 环境变量：
#   DASHBOARD_IDE_HOOK_URL   自定义上报地址，默认 http://127.0.0.1:38765/ide/hook
#   CODEBUDDY_PRODUCT        codebuddy | workbuddy，用于区分产品分支（可选）

set -u

URL="${DASHBOARD_IDE_HOOK_URL:-http://127.0.0.1:38765/ide/hook}"

INPUT="$(cat 2>/dev/null || true)"
if [ -z "$INPUT" ]; then
    INPUT='{}'
fi

extract() {
    local key="$1"
    if command -v jq >/dev/null 2>&1; then
        printf '%s' "$INPUT" | jq -r --arg k "$key" '.[$k] // ""' 2>/dev/null
    else
        printf '%s' "$INPUT" | grep -oE "\"$key\"[[:space:]]*:[[:space:]]*\"[^\"]*\"" \
            | head -n 1 | sed -E "s/\"$key\"[[:space:]]*:[[:space:]]*\"([^\"]*)\"/\1/"
    fi
}

EVENT="$(extract 'hook_event_name')"
SESSION_ID="$(extract 'session_id')"
CWD="$(extract 'cwd')"

TOOL="codebuddy_ide"
case "${CODEBUDDY_PRODUCT:-}" in
    workbuddy|WorkBuddy) TOOL="workbuddy" ;;
esac
TS_MS=$(( $(date +%s) * 1000 ))

esc() {
    printf '%s' "$1" | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g'
}
PAYLOAD=$(printf '{"hook_event_name":"%s","session_id":"%s","cwd":"%s","tool":"%s","timestamp":%d}' \
    "$(esc "$EVENT")" "$(esc "$SESSION_ID")" "$(esc "$CWD")" "$TOOL" "$TS_MS")

if command -v curl >/dev/null 2>&1; then
    (curl --silent --show-error --max-time 2 \
        -H 'Content-Type: application/json' \
        -X POST -d "$PAYLOAD" "$URL" >/dev/null 2>&1 &) 2>/dev/null
fi

printf '{"continue":true}\n'
exit 0
