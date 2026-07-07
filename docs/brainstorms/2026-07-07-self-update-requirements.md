# 自更新（Self-Update）需求文档

- **日期**: 2026-07-07
- **状态**: 待规划（brainstorm 完成，交接 ce-plan）
- **范围分级**: Standard

## 要解决的问题

`coding-pet-agent` 与 `coding-pet-server` 目前只能靠人工重新编译（`restart.sh`）或手动分发二进制来升级。当二进制部署在多台设备上时，逐台手动更新成本高、易漏。需要让运行中的 agent/server 能自动感知 GitHub 上发布的新版本，自行下载、替换二进制并重启到新版本——全程无需人工介入，且要在 Windows / macOS / Linux 三平台一致工作。

一台设备上可能**同时**运行 agent 与 server，两者需各自独立完成自更新，互不干扰。

## 目标用户与价值

- **用户**: 项目维护者（simonlei）本人及在多设备上部署 coding-pet 的使用者。
- **价值**: 一次 `git tag v* && push` 触发 CI 发布 release 后，所有在线设备在下一个检查周期内自动升级到新版本，无需登录每台机器执行 `restart.sh`。

## 方案形态（已定）

自更新逻辑**内置于 agent 和 server 自身**，不新增独立的 updater 二进制，不使用双脚本（bash + PowerShell）。理由：单一代码库、三平台行为一致、可复用现有 `internal/protocol`、`internal/collector` 代码。

### 更新与重启序列（已定）

采用「先替换二进制 → spawn 新进程 → 旧进程自愿退出」模式，**不自杀**（parent 只需正常返回）：

1. 检查到 GitHub 有更新版本。
2. 下载对应平台/架构的归档，校验 SHA256，解出目标二进制到临时文件。
3. 原子替换磁盘上的当前二进制（见下方平台差异）。
4. 从（已更新的）二进制路径 spawn 一个 detach 的子进程，参数与当前进程一致。
5. 旧进程退出。子进程即为新版本。

- **agent**：无监听端口（它是向 server 上报的 HTTP 客户端），重启平凡安全，无端口冲突。
- **server**：**旧进程先关闭监听套接字，再 spawn 子进程**由子进程重新 bind 同一端口。三平台一致，无需任何 socket 特殊 flag（不采用 SO_REUSEPORT——Windows 无干净等价物）。代价是「关旧监听」到「新进程 bind 成功」之间存在**亚秒级连接空窗**；对轮询式监控 dashboard 无实际影响。

### 平台差异（实现关注点，非本文决策）

- **Linux / macOS**：可在进程运行时用 rename 将新文件覆盖当前二进制（运行进程持有旧 inode，新文件供下次 exec）。
- **Windows**：**无法覆盖/删除正在运行的 `.exe`（被锁）**，但**可以 rename**。序列须为：把当前 `.exe` rename 成 `.old` → 把新 exe 写到原名 → spawn 新进程 → 旧进程退出 → 下次启动时清理残留 `.old`。
- detach 子进程使其脱离父进程存活、新建会话、stdio 重定向到日志文件：Unix 用 `setsid` 语义，Windows 用 `DETACHED_PROCESS`。

## 触发方式（已定）

**两者都支持，共用同一套更新逻辑：**

1. **内置定时自动检查**：进程内后台定时器（默认间隔待定，建议 ~30min）轮询 GitHub releases，发现新版即自动下载+重启。可用 `--auto-update=false`（或等价环境变量）关闭。
2. **手动子命令**：`--self-update`（或子命令）手动立即检查并更新一次，便于按需触发或配合外部 cron。

## 版本来源与比较（已定）

- 数据源：GitHub Releases API `GET /repos/simonlei/coding-pet/releases/latest`——**只返回最新的正式版**（跳过 prerelease 与 draft）。
- **只追正式版**（`prerelease=false`）。当前 CI 发布的所有 tag 均为正式版，行为与现状一致。
- 版本号取自 release 的 tag（`v*` 形式），与本地运行二进制的版本做比较；仅当远端版本更新时才升级。
- 归档命名约定（来自现有 `build.yml`）：`coding-pet-${VERSION}-${GOOS}-${GOARCH}.tar.gz`（Windows 为 `.zip`），另有 `SHA256SUMS.txt`。更新器据当前 `runtime.GOOS`/`runtime.GOARCH` 选择正确资产。

## 硬性前置：修复版本注入 bug（阻塞项）

**当前 `cmd/agent/main.go:17` 与 `cmd/server/main.go:17` 均为 `const version = "0.1.0"`。** CI 通过 `-ldflags "-X main.version=..."` 注入版本，但 **`-X` 只能作用于 `var`，对 `const` 静默失效**——因此当前每个已发布二进制无论 tag 为何都报告 `0.1.0`。

- **在自更新能按版本比较之前，必须先把两处 `const version` 改为 `var version`。** 否则本地永远读到 `0.1.0`，要么永不升级、要么每次都误判为需升级。
- 这是本特性的**硬前置条件**，应在规划的第一步完成。

## 成功标准

1. 在一台同时运行 agent+server 的设备上，发布一个更新版本后，两个进程都能在检查周期内各自升级到新版本并继续正常工作（agent 继续上报、server 继续在原端口服务）。
2. 三平台（Windows/macOS/Linux）均能完成「下载→校验→替换→重启」全流程。
3. 版本比较正确：本地二进制报告真实版本（`var version` 修复后），仅在远端更严格更新时才升级。
4. `--self-update` 手动触发与定时自动检查复用同一逻辑，结果一致。
5. 升级失败（下载失败、校验失败、spawn 失败）时**安全回退**：旧进程继续运行，不留下损坏的二进制导致下次启动失败（尤其 Windows 的 `.old` 清理）。

## 范围边界

**本次包含：**
- agent/server 内置定时自动检查 + `--self-update` 手动触发。
- GitHub `/releases/latest` 正式版比较、按平台/架构下载、SHA256 校验、原子替换、detach 重启。
- 三平台差异处理（Windows rename-swap、Unix inode 覆盖）。
- 修复 `const version` → `var version`。

**明确不做（Deferred for later）：**
- 版本回滚 / 降级到旧版本。
- prerelease / 灰度 / 分批发布策略。
- 更新签名验证（超出 SHA256 之外的 GPG/cosign 签名）。
- 更新进度 / 状态在 dashboard 上的可视化上报（可作为后续增强）。
- 独立 updater 二进制或双脚本方案（已否决）。

## 依赖与假设

- **假设**：release 资产命名严格遵循现有 `build.yml` 约定（`coding-pet-${VERSION}-${GOOS}-${GOARCH}.{tar.gz|zip}`）。若 CI 命名变化，更新器解析逻辑需同步。
- **假设**：`SHA256SUMS.txt` 随 release 发布（现有 release job 已生成），用于校验。
- **假设**：目标设备能访问 `api.github.com` 与 release 下载地址（`github.com` / `objects.githubusercontent.com`）。若在受限网络，需另议代理/镜像（本次不含）。
- **未验证假设**：两个二进制的启动参数可从当前进程完整重建（agent 有 `--server/--token/--id/--interval`，server 有 `--port/--token`）。规划时需确认「以何种方式把当前参数原样传给子进程」——直接透传 `os.Args[1:]` 是最简可行方案，需确认无一次性/敏感参数。

## 待解决问题（交接规划）

1. 定时检查的默认间隔与是否需 jitter（避免所有设备同刻打 GitHub API）？
2. 关闭开关的具体形式：`--auto-update=false` flag、环境变量，还是两者？
3. GitHub API 未认证时有速率限制（60 req/h/IP）；默认 30min 间隔单机无压力，但需确认是否需支持可选 token。
4. 子进程参数传递：透传 `os.Args[1:]` vs 显式重建——是否有不应透传的参数？
5. Windows `.old` 残留清理的确切时机（下次启动时清理 vs 延迟删除）。
6. 日志：更新事件（检查/下载/替换/重启/失败）记录到何处，便于排障。
