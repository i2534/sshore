# sshore

[English](README.md) | **简体中文**

跨平台 SSH 端口转发 + SFTP 管理工具。基于 Wails v2（Go）+ Vue 3 构建。

## 环境要求

- Go 1.26
- Node.js 22+（LTS）
- 系统 `PATH` 中需有 OpenSSH（`ssh`、`sftp`）
- wails CLI（`go install github.com/wailsapp/wails/v2/cmd/wails@latest`）

## 构建

```bash
make both     # 清理后同时构建 Linux 与 Windows 二进制（默认，尺寸优化）
make linux    # 仅 Linux amd64
make windows  # 仅 Windows amd64（打包：图标经 .syso 嵌入）
make build    # 当前平台
```

或直接使用 Wails CLI：

```bash
wails build
```

### 尺寸优化

`make` 目标会剥离符号/DWARF（`-ldflags "-s -w"`）、传递 `-trimpath`，
并默认用 UPX 压缩最终二进制（Linux ≈3.7 MB，Windows ≈4.5 MB）。

- 设置 `COMPRESS=0` 可跳过 UPX（例如杀毒软件误报 UPX 打包的 Go 二进制，
  或你更看重启动速度）：`make both COMPRESS=0`
- 需要 `PATH` 中存在 UPX；若缺失，`wails build -upx` 会告警并跳过压缩
  （Wails 仅在安装 UPX 时才压缩）。
- 构建 Windows 时**不要**传 `-nopackage`：Wails 仅在启用打包（`Pack=true`）
  时生成带图标的 `.syso` 资源，`-nopackage` 会得到无图标的 exe。
  `make windows` 目标特意保留打包。

## 开发运行

```bash
wails dev
```

## 功能特性

- **SSH 端口转发**：本地 `-L`、远程 `-R`、动态 SOCKS `-D`、跳板机 `-J`
- **SFTP 文件管理**：浏览 / 上传 / 下载 / 递归下载 / 删除 / 重命名 / 建目录
- **配置**：只读解析 `~/.ssh/config`（不存储凭据）；隧道规则保存在
  `~/.config/sshore/sshore.toml`（Windows 为 `%APPDATA%\sshore\sshore.toml`）
- **命令导入**：将粘贴的 `ssh -L/-R/-D ...` 命令解析为规则
- **实时日志面板**：隧道/SFTP 事件的内存环形缓冲（1000 条）；可按规则
  单独切换过滤（规则卡片「日志」按钮 / 日志面板 chips），ssh 子进程的
  stderr 逐行采集进面板——隧道内部错误不再被吞掉
- **规则校验**：创建/编辑/导入时拒绝空目标主机（`-L 23080::3080` 这类
  坏规则会让 ssh 解析空主机名失败：连接即 RST 而进程不退出，界面误报
  connected）；本地/远程转发绑定失败时 `ExitOnForwardFailure` 使隧道
  直接进入 error 状态
- 认证使用系统 OpenSSH（密钥/agent/config），因此 GUI 不支持密码与
  keyboard-interactive 认证——请为你的主机配置密钥或 agent 认证

## 架构

- `internal/config` — 解析 `~/.ssh/config`（枚举用 kevinburke/ssh_config，
  权威字段用 `ssh -G`）并读写 TOML 配置存储
- `internal/forward` — 生成/管理长驻 `ssh -N` 子进程、生命周期状态机、
  端口预检、错误分类
- `internal/sftp` — 每次操作一个 `sftp -b` 进程，`ls -la` 输出解析
- `internal/importer` — 将 `ssh -L/-R/-D` 命令行分词为规则（注入安全）
- `frontend/src` — Vue 3 UI（左侧导航模块切换：转发 / SFTP）+ Pinia 日志存储

## 测试

```bash
go test ./...          # Go 子系统测试（mock ssh/sftp）
cd frontend && npx vitest run   # 日志环形缓冲测试
```

`make ci` 一条命令执行 CI 的全部内容（vet + `-race` Go 测试 + 前端测试）。

## E2E

```bash
make e2e    # 等价于：bash e2e/test_local.sh
```

`e2e/test_local.sh` 会启动一个临时本地 sshd，验证 sshore 依赖的 OpenSSH 行为：
`ssh -G` 别名解析、`-N -L` 本地转发绑定、`sftp ls -l` 输出解析。
需要 `/usr/sbin/sshd`、`ssh-keygen` 和 `python3`。

## CI

`.github/workflows/ci.yml` 在 push/PR 时运行：`go vet`、Go 测试（`-race`）、
前端测试与构建，以及 Linux（webkit2gtk-4.1）与 Windows（图标经 `.syso` 嵌入）
的 `wails build`，均含 UPX 压缩。
