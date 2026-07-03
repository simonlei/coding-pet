---
name: coding-pet-codebuddy-hook
description: 为 coding-pet dashboard 安装或卸载 CodeBuddy IDE 的 7 个 hooks，实时上报会话状态给本地 agent。触发场景：(1) 用户希望让 CodeBuddy IDE 会话状态出现在 coding-pet dashboard；(2) 用户要求"安装/卸载/清理"CodeBuddy hooks；(3) 用户提到 codebuddy 会话状态、waiting_for_input、hook 上报等相关词。不适用于：CodeBuddy CLI（那部分已由 agent 磁盘扫描采集）、Claude Code、非 CodeBuddy 产品。
---

# CodeBuddy IDE Hooks 安装 / 卸载 Skill

## 功能

为 [coding-pet](https://github.com/xxx) 本地 agent 一键部署或撤销 CodeBuddy
IDE 的 hooks 上报链路，让 dashboard 能实时看到 IDE 会话在
`active` / `waiting_for_input` / `terminated` 之间的切换。

## 前置条件

- 用户已在本机运行 coding-pet agent（默认监听 `127.0.0.1:38765`），
  可通过 `curl http://127.0.0.1:38765/healthz` 验证
- 用户已安装 CodeBuddy IDE（`copilot.tencent.com/ide` 独立客户端 或
  CodeBuddy CN / WorkBuddy 内网发行版）
- Unix 机器需要 `python3`（用于安全合并 JSON）

## 关键设计（幂等安全）

- 脚本永远部署到 **用户目录** `~/.codebuddy/hooks/`，settings.json
  里 command 只写 `%USERPROFILE%\.codebuddy\hooks\...` 或 `$HOME/.codebuddy/hooks/...`，
  绝不写仓库绝对路径 → 用户移动仓库不会失效
- 每条我们插入的 hook group 都带 `"__managed_by": "coding-pet"` 标记，
  uninstall 靠标记精确清理，不影响用户自己配的其他 hooks
- 修改 settings.json 前一定备份 `settings.json.bak-<时间戳>`
- install 幂等：重复运行会先删旧标记条目再插入新的，不会重复叠加

## 交互流程

### 用户想安装

1. 询问用户 agent 是否运行在默认地址 `http://127.0.0.1:38765`；
   - 是 → 直接执行 install（无参数）
   - 否 → 让用户提供自定义 URL，用 `--hook-url` 传入
2. 根据操作系统执行对应脚本：
   - **Windows**：
     ```powershell
     powershell -NoProfile -ExecutionPolicy Bypass -File <skill_dir>\scripts\install.ps1
     # 若需自定义 URL：
     powershell -NoProfile -ExecutionPolicy Bypass -File <skill_dir>\scripts\install.ps1 -HookUrl http://127.0.0.1:xxxx/ide/hook
     ```
   - **macOS / Linux**：
     ```bash
     bash <skill_dir>/scripts/install.sh
     # 若需自定义 URL：
     bash <skill_dir>/scripts/install.sh --hook-url http://127.0.0.1:xxxx/ide/hook
     ```
3. 提示用户重启 CodeBuddy IDE

### 用户想卸载

1. 直接执行对应平台的 uninstall 脚本：
   - **Windows**：
     ```powershell
     powershell -NoProfile -ExecutionPolicy Bypass -File <skill_dir>\scripts\uninstall.ps1
     # 想保留脚本文件仅清理配置：
     powershell -NoProfile -ExecutionPolicy Bypass -File <skill_dir>\scripts\uninstall.ps1 -KeepScripts
     ```
   - **macOS / Linux**：
     ```bash
     bash <skill_dir>/scripts/uninstall.sh
     # 保留脚本：
     bash <skill_dir>/scripts/uninstall.sh --keep-scripts
     ```
2. 提示用户重启 CodeBuddy IDE

### Dry-run 预览（可选）

两个平台都支持先跑一次 `-DryRun` / `--dry-run` 只看变更不落盘。

## 操作前必看

- 用户 `~/.codebuddy/settings.json` 如已存在但内容非法 JSON，脚本会报错
  中止，不会破坏原文件；先让用户修好或删掉再重跑
- 卸载默认会同时删除 `~/.codebuddy/hooks/coding-pet-hook.*` 及可能的
  `agent.env`；如用户想保留脚本文件，加 `--keep-scripts`（Unix）或
  `-KeepScripts`（Windows）
- 每次修改 settings.json 都会生成 `settings.json.bak-<yyyyMMdd-HHmmss>`
  备份，可让用户随时手动回滚

## 内部资产索引

- `scripts/install.ps1` / `scripts/install.sh`：安装器
- `scripts/uninstall.ps1` / `scripts/uninstall.sh`：卸载器
- `scripts/coding-pet-hook.ps1` / `scripts/coding-pet-hook.sh`：会被安装到
  用户目录的转发脚本

## 状态映射（供解释用）

| Hook 事件 | Agent 内部状态 |
|---|---|
| `SessionStart` / `UserPromptSubmit` / `PreToolUse` / `PostToolUse` | `active` |
| `Stop` | `waiting_for_input` |
| `SessionEnd` | 立即从内存清理（等价 `terminated`） |
| `PreCompact` | 保留旧状态（心跳） |
