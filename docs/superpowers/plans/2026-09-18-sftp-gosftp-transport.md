# SFTP 传输底座换代（Go SFTP over OpenSSH 子系统）实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把 SFTP 传输从「每次操作起一个 `sftp -b` 进程 + 解析 ls 文本」换成「系统 `ssh -s sftp` 子系统的长驻会话 + `github.com/pkg/sftp` 直接驱动协议」，从而交付**字节级进度、整批取消、手动重试、下载/上传双向续传、临时名+原子改名落盘**，且现有功能零回归。

**Architecture:** `internal/sftp` 变成**门面 + 双后端**：`Ctrl` 保留四参 legacy 面（`internal/sync` 与 36 处既有测试不动语义），新增传输面（`TransferGet/TransferGetTree/TransferPut/TransferPutTree/Cancel`）；后端由 `SSHORE_SFTP_TRANSPORT` 环境变量 > `[app] sftp_transport` 配置 > 内置默认（先 `batch`）选择。`GoBackend` 用 `osutil` 新增的裸管道原语起 `ssh … -s sftp`，管道交给 `sftp.NewClientPipe`，会话由带状态机的池管理（传输并发 1、idle ≤2、列表/探活永不排队）。

**Tech Stack:** Go 1.26、Wails v2.15.0、`github.com/pkg/sftp v1.13.11`（连带 `x/crypto v0.54.0`、`x/sys v0.47.0`）、Vue 3 `<script setup>` + Pinia、vitest。

**Spec:** `docs/superpowers/specs/2026-09-18-sftp-gosftp-transport-design.md`（v6，547 行；本计划逐条实现其 D1–D18 与 §9 的验证矩阵。执行者**必须**同时读 spec，计划里只重复执行所需的最小信息。）

## Global Constraints

- Go 1.26；新增依赖 `github.com/pkg/sftp v1.13.11`，它要求 `golang.org/x/crypto v0.54.0`、`golang.org/x/sys v0.47.0`（仓库现为 0.53.0 / 0.46.0）→ `go.mod/go.sum` 必须一并更新，并复验 Windows 构建。
- 提交信息用**中文**；每个 task 结束必须能 `make ci` 全绿（`Makefile:94-98` = `npm run build` + `go vet ./...` + `go test ./... -race -count=1` + `npx vitest run`）。
- 传输并发上限 **1**（FIFO 排队）；idle 会话上限 **2**（LRU 关闭）；列表/变更/探活**永不排队、不因池满失败**。
- 进度节流 **200ms**，完成时**强制发最后一帧**；前端终态以绑定返回值为准。
- 取消 = **整批**（取消当前项 + 停止后续项），目标 `.part` 保留，取消**幂等**（已结束 id 返回 `false`）。
- 续传**只在单文件项**、**只在同一次运行内**；`PartPath` 由传输表给出，禁止扫目录猜锚点。
- 目录枚举阈值 **20000 文件 / 5s** ⇒ `Total=-1 && FilesTotal=-1 && Phase="transfer"`；枚举超时**只放弃枚举、不关会话**。
- 超时定值：探活 **5s**、取消后等子进程退出 **5s**；临时名长度阈值 **200 字节**。
- 临时文件中缀固定为 `.sshore-sftppart-`（含 `bak`）；判定一律用 `strings.Contains` / JS `includes`，**不是** glob 前缀、**不是** `startsWith('.')`。
- `Item{Name,Size,IsDir,Mode,ModTime}` 形状不变；`ModTime` 逐字保持 `2006-01-02 15:04`（watch/sync 用它做字符串相等比较）。
- 下载提交 = 本地 `os.Rename`（Windows 亦覆盖）；上传提交 = `Client.PosixRename`（先 `HasExtension` 探测，返回 `(string,bool)`）；无扩展/失败 → backup-swap + journal，**绝不先删目标**。
- **上传提交必须保留 backup-swap 回退**：Task 0 只观测到一次覆盖成功、零次独立失败 —— 即使 `HasExtension` 为真且 `PosixRename` 返回非 nil，也不得把「扩展可用」当作「覆盖必成」；`PosixRename` 返回错误时一律走 backup-swap（Task 0 评审 Important-1）。
- 上传新建 = `OpenFile(O_WRONLY|O_CREATE|O_TRUNC)`；上传续传 = `OpenFile(O_WRONLY)`（**严禁 O_TRUNC**）+ `Seek(partSize)`；**不用 O_APPEND**。
- 提交前置：`done == 开始时记录的 total`；不满足必须报错并保留 `.part`。
- 默认后端先 `batch`；Task 16 真机验收通过后把内置默认切 `gosftp`（用户仍可配置回退）。
- 不做：并发/并行传输、限速、校验和 UI、跨重启续传、自动重试、批量总体进度条、`internal/sync` 走新面。
- `internal/forward` 完全不动。`internal/sync` 与 `internal/watch` 只允许 **Task 13 Step 3 的那一处内置忽略判定**（D18：`IsInternalTemp` + `MatchExclude`/`inScope`），其余语义与文件不改；测试文件只允许 Task 5 的构造名机械替换。

---

## File Structure

| 文件 | 责任 |
|---|---|
| `internal/sftp/api.go`（新增） | `Progress` / `TransferRequest` / `Direction` / `Phase` / `TransferError` 等类型 |
| `internal/sftp/backend.go`（新增） | `Backend` 接口、`TransportSelector`、`resolveTransport`（env > config > 默认） |
| `internal/sftp/partname.go`（新增） | 临时/备份文件命名与匹配（唯一来源；前端有同字面量常量） |
| `internal/sftp/session.go`（新增） | 会话池状态机（idle/busy/closing/dead）、探活、并发 1、CloseAll |
| `internal/sftp/gosftp.go`（新增） | GoBackend：起 `ssh -s sftp`、`NewClientPipe`、能力探测、进度/取消/.part/续传 |
| `internal/sftp/copy.go`（新增） | 纯函数：`decideResume`、进度节流 `progressEmitter`、树遍历与聚合 |
| `internal/sftp/ctrl.go`（改） | 门面：legacy 四参面 + 新传输面 + 日志事件（`NewCtrl` 变门面构造） |
| `internal/sftp/batch.go`（由 ctrl.go 拆分） | `BatchBackend` + `NewBatchBackend`（旧实现原样） |
| `internal/sftp/*_test.go`（改/新增） | 既有 36 处构造函数机械替换；新纯函数与池的单测 |
| `internal/sftp/e2e_test.go`（新增） | 真实 sshd 的端到端（skip 约定沿用 `internal/sync/e2e_test.go:20-32`） |
| `internal/osutil/runner.go`（改） | 新增 `StartPipes` / `PipedProcess`（三路裸管道 + stderr 有界 drain） |
| `internal/config/store.go`（改） | `AppSettings.SftpTransport` + `Normalize` + `DefaultAppConfig`（三处） |
| `app.go`（改） | 选择器注入、绑定加 `id`/`resume`/`partPath`、`SftpTransferCancel`、进度事件转发 |
| `frontend/src/stores/settings.js`（改） | load/save 带上 `sftp_transport`（否则被 `SetSettings` 整结构覆盖清空） |
| `frontend/src/components/SettingsDialog.vue`（改） | 开关下拉（自动 / 新实现 / 旧实现）+ 加进自动保存 watch |
| `frontend/src/utils/queue.js` + `queue.test.js`（改/新增） | `percentOf/speedOf/etaOf/fmtSpeed` 纯函数 + vitest |
| `frontend/src/components/TransferQueue.vue`（改） | 进度条 / 速度 / ETA / 取消 / 重试 / 续传 / 清理 |
| `frontend/src/views/SftpView.vue`（改） | id 编排、事件订阅与退订、整批取消、`.part` 隐藏 |
| `e2e/test_local.sh`（改） | 同时跑 `./internal/sftp/` 与 `./internal/sync/` + 后端矩阵循环 + ssh 垫片透传 `-s` |
| `README.md`（改） | 依赖（只需 `ssh`）、架构段、配置段新增 `sftp_transport` |

---

## Task 0: Windows 真机前置探测（实施第一道闸门）

**为什么先做**：spec §10.1 与 R1 —— `-s` 的两种写法、`posix-rename` 能否真覆盖已存在目标、`Seek(partSize)` 续写在 Win32-OpenSSH 上是否成立，是本方案唯一的"最大未知"。失败则整条路线回炉，**不要先写实现再验证**。

**Files:**
- Create（客户机临时目录，**不入库**）: `C:\Users\lan\probe\main.go`、`C:\Users\lan\probe\go.mod`
- Create: `.superpowers/sdd/2026-09-18-sftp-gosftp-transport/probe.md`（证据落盘，gitignore 内）

**Interfaces:**
- Consumes: 无（首个 task）
- Produces: 结论「`-s` 用法」「`posix-rename` 是否覆盖」「`Seek` 续写是否有效」「stderr 是否需要独立读」→ 决定 Task 6/8 的分支与 Task 16 的默认切换

- [ ] **Step 1: 起客户机并确认通道**

用 `real-machine-testing` 技能的 Win10 VM 流程（VirtualBox `win10`，NAT `sshfwd 127.0.0.1:2222→22`，用户 `lan`），确认真机上 `ssh` 与 `sftp-server` 存在：

```powershell
ssh -p 2222 lan@127.0.0.1 "where ssh & where sftp & ssh -V"
```

预期：输出 Win32-OpenSSH 路径与版本（如 `OpenSSH_for_Windows_9.x`）。

**环境前提（不满足就先修环境，别改结论）**：客户机 `sshd` 在本机 `22` 可免密登录、`C:\Users\lan\probe` 可写、客户机可联网跑 `go mod tidy`（或把 `pkg/sftp` 与 `kr/fs` 的模块缓存预置过去）。任一项不通时，改用主机交叉构建 `GOOS=windows` 探测 exe 投放，并在 `probe.md` 里记录这一偏离。

- [ ] **Step 2: 在客户机投放探测程序**

`main.go`（约 60 行，验证四件事）。**实跑修正**：v1.13.11 的 `*sftp.Client` **没有** `ReadFile`/`WriteFile`（Task 0 实测编译不过）—— 下方对应的两处调用用等价 helper 替换即可（WriteFile = Create+Write+Close；ReadFile = Open+io.ReadAll+Close），探测顺序/参数/打印不变：

```go
package main

import (
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/pkg/sftp"
)

func main() {
	host := "127.0.0.1"
	port := "22"

	// 1) -s 后置写法：ssh <host> -s sftp
	// 2) -s 前置写法：ssh -s <host> sftp
	for _, mode := range []string{"post", "pre"} {
		args := []string{"-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=no", "-p", port}
		if mode == "pre" {
			args = append(args, "-s")
		}
		args = append(args, host)
		if mode == "post" {
			args = append(args, "-s")
		}
		args = append(args, "sftp")
		cmd := exec.Command("ssh", args...)
		w, _ := cmd.StdinPipe()
		r, _ := cmd.StdoutPipe()
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			fmt.Printf("[%s] start err: %v\n", mode, err)
			continue
		}
		cl, err := sftp.NewClientPipe(r, w)
		if err != nil {
			fmt.Printf("[%s] NewClientPipe err: %v\n", mode, err)
			_ = cmd.Process.Kill()
			continue
		}
		wd, _ := cl.Getwd()
		fmt.Printf("[%s] client ok, wd=%s\n", mode, wd)
		_ = cl.Close()
		_ = cmd.Wait()
	}

	// 3) posix-rename 是否覆盖已存在目标 + 4) Seek 续写
	cmd := exec.Command("ssh", "-o", "BatchMode=yes", "-p", port, host, "-s", "sftp")
	w, _ := cmd.StdinPipe()
	r, _ := cmd.StdoutPipe()
	cmd.Stderr = os.Stderr
	_ = cmd.Start()
	cl, err := sftp.NewClientPipe(r, w)
	if err != nil {
		panic(err)
	}
	_, has := cl.HasExtension("posix-rename@openssh.com")
	fmt.Printf("has posix-rename ext=%v\n", has)

	dir := "/tmp/probe-" + fmt.Sprint(time.Now().UnixNano())
	_ = cl.MkdirAll(dir)
	src := dir + "/src.txt"
	old := dir + "/old.txt"
	if err := cl.WriteFile(src, []byte("NEW")); err != nil {
		panic(err)
	}
	if err := cl.WriteFile(old, []byte("OLD")); err != nil {
		panic(err)
	}
	// 直接 Rename（plain SSH_FXP_RENAME）到已存在目标，看是否失败
	fmt.Printf("plain Rename over existing -> %v\n", cl.Rename(src, old))
	if b, err := cl.ReadFile(old); err == nil {
		fmt.Printf("after plain rename, old.txt=%q\n", string(b))
	}
	// PosixRename 到已存在目标
	_ = cl.WriteFile(src, []byte("NEW2"))
	fmt.Printf("PosixRename over existing -> %v\n", cl.PosixRename(src, old))
	if b, err := cl.ReadFile(old); err == nil {
		fmt.Printf("after PosixRename, old.txt=%q\n", string(b))
	}
	// Seek 续写：写 5 字节，Seek(3)，再写 "XY"，期望前 3 字节保留
	p := dir + "/seek.txt"
	f, err := cl.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		panic(err)
	}
	_, _ = f.Write([]byte("ABCDE"))
	_ = f.Close()
	f2, err := cl.OpenFile(p, os.O_WRONLY)
	if err != nil {
		panic(err)
	}
	if _, err := f2.Seek(3, 0); err != nil {
		fmt.Printf("Seek err: %v\n", err)
	}
	_, _ = f2.Write([]byte("XY"))
	_ = f2.Close()
	b, _ := cl.ReadFile(p)
	fmt.Printf("after Seek(3)+write, content=%q (expect \"ABCXY\")\n", string(b))

	_ = cl.RemoveDirectory(dir)
	_ = cl.Close()
}
```

```go
// go.mod
module probe

go 1.26

require github.com/pkg/sftp v1.13.11
```

构建（客户机上，隔离模块缓存）：`go mod tidy && go build -o probe.exe .`

- [ ] **Step 3: 跑探测并把输出贴进证据文件**

```powershell
.\probe.exe 2>&1 | Tee-Object -FilePath probe-out.txt
```

判定表（逐行对照，写入 `probe.md`）：

| 观察项 | 期望（Linux 参考） | 若不符 |
|---|---|---|
| `[pre]/[post] client ok` | 两种写法至少一种成功 | 只用成功的那种写进 `D1`（改 spec §4 D1 的 args 顺序 + Task 6 的 dial） |
| `has posix-rename ext` | `true` | `false` ⇒ 上传提交一律 backup-swap（Task 8 的分支即默认路径） |
| `plain Rename over existing` | 非 nil error | 若为 nil（覆盖），说明该服务端 rename 会覆盖，Task 13 的 `Rename` 可放宽，但仍优先 `PosixRename` |
| `after PosixRename, old.txt="NEW2"` | 是（覆盖成立） | 若仍是 `"OLD"` 或报错 ⇒ backup-swap 成为 Windows 常态路径，D15 的 Windows 结论要回写 spec |
| `after Seek(3)+write, content="ABCXY"` | 是 | 否则上传续传在 Windows 不可用 ⇒ 立刻停并回到 spec（上传续传要改为"整份重传"） |

- [ ] **Step 4: 清理客户机并落盘证据**

删除投放目录与临时文件、退出 VM（`VBoxManage controlvm win10 poweroff`），把 `probe.md`（含原始输出与判定）写入 SDD 工作区。

- [ ] **Step 5: 提交证据文件（不入库则跳过）**

若 `.superpowers/` 被 gitignore，则本 task 无 git 提交；在 `probe.md` 末尾记录"已 gitignore，仅本地留档"。

---

## Task 1: 依赖升级 + 传输开关（配置三处 / env / 懒解析选择器 / 前端 load-save）

**Files:**
- Modify: `go.mod`、`go.sum`
- Modify: `internal/config/store.go`（`AppSettings` 字段 `:17-24`、`Normalize` `:28`、`DefaultAppConfig` `:172-183` —— 三处）
- Create: `internal/sftp/backend.go`
- Test: `internal/config/store_test.go`（追加）、`internal/sftp/backend_test.go`（新建）
- Modify: `frontend/src/stores/settings.js`（load `:82-93` / save `:94-103`）
- Modify: `frontend/src/components/SettingsDialog.vue`（注意是 `components/` 不是 `views/`）
- Regenerate: `frontend/wailsjs/go/models.ts`

**Interfaces:**
- Consumes: 无
- Produces: `config.AppSettings.SftpTransport string`；`sftp.TransportSelector func() string`；`sftp.resolveTransport(sel TransportSelector) BackendKind`；`sftp.KindBatch/KindGo`

- [ ] **Step 1: 写失败测试（config 三处 + 选择器）**

`internal/config/store_test.go` 追加：

```go
func TestSftpTransportNormalizeAndDefault(t *testing.T) {
	c := DefaultAppConfig()
	if c.App.SftpTransport != "" {
		t.Fatalf("默认应为空（= 走内置默认），got %q", c.App.SftpTransport)
	}
	// 注意：生效入口是导出的 AppSettings.Normalize()（store.go:28）；AppConfig.normalize() 未导出（:156）
	c.App.SftpTransport = "  GoSftp "
	c.App.Normalize()
	if c.App.SftpTransport != "gosftp" {
		t.Fatalf("应归一化为小写去空格，got %q", c.App.SftpTransport)
	}
	c.App.SftpTransport = "nonsense"
	c.App.Normalize()
	if c.App.SftpTransport != "" {
		t.Fatalf("非法值应回落空串，got %q", c.App.SftpTransport)
	}
}
```

`internal/sftp/backend_test.go`（新建）：

```go
package sftp

import "testing"

func TestResolveTransportPrecedence(t *testing.T) {
	t.Setenv("SSHORE_SFTP_TRANSPORT", "")
	if got := resolveTransport(func() string { return "gosftp" }); got != KindGo {
		t.Fatalf("config 生效时 want KindGo, got %v", got)
	}
	t.Setenv("SSHORE_SFTP_TRANSPORT", "batch")
	if got := resolveTransport(func() string { return "gosftp" }); got != KindBatch {
		t.Fatalf("env 应覆盖 config, got %v", got)
	}
	t.Setenv("SSHORE_SFTP_TRANSPORT", "  GOSFTP ")
	if got := resolveTransport(func() string { return "batch" }); got != KindGo {
		t.Fatalf("env 应 trim+小写, got %v", got)
	}
}

func TestResolveTransportInvalidAndNilFallsBack(t *testing.T) {
	t.Setenv("SSHORE_SFTP_TRANSPORT", "whatever")
	if got := resolveTransport(nil); got != defaultTransport {
		t.Fatalf("非法 env + nil 选择器应回落 defaultTransport, got %v", got)
	}
	t.Setenv("SSHORE_SFTP_TRANSPORT", "")
	if got := resolveTransport(func() string { return "  " }); got != defaultTransport {
		t.Fatalf("空 config 应回落 defaultTransport, got %v", got)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/config/ -run TestSftpTransport -v && go test ./internal/sftp/ -run TestResolveTransport -v`
Expected: FAIL —— `c.App.SftpTransport undefined`（字段未定义）/ `undefined: resolveTransport`（两处都要真跑，确认是这两条而不是别的编译错）

- [ ] **Step 3: 实现配置字段与选择器**

`internal/config/store.go`：在 `AppSettings` 里紧邻 `AutoReconnectDefault` 加字段：

```go
	// SftpTransport 选择 SFTP 传输后端："" / "auto" = 内置默认；"batch" = 旧的 sftp -b；"gosftp" = 新底座。
	SftpTransport string `toml:"sftp_transport" json:"sftp_transport"`
```

在 `AppSettings.Normalize()` 里加（放在末尾，保持既有顺序不变）：

```go
	switch strings.ToLower(strings.TrimSpace(a.SftpTransport)) {
	case "batch":
		a.SftpTransport = "batch"
	case "gosftp":
		a.SftpTransport = "gosftp"
	default:
		a.SftpTransport = "" // "" / "auto" / 非法值都回落内置默认
	}
```

`DefaultAppConfig()` 的 `AppSettings{...}` 字面量里显式写 `SftpTransport: ""`（与 spec D3 的"三处都要改"一致）。

`internal/sftp/backend.go`：

```go
package sftp

import (
	"os"
	"strings"
)

// BackendKind 是传输后端的种类。
type BackendKind int

const (
	KindBatch BackendKind = iota // 旧的 sftp -b 实现
	KindGo                       // 新的 pkg/sftp 实现
)

// defaultTransport 是内置默认。Task 16 真机验收通过后改成 KindGo。
const defaultTransport = KindBatch

// TransportSelector 由 app.go 注入，懒解析配置（避免依赖 Init/startup 的先后顺序）。
type TransportSelector func() string

// resolveTransport 的优先级：环境变量 > 配置 > 内置默认。非法值一律回落默认。
func resolveTransport(sel TransportSelector) BackendKind {
	if v := strings.ToLower(strings.TrimSpace(os.Getenv("SSHORE_SFTP_TRANSPORT"))); v != "" {
		switch v {
		case "batch":
			return KindBatch
		case "gosftp":
			return KindGo
		}
	}
	if sel != nil {
		switch strings.ToLower(strings.TrimSpace(sel())) {
		case "batch":
			return KindBatch
		case "gosftp":
			return KindGo
		}
	}
	return defaultTransport
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/config/ -run TestSftpTransport -v && go test ./internal/sftp/ -run TestResolveTransport -v`
Expected: PASS（两个包）

- [ ] **Step 5: 升级依赖并复验双平台构建**

```bash
# 注意（Task 1 实测绘出的计划缺陷，已裁决）：本 task 没有任何代码 import pkg/sftp（Task 6 才 import），
# 因此 go mod tidy 会把 pkg/sftp 与 kr/fs 当未使用依赖删掉。处置：go get -> go mod tidy -> 再 go get，
# 让依赖留在 go.mod（会标 // indirect，不影响 build/vet/test/CI）；Task 6 首次 import 后再跑一次 go mod tidy
# 使其转正并去掉 // indirect。不要为了 tidy 稳定而加临时的空 import。
go get github.com/pkg/sftp@v1.13.11
go mod tidy
go get github.com/pkg/sftp@v1.13.11
grep -E "pkg/sftp|x/crypto|x/sys" go.mod
GOOS=windows GOARCH=amd64 go build ./internal/... && echo "windows internal ok"
go vet ./...
```

Expected: `go.mod` 出现 `github.com/pkg/sftp v1.13.11`，`x/crypto` 抬到 `v0.54.0`、`x/sys` 抬到 `v0.47.0`；windows 构建通过。若 `go build` 因 wails/webview2 标签失败，退化为 `GOOS=windows GOARCH=amd64 go vet ./internal/...` 并要求 Task 16 的 CI `build-windows` 通过。

- [ ] **Step 6: （已移到 Task 5）选择器注入**

**自审修正**：`NewCtrl` 加第三参数与 `app.go` 注入必须与 Task 5 的「门面 + 36 处构造替换」同批完成 —— 否则 Task 1 结束时 `make ci` 会因为 `NewCtrl` 仍是两参而编译失败。本 step 只留下待 Task 5 使用的代码片段（不要现在改 `app.go`）：

**Task 5 里 `app.go` 的 `Init`（`:123-130`）把

```go
	a.sftp = sftp.NewCtrl(osutil.NewRunner(), emit)
```

改成

```go
	a.sftp = sftp.NewCtrl(osutil.NewRunner(), emit, func() string {
		if a.cfg == nil { // app_test 直接构造时 cfg 可能为 nil（spec §12.3 的 R11）
			return ""
		}
		return a.cfg.App.SftpTransport
	})
```

（`NewCtrl` 的第三参数在 Task 5 落地；本 task 先让 `backend.go` 编译通过并保留 `resolveTransport` 备用。）

- [ ] **Step 7: 前端 load/save 带上新键 + 设置项下拉 + 重生成绑定**

`frontend/src/stores/settings.js`：state 加 `sftpTransport: ''`；`load()` 里加 `this.sftpTransport = s.sftp_transport || ''`；`save()` 的 `SetSettings({...})` 载荷里加 `sftp_transport: this.sftpTransport`。**这一步是硬要求**：`app.go:230-240` 的 `SetSettings` 是 `a.cfg.App = s` 整结构覆盖，漏了它用户每次保存设置都会把开关清空（spec R14）。

`frontend/src/components/SettingsDialog.vue`（自审 S10：路径是 `components/`；组件用 Pinia `store`，`:8`）：模板里加一个下拉，并**必须把 `store.sftpTransport` 加进 `:20-29` 的自动保存 watch 数组** —— 不加就永远不会落盘（等价 R14）：

```vue
<label class="row">
  <span>SFTP 传输后端</span>
  <select v-model="store.sftpTransport">
    <option value="">自动（默认）</option>
    <option value="gosftp">新实现（实验）</option>
    <option value="batch">旧实现（兼容）</option>
  </select>
</label>
```

```js
// SettingsDialog.vue 的 watch 源数组（原来是 5 个字段）
watch(
  () => [store.theme, store.fontScale, store.latinFont, store.cjkFont, store.autoStartOnLaunch, store.sftpTransport],
  async () => { /* 既有实现不变 */ }
)
```

**回归用例（对应 R14，自审 M6）**，加到现成的 `frontend/src/stores/settings.test.js`（已有 `vi.mock` 骨架）：

```js
it('save 必须带上 sftp_transport，否则 SetSettings 整结构覆盖会把它清空', async () => {
  const store = useSettingsStore()
  store.sftpTransport = 'gosftp'
  await store.save()
  expect(backend.saved.at(-1).sftp_transport).toBe('gosftp')
})
it('load 读回 sftp_transport', async () => {
  backend.settings = { sftp_transport: 'batch' }
  const store = useSettingsStore()
  await store.load()
  expect(store.sftpTransport).toBe('batch')
})
```

然后重生成绑定并构建：

```bash
wails generate module          # 重新生成 frontend/wailsjs（make 的 build 目标带 -skipbindings，必须显式跑）
cd frontend && npm run build && npx vitest run
```

Expected: 构建通过；vitest 98 passed。

- [ ] **Step 8: 提交**

```bash
git add go.mod go.sum internal/config/store.go internal/config/store_test.go internal/sftp/backend.go internal/sftp/backend_test.go frontend/src/stores/settings.js frontend/src/components/SettingsDialog.vue frontend/src/stores/settings.test.js frontend/wailsjs
git commit -m "feat(sftp): 传输后端开关（配置+环境变量）与 pkg/sftp 依赖升级"
```

---

## Task 2: 内部临时文件命名与匹配（`partname.go`）

**Files:**
- Create: `internal/sftp/partname.go`
- Test: `internal/sftp/partname_test.go`

**Interfaces:**
- Consumes: 无
- Produces: `const PartMarker = ".sshore-sftppart-"`（len=17）；**本地**（OS 原生分隔符，用 `filepath`）：`PartName(target, id)` / `BakName(target)` / `ShortPartName(id)`；**远端 POSIX**（用 `path`，Windows 客户端上 `filepath.Join/Clean` 会把 `/` 变成 `\`）：`PartNameRemote(target, id)` / `BakNameRemote(target)`；`IsInternalTemp(name) bool`（中缀 Contains + `filepath.Base`）。
- **远端家族的变异保护**：`path` 与 `filepath` 的差异只在 Windows 可观测，而 CI 的 **go-windows job 在 windows-latest 上跑 `go test ./... -count=1`**（`.github/workflows/ci.yml:57-59`）—— 因此「远端家族误用 filepath」的变异由 CI 自动杀死，本地 Linux 跑不出该变异是预期行为，不是缺口。
- **两套家族的原因（Task 2 评审 Important-2）**：远端路径永远是 POSIX；若用 `filepath` 生成远端临时/备份名，Windows 客户端上的退化短名会落到别处（`\data\.sshore-sftppart-…`），破坏「同目录/rename 原子」。本地用 `filepath`、远端用 `path`，各有单测。

- [ ] **Step 1: 写失败测试**

```go
package sftp

import (
	"strings"
	"testing"
)

func TestPartNameKeepsTargetPrefixAndMarker(t *testing.T) {
	got := PartName("/data/a.txt", "t1-3")
	if !strings.HasPrefix(got, "/data/a.txt"+PartMarker) {
		t.Fatalf("临时名应保留原名并接中缀，got %q", got)
	}
	if !IsInternalTemp(got) {
		t.Fatalf("IsInternalTemp 必须认出常规名，got %q", got)
	}
	if PartName("/data/a.txt", "t1-3") == PartName("/data/a.txt", "t1-3") {
		t.Fatal("同名两次调用必须不同（随机后缀），否则并发/重试会互相踩")
	}
}

func TestPartNameFallsBackToShortFormWhenTooLong(t *testing.T) {
	long := "/data/" + strings.Repeat("x", 240) + ".bin"
	got := PartName(long, "abcdef123456")
	if len(got) > partNameMax {
		t.Fatalf("超长目标必须退化，got len=%d", len(got))
	}
	// 边界必须钉住（技术审核 M6）：恰好 200 字节不退化、201 字节必须退化
	id8 := "abcdef12"
	exact := "/data/" + strings.Repeat("y", partNameMax-len("/data/")-len(PartMarker)-len(id8)-1-6)
	short := strings.Replace(exact, "yyyyy", "yyyyyyy", 1)
	if n := len(PartName(exact, id8)); n > partNameMax {
		t.Fatalf("恰好在阈值内不应退化，got len=%d", n)
	}
	if n := len(PartName(short, id8)); n > partNameMax {
		t.Fatalf("超过阈值必须退化，got len=%d", n)
	}
	if !IsInternalTemp(got) {
		t.Fatalf("退化短名也必须被 IsInternalTemp 认出（中缀判定），got %q", got)
	}
	if !strings.HasPrefix(got, "/data/") {
		t.Fatalf("退化短名必须留在同目录（rename 才能原子），got %q", got)
	}
}

func TestBakNameIsInternalTempToo(t *testing.T) {
	got := BakName("/data/a.txt")
	if !IsInternalTemp(got) {
		t.Fatalf("bak 必须复用同一中缀，否则成孤儿（spec M2），got %q", got)
	}
}

func TestIsInternalTempRejectsNormalFiles(t *testing.T) {
	for _, n := range []string{"a.txt", "a.txt.bak", ".sshore-part-abc-1", "sshore-sftppart"} {
		if IsInternalTemp(n) {
			t.Fatalf("普通文件 %q 不应被判为内部临时文件", n)
		}
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/sftp/ -run 'TestPartName|TestBakName|TestIsInternalTemp' -v`
Expected: FAIL —— `undefined: PartName`

- [ ] **Step 3: 实现**

`internal/sftp/partname.go`：

```go
package sftp

import (
	"crypto/rand"
	"encoding/hex"
	"path/filepath"
	"strings"
)

// PartMarker 是内部临时文件的中缀（唯一来源）。前端有同名同值常量，两边各自单测钉住字面量。
const PartMarker = ".sshore-sftppart-"

// partNameMax 是临时名长度阈值（字节）。超过就退化为目录内短名，避免 ENAMETOOLONG。
const partNameMax = 200

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "000000000000"
	}
	return hex.EncodeToString(b)[:n]
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	if id == "" {
		return "anon"
	}
	return id
}

// PartName 返回 <target> + 中缀 + <短id>-<随机>；过长时退化为同目录短名。
func PartName(target, id string) string {
	name := target + PartMarker + shortID(id) + "-" + randHex(6)
	if len(name) <= partNameMax {
		return name
	}
	return filepath.Join(filepath.Dir(target), ShortPartName(id))
}

// ShortPartName 是退化短名（以点开头，天然隐藏；仍由中缀判定认出）。
func ShortPartName(id string) string {
	return PartMarker + shortID(id) + "-" + randHex(6)
}

// BakName 是 backup-swap 的备份名，复用同一中缀，保证清理/过滤/忽略一套规则覆盖它。
func BakName(target string) string {
	name := target + PartMarker + "bak-" + randHex(6)
	if len(name) <= partNameMax {
		return name
	}
	return filepath.Join(filepath.Dir(target), PartMarker+"bak-"+randHex(6))
}

// IsInternalTemp 用中缀判定：常规名（<name>.sshore-sftppart-…）与退化短名（.sshore-sftppart-…）都命中。
// 不能用 strings.HasPrefix(name, PartMarker)，常规名不以它开头。
func IsInternalTemp(name string) bool {
	return strings.Contains(filepath.Base(name), PartMarker)
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/sftp/ -run 'TestPartName|TestBakName|TestIsInternalTemp' -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/sftp/partname.go internal/sftp/partname_test.go
git commit -m "feat(sftp): 内部临时文件命名与中缀匹配（长名退化 + bak 同中缀）"
```

---

## Task 3: raw-pipe 运行原语（`osutil.StartPipes` + stderr 有界 drain）

**Files:**
- Modify: `internal/osutil/runner.go`（追加，不改既有 `StartStream`）
- Test: `internal/osutil/runner_test.go`（追加）

**Interfaces:**
- Consumes: 无
- Produces: `type PipedProcess struct { Stdin io.WriteCloser; Stdout io.ReadCloser; Stderr io.ReadCloser }`；`func StartPipes(name string, args ...string) (*PipedProcess, error)`；`(p *PipedProcess) StderrText() string`；`Wait() Outcome`；`Kill() error`；`Close() error`

- [ ] **Step 1: 写失败测试（含"不读 stderr 也不阻塞"）**

`internal/osutil/runner_test.go` 追加（若文件还没导入，补 `io` / `os/exec`）：

```go
func TestStartPipesRoundTrip(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("缺少 sh")
	}
	p, err := StartPipes("sh", "-c", "cat")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if _, err := p.Stdin.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	_ = p.Stdin.Close()
	buf := make([]byte, 5)
	if _, err := io.ReadFull(p.Stdout, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "hello" {
		t.Fatalf("want hello, got %q", buf)
	}
}

// 关键：stderr 写了 200KB 而调用方从不读它，进程仍必须正常结束（否则 64KB 管道写满会让 ssh 假死）。
func TestStartPipesDoesNotBlockWhenStderrUnread(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("缺少 sh")
	}
	p, err := StartPipes("sh", "-c", "head -c 200000 /dev/zero >&2; echo done")
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	buf := make([]byte, 4)
	if _, err := io.ReadFull(p.Stdout, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "done" {
		t.Fatalf("want done, got %q", buf)
	}
	out := p.Wait()
	if out.ExitCode != 0 {
		t.Fatalf("exit=%d stderr=%q", out.ExitCode, p.StderrText())
	}
	if len(p.StderrText()) == 0 {
		t.Fatal("stderr 应被后台 drain 到有界缓冲，供错误上报")
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/osutil/ -run TestStartPipes -v`
Expected: FAIL —— `undefined: StartPipes`

- [ ] **Step 3: 实现**

`internal/osutil/runner.go` 追加：

```go
// PipedProcess 是长驻子进程的裸管道句柄（供二进制协议使用，例如 ssh -s sftp）。
type PipedProcess struct {
	Stdin  io.WriteCloser
	Stdout io.ReadCloser
	Stderr io.ReadCloser

	proc *Process

	mu     sync.Mutex
	errBuf []byte // 有界 stderr 环形缓冲（最近 16KB）
}

const pipedStderrKeep = 16 * 1024

// StartPipes 起一个长驻子进程并交出三路裸管道。
// stderr 必须由本原语自己 drain —— 调用方不读也不会把 64KB 管道写满而假死。
func StartPipes(name string, args ...string) (*PipedProcess, error) {
	cmd := exec.Command(name, args...)
	procAttrHideConsole(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		return nil, err
	}
	p := &PipedProcess{Stdin: stdin, Stdout: stdout, Stderr: stderr, proc: &Process{cmd: cmd, done: make(chan Outcome, 1)}}
	var drain sync.WaitGroup
	drain.Add(1)
	go func() {
		defer drain.Done()
		buf := make([]byte, 4096)
		for {
			n, rerr := stderr.Read(buf)
			if n > 0 {
				p.mu.Lock()
				p.errBuf = append(p.errBuf, buf[:n]...)
				if len(p.errBuf) > pipedStderrKeep {
					p.errBuf = p.errBuf[len(p.errBuf)-pipedStderrKeep:]
				}
				p.mu.Unlock()
			}
			if rerr != nil {
				break
			}
		}
		_ = stderr.Close()
	}()
	// 与 StartStream 同序：先等 stderr drain 到 EOF，再 cmd.Wait()。
	// 反过来会让 Wait 关闭父端管道、截断 stderr，且与读取并发（-race 会抓）。
	go func() {
		drain.Wait()
		err := cmd.Wait()
		p.proc.done <- Outcome{ExitCode: execResult(err)}
		close(p.proc.done)
	}()
	return p, nil
}

// StderrText 返回后台 drain 到的 stderr 尾部（错误上报的唯一来源）。
func (p *PipedProcess) StderrText() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return strings.TrimSpace(string(p.errBuf))
}

func (p *PipedProcess) Wait() Outcome { return p.proc.Wait() }

func (p *PipedProcess) Kill() error { return p.proc.Kill() }

// Signal 发送优雅中断（SIGINT/CTRL_BREAK），与 Process.Signal 同语义（spec §12.3 g）。
func (p *PipedProcess) Signal() error { return p.proc.Signal() }

// Close 关闭管道并终止子进程；调用方在传输结束后必须调用，避免长驻 ssh 泄漏。
// 幂等（Task 3 实测）：子进程已正常退出时 Kill 返回 os.ErrProcessDone，这不是错误 ——
// 否则 Task 6 每次正常收尾都会拿到假错误。调用点不需要 whitelist。
func (p *PipedProcess) Close() error {
	_ = p.Stdin.Close()
	_ = p.Stdout.Close()
	// F1（Task 3 评审）：**必须也关父端 stderr 读端**。drain 的 Read 只在写端全关后才 EOF；
	// 若后代进程仍持有 fd 2（sleep &、ProxyCommand、ControlPersist 等），不关读端会把
	// drain → drain.Wait() → cmd.Wait() 整条链无限挂住。关读端会让 Read 立刻返回错误、解除阻塞。
	// 注意：本原语不保证杀死后代进程（只杀直接子进程）。
	_ = p.Stderr.Close()
	if err := p.proc.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/osutil/ -run TestStartPipes -v -race`
Expected: PASS（两个用例）

- [ ] **Step 5: 提交**

```bash
git add internal/osutil/runner.go internal/osutil/runner_test.go
git commit -m "feat(osutil): 新增裸管道长驻原语 StartPipes（stderr 有界 drain，防假死）"
```

---

## Task 4: 会话池状态机（`session.go`）

**Files:**
- Create: `internal/sftp/session.go`
- Test: `internal/sftp/session_test.go`

**Interfaces:**
- Consumes: `osutil.PipedProcess`（Task 3）
- Produces: `type DialFunc func(host, user string) (*Session, error)`（`Session` 内含 `*sftp.Client` 与 `*osutil.PipedProcess`）；`func NewPool(d DialFunc) *Pool`；`(p *Pool) AcquireList(ctx context.Context, host, user string) (*Session, error)`；`(p *Pool) AcquireTransfer(ctx context.Context, host, user string) (*Session, error)`；`(p *Pool) Release(s *Session, reusable bool)`；`(p *Pool) Probe(ctx context.Context, host, user string) bool`；`(p *Pool) Disconnect(host string) error`；`(p *Pool) CloseAll()`

- [ ] **Step 1: 写失败测试（并发 1 / idle LRU / Disconnect 只关空闲）**

`internal/sftp/session_test.go`：

```go
package sftp

import (
	"context"
	"testing"
	"time"
)

// fakeSession 让测试完全不碰网络。
type fakeSession struct{ host string }

func newTestPool() (*Pool, *int) {
	dialed := 0
	p := NewPool(func(host, user string) (*Session, error) {
		dialed++
		return &Session{Host: host, User: user}, nil
	})
	return p, &dialed
}

func TestAcquireTransferSerializesToOne(t *testing.T) {
	p, _ := newTestPool()
	defer p.CloseAll()
	a, err := p.AcquireTransfer(context.Background(), "h1", "u")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		b, err := p.AcquireTransfer(context.Background(), "h1", "u")
		if err == nil {
			p.Release(b, false)
		}
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("并发上限为 1：第二个传输必须排队")
	case <-time.After(50 * time.Millisecond):
	}
	p.Release(a, false)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("释放后第二个传输应立刻拿到会话")
	}
}

func TestAcquireListNeverQueuesAndCapsIdle(t *testing.T) {
	p, dialed := newTestPool()
	defer p.CloseAll()
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		s, err := p.AcquireList(ctx, "h1", "u")
		if err != nil {
			t.Fatal(err)
		}
		p.Release(s, true) // 空闲复用
	}
	// 传输占满并发额度时，列表仍必须成功且不排队
	held, _ := p.AcquireTransfer(ctx, "h1", "u")
	defer p.Release(held, false)
	if _, err := p.AcquireList(ctx, "h2", "u"); err != nil {
		t.Fatalf("列表不得因池满失败：%v", err)
	}
	if *dialed > 8 {
		t.Fatalf("idle 池上限 2 应触发回收，dialed=%d", *dialed)
	}
}

func TestDisconnectOnlyClosesIdle(t *testing.T) {
	p, _ := newTestPool()
	defer p.CloseAll()
	busy, _ := p.AcquireTransfer(context.Background(), "h1", "u")
	if err := p.Disconnect("h1"); err != nil {
		t.Fatal(err)
	}
	if busy.Closed() {
		t.Fatal("Disconnect 不得关闭进行中的传输会话")
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/sftp/ -run 'TestAcquire|TestDisconnectOnlyClosesIdle' -v`
Expected: FAIL —— `undefined: NewPool`

- [ ] **Step 3: 实现**

`internal/sftp/session.go`：

```go
package sftp

import (
	"context"
	"sync"
	"time"

	"github.com/pkg/sftp"

	"sshore/internal/osutil"
)

type sessionState int

const (
	sessIdle sessionState = iota
	sessBusy
	sessClosing
	sessDead
)

const (
	probeTimeout   = 5 * time.Second
	shutdownGrace  = 5 * time.Second
	maxIdlePerHost = 2
)

// Session 是一条 ssh -s sftp 长驻会话。
type Session struct {
	Host  string
	User  string
	Conn  *sftp.Client
	Proc  *osutil.PipedProcess
	state sessionState
	last  time.Time
	mu    sync.Mutex
}

func (s *Session) Closed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state == sessDead || s.state == sessClosing
}

// setState 是唯一写 state 的入口（技术审核 S7：Release 里裸写 state 与 Closed/close 构成数据竞争，-race 已复现）。
func (s *Session) setState(st sessionState) {
	s.mu.Lock()
	s.state = st
	s.last = time.Now()
	s.mu.Unlock()
}

// fromTransfer 标记该会话是否占用了传输额度（技术审核 S8：列表会话 Release 不得归还额度，否则并发 1 被突破）。
func (s *Session) markTransfer() { s.mu.Lock(); s.transfer = true; s.mu.Unlock() }
func (s *Session) isTransfer() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.transfer }

func (s *Session) close() {
	s.mu.Lock()
	s.state = sessClosing
	s.mu.Unlock()
	if s.Conn != nil {
		_ = s.Conn.Close()
	}
	if s.Proc != nil {
		_ = s.Proc.Close()
		done := make(chan struct{})
		go func() { _ = s.Proc.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(shutdownGrace):
			_ = s.Proc.Kill()
		}
	}
	s.mu.Lock()
	s.state = sessDead
	s.mu.Unlock()
}

// DialFunc 由 GoBackend 提供（起 ssh -s sftp + NewClientPipe）。
type DialFunc func(host, user string) (*Session, error)

type Pool struct {
	mu    sync.Mutex
	dial  DialFunc
	idle  map[string][]*Session
	transfers int
	queue chan struct{} // 传输并发额度（容量 1）
	all   map[*Session]struct{}
}

func NewPool(d DialFunc) *Pool {
	q := make(chan struct{}, 1)
	q <- struct{}{}
	return &Pool{dial: d, idle: map[string][]*Session{}, queue: q, all: map[*Session]struct{}{}}
}

func (p *Pool) track(s *Session) *Session {
	p.mu.Lock()
	p.all[s] = struct{}{}
	p.mu.Unlock()
	return s
}

// AcquireTransfer：传输并发上限 1，超出 FIFO 排队；每个传输独占新会话。
func (p *Pool) AcquireTransfer(ctx context.Context, host, user string) (*Session, error) {
	select {
	case <-p.queue:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	s, err := p.dial(host, user)
	if err != nil {
		p.queue <- struct{}{}
		return nil, err
	}
	// 技术审核 S9：拿到额度后 ctx 可能已取消 —— 此时必须归还额度并关掉刚建的会话，否则双泄漏。
	if cerr := ctx.Err(); cerr != nil {
		s.close()
		p.queue <- struct{}{}
		return nil, cerr
	}
	s.markTransfer()
	s.setState(sessBusy)
	return p.track(s), nil
}

// AcquireList：优先复用 idle；池空则新建；超出 idle 上限关最久未用者。
// 绝不排队、绝不因池满失败（spec D2）。
// 注意：调用方一律传 context.Background()，不要传 nil（Probe 会对 ctx 做 WithTimeout）。
func (p *Pool) AcquireList(ctx context.Context, host, user string) (*Session, error) {
	key := host + "\x00" + user
	p.mu.Lock()
	if lst := p.idle[key]; len(lst) > 0 {
		s := lst[len(lst)-1]
		p.idle[key] = lst[:len(lst)-1]
		s.state = sessBusy
		p.mu.Unlock()
		return s, nil
	}
	p.mu.Unlock()
	s, err := p.dial(host, user)
	if err != nil {
		return nil, err
	}
	s.state = sessBusy
	return p.track(s), nil
}

// Release：reusable=true 时把会话放回 idle（并按上限 LRU 关闭多余），否则直接关。
func (p *Pool) Release(s *Session, reusable bool) {
	if s == nil {
		return
	}
	// 只有传输会话才归还并发额度；列表会话从不占用额度（技术审核 S8）。
	defer func() {
		if s.isTransfer() {
			p.refill()
		}
	}()
	if !reusable || s.Closed() {
		p.mu.Lock()
		delete(p.all, s)
		p.mu.Unlock()
		s.close()
		return
	}
	key := s.Host + "\x00" + s.User
	s.setState(sessIdle)
	p.mu.Lock()
	p.idle[key] = append(p.idle[key], s)
	var evict []*Session
	if len(p.idle[key]) > maxIdlePerHost {
		evict = append(evict, p.idle[key][0])
		p.idle[key] = p.idle[key][1:]
	}
	for e := range evict {
		delete(p.all, evict[e])
	}
	p.mu.Unlock()
	for _, e := range evict {
		e.close()
	}
}

// refill 归还一个传输并发额度（只由传输会话的 Release 触发；列表会话不占额度）。
func (p *Pool) refill() {
	select {
	case p.queue <- struct{}{}:
	default:
	}
}

// Probe 用库唯一 ctx 感知的 ReadDirContext 探活；超时即关掉并丢弃该会话。
func (p *Pool) Probe(ctx context.Context, host, user string) bool {
	cctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	s, err := p.AcquireList(cctx, host, user)
	if err != nil {
		return false
	}
	_, err = s.Conn.ReadDirContext(cctx, ".")
	p.Release(s, err == nil)
	return err == nil
}

// Disconnect 只关闭该 host 的空闲会话；进行中的传输不受影响。
func (p *Pool) Disconnect(host string) error {
	p.mu.Lock()
	var drop []*Session
	for key, lst := range p.idle {
		if len(key) >= len(host) && key[:len(host)] == host {
			drop = append(drop, lst...)
			delete(p.idle, key)
		}
	}
	for _, s := range drop {
		delete(p.all, s)
	}
	p.mu.Unlock()
	for _, s := range drop {
		s.close()
	}
	return nil
}

func (p *Pool) CloseAll() {
	p.mu.Lock()
	all := make([]*Session, 0, len(p.all))
	for s := range p.all {
		all = append(all, s)
	}
	p.all = map[*Session]struct{}{}
	p.idle = map[string][]*Session{}
	p.mu.Unlock()
	for _, s := range all {
		s.close()
	}
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/sftp/ -run 'TestAcquire|TestDisconnectOnlyClosesIdle' -v -race`
Expected: PASS；再跑 `go test ./internal/sftp/ -race` 确认既有测试未坏。

- [ ] **Step 5: 提交**

```bash
git add internal/sftp/session.go internal/sftp/session_test.go
git commit -m "feat(sftp): 会话池状态机（传输并发 1 / idle 上限 2 / 列表永不排队）"
```

---

## Task 5: 门面双面 + `NewBatchBackend` 更名 + 36 处测试机械替换

**Files:**
- Modify: `internal/sftp/ctrl.go`（拆出 batch 实现）
- Create: `internal/sftp/batch.go`
- Modify: `internal/sftp/search.go`（`Search` 接收者改名 + 抽 `searchBFS`）
- Modify: `internal/sftp/ctrl_test.go`（27 处）、`internal/sftp/listmany_test.go`（4 处）、`internal/sftp/search_test.go`（5 处）
- Modify: `internal/sync/e2e_test.go`（`:32`）、`app_test.go`（`:54`）—— 只在必要时

**Interfaces:**
- Consumes: `Backend`（Task 1 的 `backend.go` 先只有 `BackendKind`，本 task 补齐 `Backend` 接口）
- Produces: `func NewBatchBackend(r osutil.Runner, emit forward.EmitFunc) *BatchBackend`；`func NewCtrl(r osutil.Runner, emit forward.EmitFunc, sel TransportSelector) *Ctrl`；`(c *Ctrl) TransferGet/TransferGetTree/TransferPut/TransferPutTree(req TransferRequest, report func(Progress)) error`；`(c *Ctrl) Cancel(id string) bool`；legacy 四参面 `Get/GetRecursive/Put/PutRecursive/List/ListMany/Home/Remove/RemoveRecursive/Mkdir/Rename/Connected/Disconnect/CloseAll`

- [ ] **Step 1: 先补 `Backend` 接口与类型（`api.go`）**

`internal/sftp/api.go`（新建）：

```go
package sftp

import "fmt"

type Direction string

const (
	DirDownload Direction = "download"
	DirUpload   Direction = "upload"
)

type Phase string

const (
	PhaseScan     Phase = "scan"
	PhaseTransfer Phase = "transfer"
)

// Progress 是传输进度事件载荷（spec §6.1）。
type Progress struct {
	ID         string    `json:"id"`
	Host       string    `json:"host"`
	Direction  Direction `json:"direction"`
	Name       string    `json:"name"`
	PartPath   string    `json:"partPath"`
	Done       int64     `json:"done"`
	Total      int64     `json:"total"` // <0 表示未知
	FilesDone  int       `json:"filesDone"`
	FilesTotal int       `json:"filesTotal"` // <0 表示未知
	Phase      Phase     `json:"phase"`
}

// TransferRequest 是一次传输的输入（spec §6.1）。
type TransferRequest struct {
	ID       string
	Host     string
	User     string
	Remote   string
	Local    string
	Resume   bool
	PartPath string
	// ResumeOffset 是续传起点（下载=本地 .part 大小；上传=远端 .part 大小）。
	// 由 Task 11 的 decideResume 计算后填入，Task 7/8 用它 Seek。
	ResumeOffset int64
	Atomic       bool // true=新面：我方 .part + 提交；false=legacy：直写目标
}

// TransferError 统一错误形状，RemoteMsg 保留远端/ssh 原文（spec D12）。
type TransferError struct {
	Op        string
	Host      string
	Path      string
	RemoteMsg string
	Err       error
}

func (e *TransferError) Error() string {
	if e.RemoteMsg != "" {
		return fmt.Sprintf("%s %s: %s", e.Op, e.Path, e.RemoteMsg)
	}
	if e.Err != nil {
		return fmt.Sprintf("%s %s: %v", e.Op, e.Path, e.Err)
	}
	return fmt.Sprintf("%s %s 失败", e.Op, e.Path)
}

func (e *TransferError) Unwrap() error { return e.Err }

// Backend 是新传输面的最小契约；legacy 四参面保留在门面 Ctrl 上（Go 无重载）。
type Backend interface {
	List(host, user, path string) ([]Item, error)
	ListMany(host, user string, paths []string) (map[string][]Item, error)
	Home(host, user string) (string, error)
	// 方法名带 Transfer 前缀：BatchBackend 必须同时保留 legacy 四参的 Get/Put（Go 无重载，
	// 若接口也叫 Get(req,report) 就会与 legacy 版冲突 —— 技术审核 S3 的实测结论）。
	TransferGet(req TransferRequest, report func(Progress)) error
	TransferGetTree(req TransferRequest, report func(Progress)) error
	TransferPut(req TransferRequest, report func(Progress)) error
	TransferPutTree(req TransferRequest, report func(Progress)) error
	Remove(host, user, path string) error
	RemoveRecursive(host, user, path string) error
	Mkdir(host, user, path string) error
	Rename(host, user, oldPath, newPath string) error
	// Connect/Search 是门面与 app.go 需要的能力（自审 S2 补）。
	Connect(host, user string) error
	Search(ctx context.Context, host, user, root, pattern string, maxDepth, limit int, onProgress func(scanned int)) (SearchOutcome, error)
	Connected(host string) bool
	Disconnect(host string) error
	CloseAll()
}
```

- [ ] **Step 2: 把现有实现搬成 `BatchBackend` 并加门面**

1. `git mv internal/sftp/ctrl.go internal/sftp/batch.go`
2. 在 `batch.go` 里：`type BatchBackend struct` 替换 `type Ctrl struct`；`func NewBatchBackend(r osutil.Runner, emit forward.EmitFunc) *BatchBackend` 替换 `NewCtrl`；把每个 `func (c *Ctrl)` 改成 `func (c *BatchBackend)`；为 `Backend` 补四个新面方法（直写目标、无进度）：

```go
// 接口方法之一（其余三个同构）。legacy 四参的 Get/GetRecursive/Put/PutRecursive 同名保留。
func (b *BatchBackend) TransferGet(req TransferRequest, _ func(Progress)) error {
	return b.Get(req.Host, req.User, req.Remote, req.Local)
}
```

**legacy 名一律不改**（`Get/GetRecursive/Put/PutRecursive` 保持四参签名），只**新增**四个接口方法（技术审核 S3：改名会打断 `ctrl_test.go` 里 7 处 `c.Get/c.Put` 调用）：

```go
// 新增（不改名）：接口方法包装 legacy 四参实现；batch 不产生进度、不走 .part。
func (b *BatchBackend) TransferGet(req TransferRequest, _ func(Progress)) error {
	return b.Get(req.Host, req.User, req.Remote, req.Local)
}
func (b *BatchBackend) TransferGetTree(req TransferRequest, _ func(Progress)) error {
	return b.GetRecursive(req.Host, req.User, req.Remote, req.Local)
}
func (b *BatchBackend) TransferPut(req TransferRequest, _ func(Progress)) error {
	return b.Put(req.Host, req.User, req.Local, req.Remote)
}
func (b *BatchBackend) TransferPutTree(req TransferRequest, _ func(Progress)) error {
	return b.PutRecursive(req.Host, req.User, req.Local, req.Remote)
}
```

3. 新建 `internal/sftp/ctrl.go`（门面）：

```go
package sftp

import (
	"sshore/internal/forward"
	"sshore/internal/osutil"
)

// Ctrl 是门面：legacy 四参面（sync 与既有测试用）+ 新传输面（绑定层用）。
type Ctrl struct {
	batch     *BatchBackend
	sel       TransportSelector
	emit      forward.EmitFunc
	goBackend *GoBackend // 懒构造，见 backend()
}

// NewCtrl 保持 spec D3 的两参签名（env + 内置默认），既有调用点无需改动。
func NewCtrl(r osutil.Runner, emit forward.EmitFunc) *Ctrl {
	return &Ctrl{batch: NewBatchBackend(r, emit)}
}

// NewCtrlWith 由 app.go 注入懒解析选择器（spec D3）。
func NewCtrlWith(r osutil.Runner, emit forward.EmitFunc, sel TransportSelector) *Ctrl {
	c := NewCtrl(r, emit)
	c.sel = sel
	c.emit = emit
	return c
}

// —— legacy 四参面：签名逐字不变，但一律走 c.backend() 且 Atomic=false ——
// 自审 S11：若把 legacy 面硬绑 batch，则开关对 internal/sync 失效、后端矩阵名不副实、
// 且 v0.8 删掉 batch 后 sync 无处可去。Atomic=false = 直写目标（sync 自带 .part+rename）。

func (c *Ctrl) List(host, user, path string) ([]Item, error) { return c.backend().List(host, user, path) }
func (c *Ctrl) ListMany(host, user string, paths []string) (map[string][]Item, error) {
	return c.backend().ListMany(host, user, paths)
}
func (c *Ctrl) Home(host, user string) (string, error) { return c.backend().Home(host, user) }
func (c *Ctrl) Get(host, user, remote, local string) error {
	return c.backend().TransferGet(TransferRequest{Host: host, User: user, Remote: remote, Local: local}, nil)
}
func (c *Ctrl) GetRecursive(host, user, remote, local string) error {
	return c.backend().TransferGetTree(TransferRequest{Host: host, User: user, Remote: remote, Local: local}, nil)
}
func (c *Ctrl) Put(host, user, local, remote string) error {
	return c.backend().TransferPut(TransferRequest{Host: host, User: user, Local: local, Remote: remote}, nil)
}
func (c *Ctrl) PutRecursive(host, user, local, remoteDir string) error {
	return c.backend().TransferPutTree(TransferRequest{Host: host, User: user, Local: local, Remote: remoteDir}, nil)
}
func (c *Ctrl) Remove(host, user, path string) error          { return c.backend().Remove(host, user, path) }
func (c *Ctrl) RemoveRecursive(host, user, path string) error { return c.backend().RemoveRecursive(host, user, path) }
func (c *Ctrl) Mkdir(host, user, path string) error           { return c.backend().Mkdir(host, user, path) }
func (c *Ctrl) Rename(host, user, oldPath, newPath string) error {
	return c.backend().Rename(host, user, oldPath, newPath)
}

// Connect/Search 是 app.go 仍在用的方法（SftpConnect / SftpSearch），门面必须暴露。
func (c *Ctrl) Connect(host, user string) error { return c.backend().Connect(host, user) }
func (c *Ctrl) Connected(host string) bool       { return c.backend().Connected(host) }
func (c *Ctrl) Disconnect(host string) error     { return c.backend().Disconnect(host) }
func (c *Ctrl) Search(ctx context.Context, host, user, root, pattern string, maxDepth, limit int, onProgress func(scanned int)) (SearchOutcome, error) {
	return c.backend().Search(ctx, host, user, root, pattern, maxDepth, limit, onProgress)
}

// SetJournalDir 由 app.go 装配时用 stateDir() 注入（goBackend 懒构造时传给 NewGoBackend）。
func (c *Ctrl) SetJournalDir(dir string) { c.journalDir = dir }

func (c *Ctrl) CloseAll() { c.backend().CloseAll() }

// —— 新传输面：Task 6+ 接线到 GoBackend；现在先委派 batch 以保证可编译 ——

func (c *Ctrl) TransferGet(req TransferRequest, report func(Progress)) error {
	return c.backend().TransferGet(req, report)
}
func (c *Ctrl) TransferGetTree(req TransferRequest, report func(Progress)) error {
	return c.backend().TransferGetTree(req, report)
}
func (c *Ctrl) TransferPut(req TransferRequest, report func(Progress)) error {
	return c.backend().TransferPut(req, report)
}
func (c *Ctrl) TransferPutTree(req TransferRequest, report func(Progress)) error {
	return c.backend().TransferPutTree(req, report)
}
func (c *Ctrl) Cancel(id string) bool { return false } // Task 10 接线

func (c *Ctrl) backend() Backend { return c.batch } // Task 6 按 resolveTransport(c.sel) 分支
```

- [ ] **Step 3: 机械替换三处测试文件的构造函数**

```bash
sed -i 's/NewCtrl(/NewBatchBackend(/g' internal/sftp/ctrl_test.go internal/sftp/listmany_test.go internal/sftp/search_test.go
grep -c "NewBatchBackend(" internal/sftp/ctrl_test.go internal/sftp/listmany_test.go internal/sftp/search_test.go
```

- [ ] **Step 3b: 门面委派单测（自审 M7）**

`internal/sftp/ctrl_test.go` 追加：用 `fakeBackend` 断言 legacy 面真的走 `backend()` 且 `Atomic=false`（S11 的回归护栏）：

```go
type fakeBackend struct {
	lastReq TransferRequest
	called  string
}

func (f *fakeBackend) List(string, string, string) ([]Item, error) { f.called = "List"; return nil, nil }
func (f *fakeBackend) ListMany(string, string, []string) (map[string][]Item, error) { return nil, nil }
func (f *fakeBackend) Home(string, string) (string, error) { return "", nil }
func (f *fakeBackend) Get(req TransferRequest, _ func(Progress)) error {
	f.called, f.lastReq = "Get", req
	return nil
}
func (f *fakeBackend) GetTree(TransferRequest, func(Progress)) error  { f.called = "GetTree"; return nil }
func (f *fakeBackend) Put(TransferRequest, func(Progress)) error      { f.called = "Put"; return nil }
func (f *fakeBackend) PutTree(TransferRequest, func(Progress)) error  { f.called = "PutTree"; return nil }
func (f *fakeBackend) Remove(string, string, string) error            { return nil }
func (f *fakeBackend) RemoveRecursive(string, string, string) error   { return nil }
func (f *fakeBackend) Mkdir(string, string, string) error             { return nil }
func (f *fakeBackend) Rename(string, string, string, string) error    { return nil }
func (f *fakeBackend) Connect(string, string) error                   { return nil }
func (f *fakeBackend) Search(context.Context, string, string, string, string, int, int, func(int)) (SearchOutcome, error) {
	return SearchOutcome{}, nil
}
func (f *fakeBackend) Connected(string) bool   { return true }
func (f *fakeBackend) Disconnect(string) error { return nil }
func (f *fakeBackend) CloseAll()               {}

func TestFacadeLegacyFaceUsesSelectedBackendWithAtomicFalse(t *testing.T) {
	fb := &fakeBackend{}
	c := &Ctrl{sel: func() string { return "gosftp" }, forced: fb} // forced 仅供测试注入
	if err := c.Get("h", "u", "/r", "/l"); err != nil {
		t.Fatal(err)
	}
	if fb.called != "Get" || fb.lastReq.Atomic {
		t.Fatalf("legacy 面必须走 backend() 且 Atomic=false, called=%q atomic=%v", fb.called, fb.lastReq.Atomic)
	}
}
```

（为支持该测试，`Ctrl` 加一个 `forced Backend` 字段，`backend()` 优先返回它——只在测试里用。）

**`search.go` 的接收者同步改名**（自审 S2）：`func (c *Ctrl) Search(...)` → `func (b *BatchBackend) Search(...)`，并把 BFS 主体抽成 `searchBFS(ctx, lister func(host, user string, paths []string) (map[string][]Item, error), host, user, root, pattern string, maxDepth, limit int, onProgress func(int)) (SearchOutcome, error)`；`BatchBackend.Search` 传 `b.ListMany`，`GoBackend.Search`（Task 13 补）传自己的 `ListMany`。`search_test.go` 只需构造名替换（`NewBatchBackend`），方法名与断言不变。

**改名映射（不要凭印象，按这张表逐条对）**：

| 原方法（`*Ctrl`） | 新位置 |
|---|---|
| `Get/GetRecursive/Put/PutRecursive`（四参） | `(*BatchBackend)` **同名保留**（不改名！）；接口方法叫 `TransferGet/TransferGetTree/TransferPut/TransferPutTree` |
| `Search` | `(*BatchBackend) Search`（接收者改名）+ `searchBFS` 共用 |
| `List/ListMany/Home/Remove/RemoveRecursive/Mkdir/Rename/Connect/Connected/Disconnect/CloseAll/buildBatch/run/...` | `(*BatchBackend)` 原样（仅接收者改名） |
| 门面 `Ctrl` | 保留 14 个 legacy 方法 + `TransferGet/TransferGetTree/TransferPut/TransferPutTree/Cancel/Connect/Search/SetJournalDir` |

Expected: 27 / 4 / 5（共 36 处）。

**`internal/sync/e2e_test.go:32` 与 `app_test.go:54` 保持两参 `NewCtrl` 不动**（M1 修正：spec D3 要求两参构造保持可用）。**要改的是 `app.go:126`**，改成本 task 新增的 `NewCtrlWith`：

```go
	a.sftp = sftp.NewCtrlWith(osutil.NewRunner(), emit, func() string {
		if a.cfg == nil { // app_test 直接构造时 cfg 可能为 nil
			return ""
		}
		return a.cfg.App.SftpTransport
	})
	if dir := stateDir(); dir != "" {
		a.sftp.SetJournalDir(dir) // Task 8 的 backup-swap journal（S6）
	}
```

- [ ] **Step 4: 跑全量验证**

Run: `go build ./... && go vet ./... && go test ./... -race -count=1`
Expected: PASS（既有 49 个 sftp 测试不重写、语义不变）

- [ ] **Step 5: 提交**

```bash
git add internal/sftp/
git commit -m "refactor(sftp): 门面双面 + BatchBackend 更名（测试仅换构造名，语义零改动）"
```

---

## Task 6: GoBackend 引导（`ssh -s sftp` + `NewClientPipe` + 能力探测 + stderr 合并）

**Files:**
- Create: `internal/sftp/gosftp.go`
- Modify: `internal/sftp/ctrl.go`（`backend()` 分支）
- Test: `internal/sftp/e2e_test.go`（新建，含 skip 约定）

**Interfaces:**
- Consumes: `osutil.StartPipes`（Task 3）、`NewPool`（Task 4）、`resolveTransport`（Task 1）
- Produces: `func NewGoBackend(sel TransportSelector, emit forward.EmitFunc) *GoBackend`；`(g *GoBackend) Capabilities(host, user string) (posixRename bool, err error)`

- [ ] **Step 1: 写 e2e 测试骨架（缺环境变量即 skip）**

`internal/sftp/e2e_test.go`：

```go
package sftp

import (
	"os"
	"os/exec"
	"testing"
)

// TestGoBackendE2E 需要 e2e/test_local.sh 先起好临时 sshd 并导出 SSHORE_E2E_HOST / SSHORE_E2E_REMOTE。
func TestGoBackendE2E(t *testing.T) {
	host := os.Getenv("SSHORE_E2E_HOST")
	remote := os.Getenv("SSHORE_E2E_REMOTE")
	if host == "" || remote == "" {
		t.Skip("未提供 SSHORE_E2E_HOST / SSHORE_E2E_REMOTE，跳过真实传输验证")
	}
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("缺少 ssh 二进制")
	}
	g := NewGoBackend(nil, nil)
	defer g.CloseAll()

	// 本 task 只验「会话起得来 + 能力探测」；Home/List 要 Task 13 才实现（自审 S7）。
	ok, err := g.Capabilities(host, "")
	if err != nil {
		t.Fatalf("能力探测失败（会话没起来）: %v", err)
	}
	t.Logf("posix-rename 能力: %v", ok)
	if _, err := g.List(host, "", remote); err == nil {
		t.Log("提示：List 已实现（若已执行到 Task 13 则正常）")
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `SSHORE_E2E_HOST=x SSHORE_E2E_REMOTE=/tmp go test ./internal/sftp/ -run TestGoBackendE2E -v`
Expected: FAIL —— `undefined: NewGoBackend`（无环境变量时应 skip：先确认 `go test ./internal/sftp/ -run TestGoBackendE2E -v` 输出 SKIP）

- [ ] **Step 3: 实现 GoBackend 引导**

`internal/sftp/gosftp.go`：

```go
package sftp

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/pkg/sftp"

	"sshore/internal/forward"
	"sshore/internal/osutil"
)

// GoBackend 是基于 pkg/sftp 的新传输后端（spec D1）。
// 技术审核 S5：Go 没有 partial struct —— 后续 task 新增字段必须回来改**这一处**声明。
// 这里一次性声明全部字段，后续 task 只实现方法，避免每个 task 都要改结构体。
type GoBackend struct {
	pool *Pool
	emit forward.EmitFunc
	sel  TransportSelector

	journalDir string      // Task 8：journal 目录（app 用 stateDir() 注入）
	journal    *swapJournal // Task 8：backup-swap 崩溃恢复

	regMu sync.Mutex        // Task 10：id → 会话（取消用）
	reg   map[string]*Session

	inflightMu sync.Mutex   // Task 11：同目标去重
	inflight   map[string]string

	partsMu    sync.Mutex   // Task 13：已知 .part（退出清理用）
	knownParts map[string][2]string // id → {local, remote}
}

func NewGoBackend(sel TransportSelector, emit forward.EmitFunc) *GoBackend {
	g := &GoBackend{emit: emit, sel: sel}
	g.pool = NewPool(g.dial)
	return g
}

// 本文件在 Task 7 里还会补：var ioCopy = io.Copy（便于测试注入）、
// func (g *GoBackend) copyFileToLocal(s *Session, req TransferRequest, remote, local string, report func(Progress)) error
// —— Task 12 的目录传输复用它，避免逐文件建会话。
// dial 起 ssh -s sftp，把 stdin/stdout 交给 NewClientPipe。
// stderr 由 osutil.StartPipes 后台 drain，错误上报读 PipedProcess.StderrText()。
func (g *GoBackend) dial(host, user string) (*Session, error) {
	args := []string{
		"-o", "BatchMode=yes",
		"-o", "IdentitiesOnly=yes",
		"-o", "ConnectTimeout=10",
		"-o", "ServerAliveInterval=30",
		"-o", "ServerAliveCountMax=3",
	}
	if user != "" {
		args = append(args, "-o", "User="+user)
	}
	// Task 0 实测：-s 前置/后置在 Win32-OpenSSH 9.5p1 都可用；采用前置（不依赖 getopt 置换）。
	// 另注（Task 0 的 Session 0 现象）：本函数必须在交互桌面会话里运行 ——
	// Session 0 中 spawn 的 ssh.exe 会卡在 SFTP INIT 之后（裸 ssh 命令同样卡），属环境限制。
	args = append(args, "-s", host, "sftp")
	pp, err := osutil.StartPipes("ssh", args...)
	if err != nil {
		return nil, err
	}
	cl, err := sftp.NewClientPipe(pp.Stdout, pp.Stdin)
	if err != nil {
		msg := pp.StderrText()
		_ = pp.Close()
		if strings.Contains(msg, "subsystem request failed") {
			return nil, fmt.Errorf("远端未启用 sftp 子系统（检查 sshd_config 的 Subsystem sftp）: %s", msg)
		}
		return nil, fmt.Errorf("建立 SFTP 会话失败: %v (%s)", err, msg)
	}
	return &Session{Host: host, User: user, Conn: cl, Proc: pp, state: sessBusy}, nil
}

func (g *GoBackend) Capabilities(host, user string) (bool, error) {
	s, err := g.pool.AcquireList(context.Background(), host, user)
	if err != nil {
		return false, err
	}
	_, ok := s.Conn.HasExtension("posix-rename@openssh.com")
	g.pool.Release(s, true)
	return ok, nil
}

func (g *GoBackend) Home(host, user string) (string, error) { /* Task 13 补齐 */ return "", errors.New("未实现") }
func (g *GoBackend) List(host, user, path string) ([]Item, error) { /* Task 13 补齐 */ return nil, errors.New("未实现") }
func (g *GoBackend) ListMany(host, user string, paths []string) (map[string][]Item, error) {
	return nil, errors.New("未实现")
}
func (g *GoBackend) Get(req TransferRequest, report func(Progress)) error     { return errors.New("未实现") }
func (g *GoBackend) GetTree(req TransferRequest, report func(Progress)) error { return errors.New("未实现") }
func (g *GoBackend) Put(req TransferRequest, report func(Progress)) error     { return errors.New("未实现") }
func (g *GoBackend) PutTree(req TransferRequest, report func(Progress)) error { return errors.New("未实现") }
func (g *GoBackend) Remove(host, user, path string) error                     { return errors.New("未实现") }
func (g *GoBackend) RemoveRecursive(host, user, path string) error            { return errors.New("未实现") }
func (g *GoBackend) Mkdir(host, user, path string) error                      { return errors.New("未实现") }
func (g *GoBackend) Rename(host, user, oldPath, newPath string) error         { return errors.New("未实现") }
func (g *GoBackend) Connected(host string) bool                               { return false }
func (g *GoBackend) Disconnect(host string) error                             { return g.pool.Disconnect(host) }
func (g *GoBackend) CloseAll()                                                { g.pool.CloseAll() }
```

把 `internal/sftp/ctrl.go` 的 `backend()` 改成按选择器分支：

```go
func (c *Ctrl) backend() Backend {
	if resolveTransport(c.sel) == KindGo {
		if c.goBackend == nil {
			c.goBackend = NewGoBackend(c.sel, c.emit)
		}
		return c.goBackend
	}
	return c.batch
}
```

（`Ctrl` 加字段 `goBackend *GoBackend`、`emit forward.EmitFunc`。）

- [ ] **Step 3b: 依赖转正（Task 1 裁决的收尾）**

本 task 首次 import `github.com/pkg/sftp` 之后跑一次 `go mod tidy`，确认 `go.mod` 里 `pkg/sftp` 不再是 `// indirect` 且 `go mod tidy -diff` 为空；把 `go.mod/go.sum` 一起提交。

- [ ] **Step 4: 起临时 sshd 跑 e2e**

先把 `e2e/test_local.sh` 末尾那条单次 `go test` 换成**保留全部环境的双后端循环**（自审 S4/S5：现有脚本不 export 也不打印 `SSHORE_E2E_*`，且 `PATH=$SHIM` 与 `GOPATH/GOMODCACHE/GOCACHE` 都是必需的，漏了三样就会「垫片失效 → 别名解析不到 → 测试全 skip → 假绿」）：

```bash
E2E_RUN="${E2E_RUN:-E2E$}"   # 允许按用例名过滤；默认跑所有以 E2E 结尾的用例
for backend in batch gosftp; do
  echo "--- backend=$backend run=$E2E_RUN ---"
  PATH="$SHIM:$PATH" \
   GOPATH="$REAL_GOPATH" GOMODCACHE="$REAL_GOMODCACHE" GOCACHE="$REAL_GOCACHE" \
   SSHORE_E2E_HOST=sshore-e2e SSHORE_E2E_REMOTE="$REMOTE_DIR" \
   SSHORE_SFTP_TRANSPORT="$backend" \
   HOME="$HOME" go test ./internal/sftp/ ./internal/sync/ -run "$E2E_RUN" -count=1 -v || exit 1
done
```

之后本 task 与 Task 7-13 的 e2e 一律用同一条命令（env 由脚本自带，不要再手敲 `SSHORE_E2E_*`）：

```bash
E2E_RUN=TestGoBackendE2E bash e2e/test_local.sh
```

Expected: PASS，且日志打印 `posix-rename 能力: true`（Linux OpenSSH）。

- [ ] **Step 5: 提交**

```bash
git add internal/sftp/gosftp.go internal/sftp/ctrl.go internal/sftp/e2e_test.go
git commit -m "feat(sftp): GoBackend 引导（ssh -s sftp + NewClientPipe + 能力探测）"
```

---

## Task 7: 单文件下载（进度 + `.part` + `done==total` + 本地提交）

**Files:**
- Modify: `internal/sftp/gosftp.go`（`Get`）
- Create: `internal/sftp/copy.go`（`decideCommit` 纯函数 + `progressEmitter`）
- Test: `internal/sftp/copy_test.go`（新建）、`internal/sftp/e2e_test.go`（追加下载用例）

**Interfaces:**
- Consumes: `PartName/IsInternalTemp`（Task 2）、`Pool`（Task 4）
- Produces: `type commitDecision int`（`commitOK`/`commitShortRead`）；`func decideCommit(done, total int64) commitDecision`；`func newProgressEmitter(id string, report func(Progress)) *progressEmitter`；`(e *progressEmitter) send(p Progress, force bool)`

- [ ] **Step 1: 写失败测试（提交前置 + 节流）**

`internal/sftp/copy_test.go`：

```go
package sftp

import (
	"testing"
	"time"
)

// 技术审核 R13/评审 M7：ReadFrom 对 EOF 返回 (n, nil)，所以「截断的源」不会报错 ——
// 必须靠 done!=total 兜住；这里用 copyStream + 截断 reader 直接钉住这条防线。
func TestTruncatedSourceIsCaughtByByteCount(t *testing.T) {
	total := int64(100)
	got, err := copyStream(io.Discard, io.LimitReader(strings.NewReader(strings.Repeat("x", 100)), 40))
	if err != nil {
		t.Fatalf("截断读取本身不该报错（这正是危险之处）: %v", err)
	}
	if decideCommit(got, total) != commitShortRead {
		t.Fatalf("少传 %d/%d 必须拒绝提交", got, total)
	}
}

func TestDecideCommitRequiresExactTotal(t *testing.T) {
	if decideCommit(100, 100) != commitOK {
		t.Fatal("done==total 才允许提交")
	}
	if decideCommit(99, 100) != commitShortRead {
		t.Fatal("少一字节也必须拒绝提交（ReadFrom 对 EOF 返回 nil，spec R13）")
	}
}

func TestProgressEmitterThrottlesButForcesFinalFrame(t *testing.T) {
	now := time.Unix(0, 0)
	sent := 0
	e := newProgressEmitter("t1", func(Progress) { sent++ })
	e.now = func() time.Time { return now }

	e.send(Progress{Done: 1}, false)
	e.send(Progress{Done: 2}, false) // 200ms 内被节流
	if sent != 1 {
		t.Fatalf("节流失效，sent=%d", sent)
	}
	now = now.Add(250 * time.Millisecond)
	e.send(Progress{Done: 3}, false)
	if sent != 2 {
		t.Fatalf("超过节流窗口应放行，sent=%d", sent)
	}
	e.send(Progress{Done: 4}, true) // 末帧强制
	if sent != 3 {
		t.Fatalf("末帧必须强制发送，sent=%d", sent)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/sftp/ -run 'TestDecideCommit|TestProgressEmitter' -v`
Expected: FAIL —— `undefined: decideCommit`

- [ ] **Step 3: 实现 `copy.go`**

```go
package sftp

import (
	"io"
	"os"
	"sync"
	"time"
)

type commitDecision int

const (
	commitOK commitDecision = iota
	commitShortRead
)

// decideCommit：只有 done == total 才允许提交（D15）。
func decideCommit(done, total int64) commitDecision {
	if done == total {
		return commitOK
	}
	return commitShortRead
}

const progressInterval = 200 * time.Millisecond

// progressEmitter 负责节流与末帧强制（D7）。
type progressEmitter struct {
	id     string
	report func(Progress)
	now    func() time.Time

	mu   sync.Mutex
	last time.Time
}

func newProgressEmitter(id string, report func(Progress)) *progressEmitter {
	if report == nil {
		report = func(Progress) {}
	}
	return &progressEmitter{id: id, report: report, now: time.Now}
}

func (e *progressEmitter) send(p Progress, force bool) {
	p.ID = e.id
	e.mu.Lock()
	now := e.now()
	if !force && now.Sub(e.last) < progressInterval {
		e.mu.Unlock()
		return
	}
	e.last = now
	e.mu.Unlock()
	e.report(p)
}

// ioCopy 是 io.Copy 的薄包装，便于测试注入。
var ioCopy = io.Copy

// countingReader 统计已发送字节（上传方向），base 是续传起点。
type countingReader struct {
	r    *os.File
	e    *progressEmitter
	p    Progress
	base int64
}

func (c *countingReader) Read(b []byte) (int, error) {
	n, err := c.r.Read(b)
	c.base += int64(n)
	c.p.Done = c.base
	c.e.send(c.p, false)
	return n, err
}

// copyStream 是唯一的字节搬运入口：便于单测注入「中途截断的 reader」验证 done!=total 必须拒绝提交。
func copyStream(dst io.Writer, src io.Reader) (int64, error) { return io.Copy(dst, src) }

// copyFileToLocal 把远端单个文件搬到本地（Task 12 的目录传输复用它，避免逐文件建会话）。
// 返回 part 路径与已传字节；调用方负责提交（rename）。
func (g *GoBackend) copyFileToLocal(s *Session, req TransferRequest, remote, local string, report func(Progress)) (string, int64, int64, error) {
	st, err := s.Conn.Stat(remote)
	if err != nil {
		return "", 0, 0, err
	}
	total := st.Size()
	part := req.PartPath
	if part == "" {
		part = PartName(local, req.ID)
	}
	f, err := os.OpenFile(part, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return "", 0, 0, err
	}
	rf, err := s.Conn.Open(remote)
	if err != nil {
		_ = f.Close()
		return "", 0, 0, err
	}
	em := newProgressEmitter(req.ID, report)
	cw := &countingWriter{f: f, e: em, p: Progress{Host: req.Host, Direction: DirDownload, Name: remote, PartPath: part, Total: total, Phase: PhaseTransfer}}
	em.send(cw.p, true)
	n, cerr := copyStream(cw, rf)
	_ = rf.Close()
	_ = f.Close()
	if cerr != nil {
		return part, n, total, cerr
	}
	return part, n, total, nil
}

// countingWriter 统计已落盘字节，并按需上报。
type countingWriter struct {
	f *os.File
	e *progressEmitter
	p Progress
	n int64
}

func (w *countingWriter) Write(b []byte) (int, error) {
	n, err := w.f.Write(b)
	w.n += int64(n)
	w.p.Done = w.n
	w.e.send(w.p, false)
	return n, err
}
```

`gosftp.go` 实现 `Get`：

```go
func (g *GoBackend) Get(req TransferRequest, report func(Progress)) error {
	s, err := g.pool.AcquireTransfer(context.Background(), req.Host, req.User)
	if err != nil {
		return &TransferError{Op: "sftp get", Host: req.Host, Path: req.Remote, Err: err, RemoteMsg: ""}
	}
	reuse := false
	defer func() { g.pool.Release(s, reuse) }()

	st, err := s.Conn.Stat(req.Remote)
	if err != nil {
		return &TransferError{Op: "sftp get", Host: req.Host, Path: req.Remote, Err: err, RemoteMsg: s.Proc.StderrText()}
	}
	total := st.Size()
	part := req.PartPath
	if part == "" {
		part = PartName(req.Local, req.ID)
	}
	f, err := os.OpenFile(part, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return &TransferError{Op: "sftp get", Path: part, Err: err}
	}
	rf, err := s.Conn.Open(req.Remote)
	if err != nil {
		_ = f.Close()
		return &TransferError{Op: "sftp get", Host: req.Host, Path: req.Remote, Err: err, RemoteMsg: s.Proc.StderrText()}
	}
	em := newProgressEmitter(req.ID, report)
	cw := &countingWriter{f: f, e: em, p: Progress{Host: req.Host, Direction: DirDownload, Name: req.Remote, PartPath: part, Total: total, Phase: PhaseTransfer}}
	em.send(cw.p, true) // 首帧
	_, cerr := ioCopy(cw, rf)
	_ = rf.Close()
	_ = f.Close()
	if cerr != nil {
		return &TransferError{Op: "sftp get", Host: req.Host, Path: req.Remote, Err: cerr, RemoteMsg: s.Proc.StderrText()}
	}
	if decideCommit(cw.n, total) != commitOK {
		return &TransferError{Op: "sftp get", Path: req.Local, Err: fmt.Errorf("传输不完整：%d/%d 字节，已保留 %s", cw.n, total, part)}
	}
	if err := os.Rename(part, req.Local); err != nil {
		return &TransferError{Op: "sftp get", Path: req.Local, Err: err}
	}
	cw.p.Done = total
	em.send(cw.p, true) // 末帧强制
	reuse = true
	return nil
}
```

**Atomic 分支（技术审核 S11：字段必须真的被读，否则删掉它）**——在 `Get` 取到 `part` 之前插入：

```go
	if !req.Atomic { // legacy 面（internal/sync）：直写目标，原子性由 sync 自己的 .part+rename 保证
		f, err := os.OpenFile(req.Local, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
		if err != nil {
			return &TransferError{Op: "sftp get", Path: req.Local, Err: err}
		}
		rf, err := s.Conn.Open(req.Remote)
		if err != nil {
			_ = f.Close()
			return &TransferError{Op: "sftp get", Host: req.Host, Path: req.Remote, Err: err, RemoteMsg: s.Proc.StderrText()}
		}
		n, cerr := copyStream(f, rf)
		_ = rf.Close()
		_ = f.Close()
		if cerr != nil {
			return &TransferError{Op: "sftp get", Host: req.Host, Path: req.Remote, Err: cerr}
		}
		if n != st.Size() {
			return &TransferError{Op: "sftp get", Path: req.Local, Err: fmt.Errorf("传输不完整：%d/%d", n, st.Size())}
		}
		reuse = true
		return nil
	}
```

`Put` 同理：`!req.Atomic` 时直接 `OpenFile(req.Remote, O_WRONLY|O_CREATE|O_TRUNC)` 写完即返回，不做 `.part`、不做 `PosixRename`、不登记 journal。

（`ioCopy` 是 `io.Copy` 的薄包装，便于测试注入。）

- [ ] **Step 4: 跑测试 + e2e**

```bash
go test ./internal/sftp/ -run 'TestDecideCommit|TestProgressEmitter' -v
E2E_RUN=TestGoBackendE2E bash e2e/test_local.sh
```

并在 `e2e_test.go` 追加下载断言：传输中目标名不存在、`.part` 存在、结束后目标文件 sha256 == 源。

- [ ] **Step 5: 提交**

```bash
git add internal/sftp/copy.go internal/sftp/copy_test.go internal/sftp/gosftp.go internal/sftp/e2e_test.go
git commit -m "feat(sftp): 单文件下载（字节进度 + .part 原子提交 + done==total 前置）"
```

---

## Task 8: 单文件上传（flags + `PosixRename` 提交 + backup-swap + journal）

**Files:**
- Modify: `internal/sftp/gosftp.go`（`Put`）
- Create: `internal/sftp/journal.go`
- Test: `internal/sftp/journal_test.go`、`internal/sftp/e2e_test.go`（追加覆盖写用例）

**Interfaces:**
- Consumes: `PartNameRemote/BakNameRemote`（**远端路径必须用 Remote 家族**；本地路径才用 `PartName/BakName`）—— Task 2 评审 Important-2 / 重审 N3、`Capabilities`（Task 6）
- Produces: `type swapJournal struct{...}`；`func newSwapJournal(dir string) *swapJournal`；`(j *swapJournal) Begin(target, bak, part string) error`；`(j *swapJournal) Done(target string) error`；`(j *swapJournal) Recover() []string`

- [ ] **Step 1: 写失败测试（journal 幂等 + 恢复）**

`internal/sftp/journal_test.go`：

```go
package sftp

import (
	"os"
	"path/filepath"
	"testing"
)

func TestJournalBeginDoneRecover(t *testing.T) {
	dir := t.TempDir()
	j := newSwapJournal(dir)
	if err := j.Begin("/data/a.txt", "/data/a.txt.sshore-sftppart-bak-1", "/data/a.txt.sshore-sftppart-t1-2"); err != nil {
		t.Fatal(err)
	}
	if got := j.Recover(); len(got) != 1 || got[0] != "/data/a.txt" {
		t.Fatalf("恢复列表应含未完成的目标，got %v", got)
	}
	if err := j.Done("/data/a.txt"); err != nil {
		t.Fatal(err)
	}
	if got := j.Recover(); len(got) != 0 {
		t.Fatalf("完成后不应再报告，got %v", got)
	}
	// 幂等：done 不存在也算成功
	if err := j.Done("/data/none.txt"); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(filepath.Join(dir, "swap-entries.json"))
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/sftp/ -run TestJournal -v`
Expected: FAIL —— `undefined: newSwapJournal`

- [ ] **Step 3: 实现 journal 与 `Put`**

`internal/sftp/journal.go`：用 `os.WriteFile/O_RENAME` 语义的 `swap-entries.json`（`{entries:[{target,bak,part}]}`）实现 `Begin/Done/Recover`（`Recover` 只返回目标路径，供启动时恢复；实现用 `encoding/json` + `sync.Mutex` + 原子写 `tmp+rename`）。

`gosftp.go` 的 `Put`：

```go
func (g *GoBackend) Put(req TransferRequest, report func(Progress)) error {
	st, err := os.Stat(req.Local)
	if err != nil {
		return &TransferError{Op: "sftp put", Path: req.Local, Err: err}
	}
	total := st.Size()
	s, err := g.pool.AcquireTransfer(context.Background(), req.Host, req.User)
	if err != nil {
		return &TransferError{Op: "sftp put", Host: req.Host, Path: req.Remote, Err: err}
	}
	reuse := false
	defer func() { g.pool.Release(s, reuse) }()

	// 能力探测：HasExtension 返回 (string, bool)（spec §2.3）。
	_, hasPosix := s.Conn.HasExtension("posix-rename@openssh.com")
	part := req.PartPath
	if part == "" {
		part = PartNameRemote(req.Remote, req.ID) // 远端 POSIX 路径：必须用 Remote 家族（Task 2 评审 Important-2）
	}
	// 新建：O_WRONLY|O_CREATE|O_TRUNC；续传（Task 11）：O_WRONLY（严禁 O_TRUNC）+ Seek(partSize)
	flags := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	var offset int64
	if req.Resume {
		flags = os.O_WRONLY
		offset = req.ResumeOffset
	}
	wf, err := s.Conn.OpenFile(part, flags)
	if err != nil {
		return &TransferError{Op: "sftp put", Host: req.Host, Path: part, Err: err, RemoteMsg: s.Proc.StderrText()}
	}
	if req.Resume && offset > 0 {
		if _, err := wf.Seek(offset, 0); err != nil {
			_ = wf.Close()
			return &TransferError{Op: "sftp put", Path: part, Err: err}
		}
	}
	lf, err := os.Open(req.Local)
	if err != nil {
		_ = wf.Close()
		return &TransferError{Op: "sftp put", Path: req.Local, Err: err}
	}
	em := newProgressEmitter(req.ID, report)
	cr := &countingReader{r: lf, e: em, p: Progress{Host: req.Host, Direction: DirUpload, Name: req.Remote, PartPath: part, Total: total, Phase: PhaseTransfer}, base: offset}
	em.send(cr.p, true)
	n, cerr := ioCopy(wf, cr)
	_ = wf.Close()
	_ = lf.Close()
	if cerr != nil {
		return &TransferError{Op: "sftp put", Host: req.Host, Path: req.Remote, Err: cerr, RemoteMsg: s.Proc.StderrText()}
	}
	if decideCommit(n+offset, total) != commitOK {
		return &TransferError{Op: "sftp put", Path: req.Remote, Err: fmt.Errorf("传输不完整：%d/%d 字节，已保留 %s", n+offset, total, part)}
	}
	if err := commitRemote(s, hasPosix, part, req.Remote, g.journal); err != nil {
		return &TransferError{Op: "sftp put", Host: req.Host, Path: req.Remote, Err: err, RemoteMsg: s.Proc.StderrText()}
	}
	em.send(cr.p, true)
	reuse = true
	return nil
}
```

其中 `commitRemote`（同文件）：

```go
// commitRemote：有 posix-rename 就原子覆盖；否则 backup-swap（绝不先删目标）。
// GoBackend 的字段与方法（补在 NewGoBackend 附近）：
//   journalDir string
//   journal    *swapJournal
func (g *GoBackend) SetJournalDir(dir string) {
	g.journalDir = dir
	if dir != "" {
		g.journal = newSwapJournal(dir)
	}
}

// j 由 GoBackend.SetJournalDir（app 装配时用 stateDir()）注入；nil 表示不做崩溃恢复。
func commitRemote(s *Session, hasPosix bool, part, target string, j *swapJournal) error {
	if hasPosix {
		return s.Conn.PosixRename(part, target)
	}
	bak := BakNameRemote(target) // 远端路径同上
	if err := s.Conn.Rename(target, bak); err != nil {
		// 技术审核 M4：只有「目标本来就不存在」才退回直接提交；其它错误（权限/被占用）必须原样上报，
		// 否则会把失败当成功、还会绕过 journal。
		if !os.IsNotExist(err) {
			return err
		}
		return s.Conn.Rename(part, target)
	}
	if j != nil {
		_ = j.Begin(target, bak, part)
	}
	if err := s.Conn.Rename(part, target); err != nil {
		_ = s.Conn.Rename(bak, target) // 回滚
		return err
	}
	if j != nil {
		_ = j.Done(target)
	}
	_ = s.Conn.Remove(bak)
	return nil
}
```

- [ ] **Step 4: 跑测试 + e2e（含覆盖写）**

```bash
go test ./internal/sftp/ -run TestJournal -v
E2E_RUN=TestGoBackendE2E bash e2e/test_local.sh
```

`e2e_test.go` 追加两条（M7）：

```go
// 覆盖写：旧内容在提交前仍在，提交后为新内容
func TestUploadOverwriteE2E(t *testing.T) { /* 目标先写 old；上传 new；断言最终 new；提交前用 report 回调里 Stat 目标 == old */ }

// backup-swap 回滚：把目标做成**非空目录**，PosixRename/rename 必失败 → 断言目标目录仍在、无残留 bak
func TestBackupSwapRollbackE2E(t *testing.T) {
	host, remote, ok := e2eEnv(t)
	if !ok {
		return
	}
	g := NewGoBackend(nil, nil)
	defer g.CloseAll()
	target := remote + "/occupied"
	if err := g.Mkdir(host, "", target); err != nil {
		t.Fatal(err)
	}
	if err := g.Mkdir(host, "", target+"/child"); err != nil { // 非空目录，rename 覆盖必失败
		t.Fatal(err)
	}
	src := filepath.Join(t.TempDir(), "x.txt")
	if err := os.WriteFile(src, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := g.Put(TransferRequest{ID: "occ", Host: host, Local: src, Remote: target, Atomic: true}, nil); err == nil {
		t.Fatal("覆盖非空目录必须失败")
	}
	if _, err := g.List(host, "", target+"/child"); err != nil {
		t.Fatalf("失败后目标必须可回滚/仍存在: %v", err)
	}
}
```

- [ ] **Step 5: 提交**

```bash
git add internal/sftp/journal.go internal/sftp/journal_test.go internal/sftp/gosftp.go internal/sftp/e2e_test.go
git commit -m "feat(sftp): 单文件上传（PosixRename 提交 + backup-swap + journal 恢复）"
```

---

## Task 9: 进度事件 + 绑定层（id / `TransferGet…` / `SftpTransferCancel`）

**Files:**
- Modify: `app.go`（`SftpGet/SftpGetDir/SftpPut/SftpPutRecursive` 加 id/resume/partPath、新增 `SftpTransferCancel`、`sftp:transfer-progress` 事件）
- Modify: `internal/sftp/ctrl.go`（`Cancel` 转发到后端）
- Test: `app_test.go`（8 处旧调用改为新签名）

**Interfaces:**
- Consumes: Task 5 的 `TransferGet/TransferPut/Cancel`
- Produces: 绑定 `SftpGet(id, host, user, remote, local string, resume bool, partPath string) error`（`SftpGetDir/SftpPut/SftpPutRecursive` 同构）、`SftpTransferCancel(id string) bool`、事件 `sftp:transfer-progress`

- [ ] **Step 1: 写失败测试（事件载荷）**

`app_test.go` 追加：

```go
func TestSftpTransferProgressEventShape(t *testing.T) {
	// 用注入的 emit 记录事件；Progress → map 的字段名必须与前端约定一致
	got := progressToEvent(sftp.Progress{ID: "t1", Done: 5, Total: 10, Phase: sftp.PhaseTransfer})
	if got["id"] != "t1" || got["done"] != int64(5) || got["total"] != int64(10) || got["phase"] != "transfer" {
		t.Fatalf("事件字段不符: %#v", got)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test . -run TestSftpTransferProgressEventShape -v`
Expected: FAIL —— `undefined: progressToEvent`

- [ ] **Step 3: 实现绑定与事件**

`app.go` 增加：

```go
func progressToEvent(p sftp.Progress) map[string]any {
	return map[string]any{
		"id": p.ID, "host": p.Host, "direction": string(p.Direction), "name": p.Name,
		"partPath": p.PartPath, "done": p.Done, "total": p.Total,
		"filesDone": p.FilesDone, "filesTotal": p.FilesTotal, "phase": string(p.Phase),
	}
}

func (a *App) emitProgress(p sftp.Progress) {
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, "sftp:transfer-progress", progressToEvent(p))
	}
}

func (a *App) SftpGet(id, host, user, remote, local string, resume bool, partPath string) error {
	if err := a.sftp.TransferGet(sftp.TransferRequest{ID: id, Host: host, User: user, Remote: remote, Local: local, Resume: resume, PartPath: partPath, Atomic: true}, a.emitProgress); err != nil {
		return err
	}
	a.recordRecentSFTP(host, path.Dir(remote), filepath.Dir(local))
	return nil
}

func (a *App) SftpTransferCancel(id string) bool { return a.sftp.Cancel(id) }
```

（`SftpGetDir/SftpPut/SftpPutRecursive` 同构改为 `TransferGetTree/TransferPut/TransferPutTree`。`app_test.go` 的 8 处调用同步加 id/resume/partPath 实参。）

**同批必须做两件事**（技术审核 M1：id 要在本 task 就生成，否则 Task 9→14 之间取消/续传没有数据源）：

1. `runBatch` 里给每个队列项生成稳定 id 并写进 `rec`：`const rec = { id: ``t${seq}-${n}``, ..., partPath: '', resume: false }`；
2. 修前端 3 个调用点（`SftpView.vue:362/363/474`，拖拽也走 `runBatch`），否则签名变更后前端会把 host 当 id 传（绑定参数错位 = 运行期错乱且 `npm run build` 抓不到）：`frontend/src/views/SftpView.vue` 的 `runBatch`（`:362-363`）、`uploadPicked`（`:474`）与拖拽入口改成：

```js
// 每个队列项一个稳定 id；resume/partPath 这轮先给 false/''，Task 14 再接「续传」按钮
await SftpGet(rec.id, host.value, '', t.src, t.dst, false, '')
```

- [ ] **Step 4: 跑测试 + 重新生成绑定**

```bash
go test . -run TestSftpTransferProgressEventShape -v
wails generate module && cd frontend && npm run build
```

Expected: PASS；`SftpTransferCancel` 出现在 `frontend/wailsjs/go/main/App.js`（缺失就会在 Rollup 阶段报 not exported）。

- [ ] **Step 5: 提交**

```bash
git add app.go app_test.go frontend/wailsjs frontend/src/views/SftpView.vue
git commit -m "feat(sftp): 传输绑定加 id/resume/partPath + 进度事件 + 取消入口"
```

---

## Task 10: 取消（整批语义 + 幂等）

**Files:**
- Modify: `internal/sftp/gosftp.go`（`cancelMu` + `Cancel` 注册表）
- Modify: `internal/sftp/ctrl.go`（`Cancel` 转发）
- Test: `internal/sftp/e2e_test.go`（取消四断言）

**Interfaces:**
- Consumes: Task 7/8 的传输路径
- Produces: `(g *GoBackend) Cancel(id string) bool`（幂等：未知/已完成 id 返回 false）；`Ctrl.Cancel(id) bool`

- [ ] **Step 1: 写 e2e 断言（目标名不存在 + `.part` 是源前缀 + 幂等）**

`e2e_test.go` 追加：

```go
func TestCancelWholeBatchE2E(t *testing.T) {
	host := os.Getenv("SSHORE_E2E_HOST")
	remote := os.Getenv("SSHORE_E2E_REMOTE")
	if host == "" || remote == "" {
		t.Skip("未提供 SSHORE_E2E_*，跳过取消验证")
	}
	g := NewGoBackend(nil, nil)
	defer g.CloseAll()
	// 自造 32MB 源文件并上传（不依赖脚本预置）
	big := filepath.Join(t.TempDir(), "big.bin")
	if err := os.WriteFile(big, bytes.Repeat([]byte("x"), 32<<20), 0600); err != nil {
		t.Fatal(err)
	}
	src := remote + "/cancel-src.bin"
	if err := g.Put(TransferRequest{ID: "t-seed", Host: host, Local: big, Remote: src, Atomic: true}, nil); err != nil {
		t.Fatalf("种子上传失败: %v", err)
	}
	dst := filepath.Join(t.TempDir(), "cancel-dst.bin")

	// 确定性同步点：首帧进度是 io.Copy 之前同步发出的，收到它即代表会话与 `.part` 已就绪、传输尚未结束。
	// 不要用 time.Sleep —— 32MB 在本机可能 100ms 内就传完，Cancel 会返回 false（自审 S9）。
	started := make(chan struct{}, 1)
	report := func(Progress) { select { case started <- struct{}{}: default: } }
	done := make(chan error, 1)
	go func() {
		done <- g.Get(TransferRequest{ID: "t-cancel", Host: host, Remote: src, Local: dst, Atomic: true}, report)
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("传输未在 5s 内开始（首帧进度未到）")
	}
	if !g.Cancel("t-cancel") {
		t.Fatal("取消应返回 true（会话被关）")
	}
	if err := <-done; err == nil {
		t.Fatal("被取消的传输必须返回错误")
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatal("取消后最终文件名不得存在")
	}
	parts, _ := filepath.Glob(filepath.Dir(dst) + "/*" + PartMarker + "*")
	if len(parts) == 0 {
		t.Fatal("取消后应保留 .part 供续传")
	}
	if g.Cancel("t-cancel") {
		t.Fatal("取消必须幂等：已结束的 id 返回 false")
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `E2E_RUN=TestCancelWholeBatchE2E bash e2e/test_local.sh`
Expected: FAIL —— `g.Cancel undefined`

- [ ] **Step 3: 实现注册表与取消**

`gosftp.go` 加：

```go
type transferReg struct {
	mu   sync.Mutex
	byID map[string]*Session
}

func (g *GoBackend) register(id string, s *Session) func() {
	g.reg.mu.Lock()
	g.reg.byID[id] = s
	g.reg.mu.Unlock()
	return func() {
		g.reg.mu.Lock()
		delete(g.reg.byID, id)
		g.reg.mu.Unlock()
	}
}

// Cancel 关掉该传输独占的会话（库无逐请求取消）。幂等。
func (g *GoBackend) Cancel(id string) bool {
	g.reg.mu.Lock()
	s, ok := g.reg.byID[id]
	delete(g.reg.byID, id)
	g.reg.mu.Unlock()
	if !ok {
		return false
	}
	s.close()
	return true
}
```

在 `Get/Put` 取到会话后立刻 `defer g.register(req.ID, s)()`。

- [ ] **Step 4: 跑 e2e**

Run: `E2E_RUN='TestGoBackendE2E|TestCancelWholeBatchE2E' bash e2e/test_local.sh`
Expected: PASS（四条断言全绿）

- [ ] **Step 5: 提交**

```bash
git add internal/sftp/gosftp.go internal/sftp/ctrl.go internal/sftp/e2e_test.go
git commit -m "feat(sftp): 整批取消（关会话 + 幂等）与取消后 .part 保留"
```

---

## Task 11: 双向续传（`PartPath` + `part==total` + 去重；仅单文件）

**Files:**
- Create: `internal/sftp/resume.go`（纯函数 `decideResume`）
- Test: `internal/sftp/resume_test.go`
- Modify: `internal/sftp/gosftp.go`（下载/上传的续传分支 + `(host,direction,target)` 去重）

**Interfaces:**
- Consumes: `PartPath`（Task 7/8）、`decideCommit`
- Produces: `type resumeKind int`（`resumeFull`/`resumeAppend`/`resumeCommit`）；`type resumeInput struct{ PartSize, Total, SrcSize int64; SrcMtime, RecMtime time.Time; SrcChanged bool }`；`func decideResume(in resumeInput) (resumeKind, int64)`。**技术审核 S10**：只比 size 无法发现「同尺寸但内容变了」，因此 `decideResume` 必须要求 `SrcMtime.Equal(RecMtime)`（传输开始时记录，内存态），不等即 `resumeFull`

- [ ] **Step 1: 写失败测试（三条规则）**

`internal/sftp/resume_test.go`：

```go
package sftp

import "testing"

func TestDecideResumeThreeRules(t *testing.T) {
	// part 完整 → 直接提交，避免"续传 0 字节"
	if k, off := decideResume(resumeInput{PartSize: 100, Total: 100, SrcSize: 100}); k != resumeCommit || off != 100 {
		t.Fatalf("want commit/100, got %v/%d", k, off)
	}
	// part 小于总量且源未变 → 续写
	if k, off := decideResume(resumeInput{PartSize: 40, Total: 100, SrcSize: 100}); k != resumeAppend || off != 40 {
		t.Fatalf("want append/40, got %v/%d", k, off)
	}
	// 源被改动 / part 超出总量 → 整份重传
	if k, _ := decideResume(resumeInput{PartSize: 40, Total: 100, SrcSize: 100, SrcChanged: true}); k != resumeFull {
		t.Fatalf("源变动必须整份重传, got %v", k)
	}
	if k, _ := decideResume(resumeInput{PartSize: 120, Total: 100, SrcSize: 100}); k != resumeFull {
		t.Fatalf("part 超过总量必须整份重传, got %v", k)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/sftp/ -run TestDecideResume -v`
Expected: FAIL —— `undefined: decideResume`

- [ ] **Step 3: 实现纯函数与接线**

`resume.go` 按上面语义实现（`resumeCommit` 时 offset=Total；`resumeAppend` 时 offset=PartSize；否则整份）。

`gosftp.go`：`Get/Put` 开头若 `req.Resume && req.PartPath != ""`，用 `Stat`（下载查远端、上传查远端 `.part`）与本地/远端源的大小喂给 `decideResume`；`resumeFull` 时先 `Remove`/`os.Remove` 该 `.part` 再走全新流程；`resumeAppend` 时把 `ResumeOffset` 塞进 `TransferRequest`（`Put` 用 `Seek`，`Get` 用本地 `Seek`）。

去重：`GoBackend` 加 `inflight map[string]bool`（键 `host+"|"+direction+"|"+target`），重复触发返回 `TransferError{... Err: errors.New("同一目标已在传输中")}`。

- [ ] **Step 4: 跑单测 + e2e（sha256 相等）**

```bash
go test ./internal/sftp/ -run TestDecideResume -v
E2E_RUN=TestResumeE2E bash e2e/test_local.sh
```

`e2e_test.go` 追加（M2/M3 要求的四件事全在这里）：

```go
func TestResumeE2E(t *testing.T) {
	host, remote, ok := e2eEnv(t)
	if !ok {
		return
	}
	g := NewGoBackend(nil, nil)
	defer g.CloseAll()

	// 造 32MB 源并上传
	big := filepath.Join(t.TempDir(), "big.bin")
	payload := bytes.Repeat([]byte("abcdefgh"), 4<<20) // 32MB
	if err := os.WriteFile(big, payload, 0600); err != nil {
		t.Fatal(err)
	}
	src := remote + "/resume.bin"
	if err := g.Put(TransferRequest{ID: "seed", Host: host, Local: big, Remote: src, Atomic: true}, nil); err != nil {
		t.Fatal(err)
	}

	// 1) 全量下载拿参考 sha256
	full := filepath.Join(t.TempDir(), "full.bin")
	if err := g.Get(TransferRequest{ID: "full", Host: host, Remote: src, Local: full, Atomic: true}, nil); err != nil {
		t.Fatal(err)
	}
	wantSHA := sha256File(t, full)

	// 2) 取消到中途，断言目标名不存在 + .part 是源前缀
	part := filepath.Join(t.TempDir(), "part.bin")
	started := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		done <- g.Get(TransferRequest{ID: "cut", Host: host, Remote: src, Local: part, Atomic: true},
			func(Progress) { select { case started <- struct{}{}: default: } })
	}()
	<-started
	g.Cancel("cut")
	if err := <-done; err == nil {
		t.Fatal("被取消的下载必须报错")
	}
	if _, err := os.Stat(part); !os.IsNotExist(err) {
		t.Fatal("取消后最终名不得存在")
	}
	parts, _ := filepath.Glob(filepath.Dir(part) + "/*" + PartMarker + "*")
	if len(parts) != 1 {
		t.Fatalf("应恰好留一个 .part，got %v", parts)
	}
	partFile := parts[0]
	cutBytes, _ := os.ReadFile(partFile)
	if !bytes.Equal(cutBytes, payload[:len(cutBytes)]) {
		t.Fatal(".part 必须是源的前缀（sha256 前缀比对）—— 拼错锚点会静默损坏文件")
	}

	// 3) 续传并断言 sha256 == 一次性完整下载
	if err := g.Get(TransferRequest{ID: "resume", Host: host, Remote: src, Local: part, Atomic: true,
		Resume: true, PartPath: partFile}, nil); err != nil {
		t.Fatalf("续传失败: %v", err)
	}
	if got := sha256File(t, part); got != wantSHA {
		t.Fatalf("续传结果 sha256 不一致: got %s want %s", got, wantSHA)
	}

	// 4) 同目标重复触发必须被去重拒绝
	inflight := make(chan struct{}, 1)
	go func() {
		_ = g.Get(TransferRequest{ID: "dup1", Host: host, Remote: src, Local: filepath.Join(t.TempDir(), "dup1.bin"), Atomic: true},
			func(Progress) { select { case inflight <- struct{}{}: default: } })
	}()
	<-inflight
	errDup := g.Get(TransferRequest{ID: "dup2", Host: host, Remote: src, Local: filepath.Join(t.TempDir(), "dup2.bin"), Atomic: true}, nil)
	if errDup == nil {
		t.Fatal("同一目标并发传输必须被拒绝（inflight 去重）")
	}

	// 5) 同尺寸改写（技术审核 S10）：尺寸不变、内容变 + mtime 变 → 必须整份重传而不是拼接。
	if err := os.WriteFile(big, append([]byte("Z"), payload[1:]...), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(big, time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := g.Put(TransferRequest{ID: "seed2", Host: host, Local: big, Remote: src, Atomic: true}, nil); err != nil {
		t.Fatal(err)
	}
	part2 := filepath.Join(t.TempDir(), "part2.bin")
	if err := g.Get(TransferRequest{ID: "same", Host: host, Remote: src, Local: part2, Atomic: true}, nil); err != nil {
		t.Fatal(err)
	}
	if got := sha256File(t, part2); got == wantSHA {
		t.Fatal("源已改写，内容不可能与旧参考一致（自检）")
	}
	// 同尺寸但 mtime 不同的 .part 必须判为「不可续」：这里用改过源的旧 .part 触发整份重传，
	// 断言结果等于「用新源完整下载」的 sha256，而不是旧内容前缀 + 新内容拼接。
	fresh := filepath.Join(t.TempDir(), "fresh.bin")
	if err := g.Get(TransferRequest{ID: "fresh", Host: host, Remote: src, Local: fresh, Atomic: true}, nil); err != nil {
		t.Fatal(err)
	}
	if got, want := sha256File(t, part2), sha256File(t, fresh); got != want {
		t.Fatalf("同尺寸改写后必须整份重传: got %s want %s", got, want)
	}
}

func sha256File(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
```

**去重的实现（Task 11 Step 3 的真实代码，替换原来那句散文）**：

```go
// inflight 键 = host|direction|target，值 = transfer id；重复触发直接报错。
func (g *GoBackend) acquireInflight(host, dir, target, id string) (release func(), err error) {
	key := host + "|" + dir + "|" + target
	g.inflightMu.Lock()
	defer g.inflightMu.Unlock()
	if g.inflight == nil {
		g.inflight = map[string]string{}
	}
	if other, busy := g.inflight[key]; busy {
		return nil, &TransferError{Op: "sftp transfer", Host: host, Path: target, Err: fmt.Errorf("同一目标已在传输中（%s）", other)}
	}
	g.inflight[key] = id
	return func() {
		g.inflightMu.Lock()
		delete(g.inflight, key)
		g.inflightMu.Unlock()
	}, nil
}
```

在 `Get/Put` 开头 `release, err := g.acquireInflight(...)` + `defer release()`；「重试前等旧会话确认关闭」由 `Cancel` 的 `s.close()`（有界等待子进程退出）保证，不再需要额外等待。

- [ ] **Step 5: 提交**

```bash
git add internal/sftp/resume.go internal/sftp/resume_test.go internal/sftp/gosftp.go internal/sftp/e2e_test.go
git commit -m "feat(sftp): 单文件双向续传（三规则判定 + 同目标去重）"
```

---

## Task 12: 目录传输（枚举 + 阈值降级 + 逐文件 `.part` + 只重试）

**Files:**
- Modify: `internal/sftp/gosftp.go`（`GetTree/PutTree`）
- Modify: `internal/sftp/copy.go`（`scanTree` + 聚合进度）
- Test: `internal/sftp/copy_test.go`（阈值降级）、`internal/sftp/e2e_test.go`（目录往返）

**Interfaces:**
- Consumes: `ReadDirContext`（唯一 ctx 感知 API）、`PartName`
- Produces: `func scanLimitReached(files int, elapsed time.Duration) bool`（阈值 20000 / 5s）；`(g *GoBackend) GetTree/PutTree`（不支持续传，只重试）

- [ ] **Step 1: 写失败测试（阈值与字段闭环）**

`internal/sftp/copy_test.go` 追加：

```go
func TestScanLimitReachedAndDegradedProgress(t *testing.T) {
	if scanLimitReached(19999, 1*time.Second) {
		t.Fatal("未到阈值不应降级")
	}
	if !scanLimitReached(20001, 1*time.Second) {
		t.Fatal("超过 20000 文件必须降级")
	}
	if !scanLimitReached(10, 6*time.Second) {
		t.Fatal("超过 5s 必须降级")
	}
	p := degradedProgress("t1", "h", DirDownload, "d")
	if p.Total != -1 || p.FilesTotal != -1 || p.Phase != PhaseTransfer {
		t.Fatalf("降级后必须是 Total=-1/FilesTotal=-1/Phase=transfer, got %#v", p)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/sftp/ -run TestScanLimit -v`
Expected: FAIL —— `undefined: scanLimitReached`

- [ ] **Step 3: 实现**

`copy.go`：

**追加到 Task 7 已建的 `internal/sftp/copy.go`**（不要新建文件；该文件已有 package 头与 `io/os/sync/time` import）：

```go
const (
	scanMaxFiles   = 20000
	scanMaxElapsed = 5 * time.Second
)

func scanLimitReached(files int, elapsed time.Duration) bool {
	return files > scanMaxFiles || elapsed > scanMaxElapsed
}

func degradedProgress(id, host string, d Direction, name string) Progress {
	return Progress{ID: id, Host: host, Direction: d, Name: name, Total: -1, FilesTotal: -1, Phase: PhaseTransfer}
}
```

`GetTree`：用 `ReadDirContext` 递归（每批检查 `scanLimitReached` 与 `ctx.Err()`），逐文件调 `Get`（同一会话不可复用——每个文件走 `Get` 会独占会话；因此 `GetTree` 内部改用「一个会话 + 循环 copyFile」的私有函数 `copyFileWith`，避免逐个会话握手）。目录项 `req.Resume` 一律忽略（只重试）。

- [ ] **Step 4: 跑单测 + e2e**

```bash
go test ./internal/sftp/ -run TestScanLimit -v
E2E_RUN=TestTreeE2E bash e2e/test_local.sh
```

`TestTreeE2E`（追加到 `e2e_test.go`）：

```go
func TestTreeE2E(t *testing.T) {
	host, remote, ok := e2eEnv(t)
	if !ok {
		return
	}
	g := NewGoBackend(nil, nil)
	defer g.CloseAll()

	// 建本地树 a/b/c，各放一个小文件
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "a", "b", "c"), 0755); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"a/x.txt", "a/b/y.txt", "a/b/c/z.txt"} {
		if err := os.WriteFile(filepath.Join(src, p), []byte(p), 0600); err != nil {
			t.Fatal(err)
		}
	}
	target := remote + "/tree"
	if err := g.PutTree(TransferRequest{Host: host, Local: src + "/a", Remote: target, Atomic: true}, nil); err != nil {
		t.Fatalf("PutTree: %v", err)
	}
	// 并入语义：远端已有同名目录时不得嵌套出 target/a/a
	if err := g.PutTree(TransferRequest{Host: host, Local: src + "/a", Remote: target, Atomic: true}, nil); err != nil {
		t.Fatalf("PutTree 第二次（并入）: %v", err)
	}
	if _, err := g.List(host, "", target+"/a/a"); err == nil {
		t.Fatal("put -r 必须并入而非嵌套（不应出现 target/a/a）")
	}
	// 下载回来逐文件比对
	dst := t.TempDir()
	if err := g.GetTree(TransferRequest{Host: host, Remote: target, Local: dst + "/tree", Atomic: true}, nil); err != nil {
		t.Fatalf("GetTree: %v", err)
	}
	for _, p := range []string{"a/x.txt", "a/b/y.txt", "a/b/c/z.txt"} {
		want, _ := os.ReadFile(filepath.Join(src, p))
		got, err := os.ReadFile(filepath.Join(dst, "tree", p))
		if err != nil || !bytes.Equal(want, got) {
			t.Fatalf("文件不一致: %s err=%v", p, err)
		}
	}
	_ = g.RemoveRecursive(host, "", target)
}
```

（`e2eEnv(t) (host, remote string, ok bool)` 是本文件里的小 helper：读 `SSHORE_E2E_HOST/REMOTE`，缺失时 `t.Skip` 并返回 `ok=false`。）

- [ ] **Step 5: 提交**

```bash
git add internal/sftp/copy.go internal/sftp/copy_test.go internal/sftp/gosftp.go internal/sftp/e2e_test.go
git commit -m "feat(sftp): 目录传输（枚举阈值降级 + 逐文件 .part + 合并语义保持）"
```

---

## Task 13: 其余能力迁移 + 生命周期清理与 journal 恢复

**Files:**
- Modify: `internal/sftp/gosftp.go`（`List/ListMany/Home/Remove/RemoveRecursive/Mkdir/Rename/Connected`）
- Modify: `app.go`（`OnShutdown` 调 `Recover()` 与清理已知 `.part`；`startup` 清理陈旧临时文件）
- Modify: `internal/watch/scan.go`（`MatchExclude` 加内部临时文件判定）+ `internal/watch/scan_test.go`
- Modify: `internal/sync/ctrl.go`（`inScope` 单一判定点加同一判定）+ `internal/sync/ctrl_test.go`
- Test: `internal/sftp/e2e_test.go`（语义等价）、`internal/sftp/gosftp_test.go`（`Item.Mode`/`ModTime` 格式）

**Interfaces:**
- Consumes: `Pool`、`PosixRename`、`journal`
- Produces: `List/ListMany`（缺失 key = 未知）、`Home`、`Remove/RemoveRecursive`（保留旧失败语义）、`Rename`（`PosixRename` 覆盖）、`Connected`（池内活会话）

- [ ] **Step 1: 写失败测试（Item 形状与时间格式）**

`internal/sftp/gosftp_test.go`：

```go
package sftp

import (
	"testing"
	"time"
)

func TestItemFromFileInfoKeepsContract(t *testing.T) {
	it := itemFrom("a.txt", 123, false, 0644, time.Date(2026, 9, 18, 10, 30, 5, 0, time.UTC))
	if it.Name != "a.txt" || it.Size != 123 || it.IsDir {
		t.Fatalf("基本字段错: %#v", it)
	}
	if it.ModTime != "2026-09-18 10:30" {
		t.Fatalf("ModTime 必须逐字保持 2006-01-02 15:04（watch/sync 用它做字符串比较）, got %q", it.ModTime)
	}
	if it.Mode == "" {
		t.Fatal("Mode 必须填（UI 暂不使用，但字段不可空）")
	}
	zero := itemFrom("b.txt", 0, false, 0644, time.Time{})
	if zero.ModTime != "" {
		t.Fatalf("mtime 缺失/为零 → 空串（前端显示 —）, got %q", zero.ModTime)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/sftp/ -run TestItemFromFileInfo -v`
Expected: FAIL —— `undefined: itemFrom`

- [ ] **Step 3: 实现其余能力**

`gosftp.go`（技术审核 M3：这些都要真实代码，不能只写语义）：

```go
// itemFrom 是唯一构造 Item 的地方：ModTime 必须逐字保持 2006-01-02 15:04。
func itemFrom(name string, size int64, isDir bool, mode os.FileMode, mtime time.Time) Item {
	mt := ""
	if !mtime.IsZero() {
		mt = mtime.Format("2006-01-02 15:04")
	}
	return Item{Name: name, Size: size, IsDir: isDir, Mode: mode.String(), ModTime: mt}
}

func (g *GoBackend) List(host, user, path string) ([]Item, error) {
	s, err := g.pool.AcquireList(context.Background(), host, user)
	if err != nil {
		return nil, err
	}
	reusable := true
	defer func() { g.pool.Release(s, reusable) }()
	infos, err := s.Conn.ReadDirContext(context.Background(), path)
	if err != nil {
		reusable = false
		return nil, &TransferError{Op: "sftp ls", Host: host, Path: path, Err: err, RemoteMsg: s.Proc.StderrText()}
	}
	items := make([]Item, 0, len(infos))
	for _, fi := range infos {
		if fi.Name() == "." || fi.Name() == ".." {
			continue
		}
		items = append(items, itemFrom(fi.Name(), fi.Size(), fi.IsDir(), fi.Mode(), fi.ModTime()))
	}
	return items, nil
}

// ListMany：失败目录**缺席**于返回 map（绝不返回空切片）—— Search 与 sync 都依赖这条契约。
func (g *GoBackend) ListMany(host, user string, paths []string) (map[string][]Item, error) {
	s, err := g.pool.AcquireList(context.Background(), host, user)
	if err != nil {
		return nil, err
	}
	reusable := true
	defer func() { g.pool.Release(s, reusable) }()
	res := make(map[string][]Item, len(paths))
	for _, p := range paths {
		infos, err := s.Conn.ReadDirContext(context.Background(), p)
		if err != nil {
			continue // 缺席 = 未知
		}
		items := make([]Item, 0, len(infos))
		for _, fi := range infos {
			if fi.Name() == "." || fi.Name() == ".." {
				continue
			}
			items = append(items, itemFrom(fi.Name(), fi.Size(), fi.IsDir(), fi.Mode(), fi.ModTime()))
		}
		res[p] = items
	}
	return res, nil
}

func (g *GoBackend) Home(host, user string) (string, error) {
	s, err := g.pool.AcquireList(context.Background(), host, user)
	if err != nil {
		return "", err
	}
	defer g.pool.Release(s, true)
	return s.Conn.Getwd()
}

// RemoveRecursive 保留旧语义：拒绝根路径、目录不可读即整体失败（不静默漏删）。
func (g *GoBackend) RemoveRecursive(host, user, path string) error {
	if path == "" || path == "/" {
		return fmt.Errorf("拒绝递归删除根路径: %q", path)
	}
	s, err := g.pool.AcquireList(context.Background(), host, user)
	if err != nil {
		return err
	}
	reusable := true
	defer func() { g.pool.Release(s, reusable) }()
	w := s.Conn.Walk(path)
	var dirs []string
	for w.Step() {
		if err := w.Err(); err != nil {
			reusable = false
			return &TransferError{Op: "sftp rm -r", Host: host, Path: path, Err: err, RemoteMsg: s.Proc.StderrText()}
		}
		p := w.Path()
		if p == path {
			continue
		}
		if fi, err := s.Conn.Stat(p); err == nil && fi.IsDir() {
			dirs = append([]string{p}, dirs...) // 自底向上
			continue
		}
		if err := s.Conn.Remove(p); err != nil {
			reusable = false
			return &TransferError{Op: "sftp rm -r", Host: host, Path: p, Err: err, RemoteMsg: s.Proc.StderrText()}
		}
	}
	for _, d := range dirs {
		if err := s.Conn.RemoveDirectory(d); err != nil {
			reusable = false
			return &TransferError{Op: "sftp rm -r", Host: host, Path: d, Err: err}
		}
	}
	return nil
}

// Rename 必须覆盖已存在目标（保持旧 sftp rename 语义）→ 用 PosixRename，扩展缺失时明确失败。
func (g *GoBackend) Rename(host, user, oldPath, newPath string) error {
	s, err := g.pool.AcquireList(context.Background(), host, user)
	if err != nil {
		return err
	}
	defer g.pool.Release(s, true)
	if _, ok := s.Conn.HasExtension("posix-rename@openssh.com"); !ok {
		return &TransferError{Op: "sftp rename", Host: host, Path: oldPath, Err: errors.New("远端不支持原子覆盖改名，请先删除目标")}
	}
	return s.Conn.PosixRename(oldPath, newPath)
}
```

`Connect(host, user)` 用 `pool.Probe` 首次建会话并返回其错误；`Connected(host)` 用 `pool.Probe`（5s deadline）；`Search` 调 `searchBFS(ctx, g.ListMany, ...)`。

`app.go` 的 `OnShutdown`（`:968`）**顺序必须写死为三步**（技术审核 M5：先 CloseAll 才不会删掉在途 `.part`）：

```go
	// 1) 停传输、关会话（此时才没有在途写入）
	a.sftp.CloseAll()
	// 2) 按 journal 恢复 backup-swap 的中断现场
	if n, err := a.sftp.RecoverSwaps(); err != nil {
		logf("恢复 swap journal 失败: %v", err)
	} else if n > 0 {
		logf("恢复了 %d 处中断提交", n)
	}
	// 3) 清理已知 .part（本地 + 远端）
	a.sftp.CleanupParts()
```

门面加两个转发：`func (c *Ctrl) RecoverSwaps() (int, error)`、`func (c *Ctrl) CleanupParts()`（`GoBackend` 侧用 `knownParts` 枚举 + `journal.Recover()`）。启动时（`startup`）清理 >7 天且名字含 `PartMarker` 的本地临时文件。

**同一份改动里**把「内部临时文件」做成 sync/watch 的**内置忽略**（D18：不能依赖各规则自己的 `excludes`）。`internal/watch/scan.go`：

```go
// IsInternalTemp 判定 sshore 自己的临时文件；sync 的 align/ScanTree 与面板都不该把它当业务文件。
// 与 internal/sftp.PartMarker 同字面量；两边各自单测钉住。
func IsInternalTemp(rel string) bool {
	return strings.Contains(path.Base(rel), ".sshore-sftppart-")
}
```

在 `MatchExclude`（`watch/scan.go:39`）开头加：

```go
	if IsInternalTemp(rel) {
		return true
	}
```

并在 `internal/sync/ctrl.go` 的 `inScope`（`:542`）**同一个判定点**复用（`inScope` 是唯一的「路径要不要管」判定，`consume` 与 `align` 都走它）：

```go
	if watch.IsInternalTemp(rel) {
		return false
	}
```

`internal/watch/scan_test.go` 加一条：`IsInternalTemp("a/b.txt.sshore-sftppart-t1-ab") == true`、`IsInternalTemp("a/b.txt") == false`。

**同时更新那句已过时的注释**（spec D18 明确要求）：`internal/config/store.go:103-104` 现在写「不要放 *.part：那是我们本地临时文件的后缀，**远端不会出现**」—— 上传方向用远端 `.part` 后这句不再成立，改成：

```go
// DefaultExcludes 是远端路径的默认忽略集合（过滤的是**远端**路径）。
// 注意：sshore 自己的传输临时文件（中缀 .sshore-sftppart-）由 watch/sync 的**内置忽略**
// 处理（见 watch.IsInternalTemp），不依赖本表 —— 用户删改本表也不会让 .part 被当业务文件。
```

- [ ] **Step 4: 跑 e2e 语义等价**

```bash
go test ./internal/sftp/ -run TestItemFromFileInfo -v
E2E_RUN=TestCapabilitiesE2E bash e2e/test_local.sh
```

**「已知 `.part`」的枚举与清理（自审 M4）**：`GoBackend` 维护一张 `knownParts map[string]partPair`（`{local, remote string}`），在 `Get/Put` 生成本地/远端 `.part` 时登记、提交成功后注销；`CloseAll` 在 `pool.CloseAll()` **之前**遍历它做 best-effort 删除（下载 `os.Remove`、上传 `Remove`），随后清空。

**Linux e2e 的无残留进程与退出清理断言**（写成一条 e2e，勿只靠 Task 16 的 Windows tasklist）：

```go
func TestNoLeftoverOnCloseE2E(t *testing.T) {
	host, remote, ok := e2eEnv(t)
	if !ok {
		return
	}
	g := NewGoBackend(nil, nil)
	src := filepath.Join(t.TempDir(), "small.bin")
	if err := os.WriteFile(src, []byte("hello"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := g.Put(TransferRequest{ID: "p1", Host: host, Local: src, Remote: remote + "/small.bin", Atomic: true}, nil); err != nil {
		t.Fatal(err)
	}
	g.CloseAll()

	// 1) 退出后本地与远端 .part 都必须消失（这里以「远端也查不到」为准）
	if items, err := g.List(host, "", remote); err == nil {
		for _, it := range items {
			if strings.Contains(it.Name, PartMarker) {
				t.Fatalf("CloseAll 后仍有临时文件: %s", it.Name)
			}
		}
	}
	// 2) 无残留 ssh 子进程（Linux 用 pgrep；Windows 见 Task 16 的 tasklist）
	out, _ := exec.Command("pgrep", "-fa", "-s sftp").CombinedOutput()
	if len(bytes.TrimSpace(out)) > 0 {
		t.Fatalf("CloseAll 后仍有 ssh 子进程: %s", out)
	}
}
```

`TestCapabilitiesE2E`（追加到 `e2e_test.go`）：

```go
func TestCapabilitiesE2E(t *testing.T) {
	host := os.Getenv("SSHORE_E2E_HOST")
	remote := os.Getenv("SSHORE_E2E_REMOTE")
	if host == "" || remote == "" {
		t.Skip("未提供 SSHORE_E2E_*，跳过能力验证")
	}
	g := NewGoBackend(nil, nil)
	defer g.CloseAll()
	dir := remote + "/caps"
	if err := g.Mkdir(host, "", dir); err != nil {
		t.Fatal(err)
	}
	// rename 覆盖已存在目标必须成功（保持旧 sftp rename 语义）
	if err := g.Put(TransferRequest{Host: host, Local: writeTemp(t, "a"), Remote: dir + "/a.txt", Atomic: true}, nil); err != nil {
		t.Fatal(err)
	}
	if err := g.Put(TransferRequest{Host: host, Local: writeTemp(t, "b"), Remote: dir + "/b.txt", Atomic: true}, nil); err != nil {
		t.Fatal(err)
	}
	if err := g.Rename(host, "", dir+"/a.txt", dir+"/b.txt"); err != nil {
		t.Fatalf("rename 覆盖已存在目标必须成功: %v", err)
	}
	// ListMany 缺失 key = 未知（绝不当空目录）
	res, err := g.ListMany(host, "", []string{dir, dir + "/does-not-exist"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := res[dir+"/does-not-exist"]; ok {
		t.Fatal("列不出来的目录必须缺席于返回值")
	}
	// 不可读目录 → RemoveRecursive 整体失败
	if err := g.RemoveRecursive(host, "", dir+"/does-not-exist"); err == nil {
		t.Fatal("不存在的目录必须整体失败")
	}
	if err := g.RemoveRecursive(host, "", dir); err != nil {
		t.Fatal(err)
	}
}

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "f.txt")
	if err := os.WriteFile(p, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return p
}
```

- [ ] **Step 5: 提交**

```bash
git add internal/sftp/gosftp.go internal/sftp/gosftp_test.go internal/sftp/e2e_test.go app.go internal/sync/ctrl.go internal/sync/ctrl_test.go internal/watch/scan.go internal/watch/scan_test.go
git commit -m "feat(sftp): 迁移列举/删除/改名等能力 + 退出清理与 journal 恢复"
```

---

## Task 14: 前端（进度/取消/重试/续传/清理 + `.part` 隐藏）

**Files:**
- Modify: `frontend/src/utils/queue.js`
- Create: `frontend/src/utils/queue.test.js`（若已存在则追加）
- Modify: `frontend/src/components/TransferQueue.vue`
- Modify: `frontend/src/views/SftpView.vue`

**Interfaces:**
- Consumes: 事件 `sftp:transfer-progress`、绑定 `SftpGet(id,…)/SftpTransferCancel(id)`
- Produces: `percentOf(t)`、`speedOf(t, now)`、`etaOf(t, now)`、`fmtSpeed(n)`、`isInternalTempName(name)`

- [ ] **Step 1: 写失败测试**

`frontend/src/utils/queue.test.js`：

```js
import { describe, it, expect } from 'vitest'
import { percentOf, speedOf, etaOf, isInternalTempName } from './queue'

describe('percentOf', () => {
  it('total 未知（<0）返回 null（不定进度）', () => {
    expect(percentOf({ done: 5, total: -1 })).toBe(null)
  })
  it('0 字节文件完成即 100%', () => {
    expect(percentOf({ done: 0, total: 0 })).toBe(100)
  })
  it('超过 100% 时钳制', () => {
    expect(percentOf({ done: 120, total: 100 })).toBe(100)
  })
  it('乱序事件导致回退也钳到 0', () => {
    expect(percentOf({ done: -5, total: 100 })).toBe(0)
  })
})

describe('speedOf / etaOf', () => {
  it('有起点与时间差时给出速度（字节/秒）', () => {
    const t = { done: 1000, total: 2000, startedAt: 0 }
    expect(speedOf(t, 2000)).toBe(500) // 1000B / 2s
  })
  it('起点为 0 或时间差为 0 时速度为 0（不产生 Infinity/NaN）', () => {
    expect(speedOf({ done: 10, total: 10, startedAt: 0 }, 0)).toBe(0)
  })
  it('未知总量时 eta 为 null', () => {
    expect(etaOf({ done: 10, total: -1, startedAt: 0 }, 1000)).toBe(null)
  })
})

describe('isInternalTempName', () => {
  it('认出常规名与退化短名（中缀判定）', () => {
    expect(isInternalTempName('a.txt.sshore-sftppart-t1-ab12')).toBe(true)
    expect(isInternalTempName('.sshore-sftppart-t1-ab12')).toBe(true)
  })
  it('不误判普通文件', () => {
    expect(isInternalTempName('a.txt')).toBe(false)
    expect(isInternalTempName('a.txt.bak')).toBe(false)
  })
})
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd frontend && npx vitest run src/utils/queue.test.js`
Expected: FAIL —— `percentOf is not a function`

- [ ] **Step 3: 实现纯函数**

`queue.js` 追加：

```js
export const PART_MARKER = '.sshore-sftppart-'

export function isInternalTempName(name) {
  return String(name || '').includes(PART_MARKER)
}

export function percentOf(t) {
  const total = Number(t && t.total)
  const done = Number((t && t.done) || 0)
  if (!Number.isFinite(total) || total < 0) return null
  if (total === 0) return 100
  return Math.max(0, Math.min(100, (done / total) * 100))
}

export function speedOf(t, now) {
  const started = Number((t && t.startedAt) || 0)
  const secs = (Number(now) - started) / 1000
  if (!(secs > 0)) return 0
  return Number((t && t.done) || 0) / secs
}

export function etaOf(t, now) {
  const p = percentOf(t)
  if (p === null || p >= 100) return null
  const sp = speedOf(t, now)
  if (!(sp > 0)) return null
  const remain = Number(t.total) - Number(t.done || 0)
  return Math.max(0, Math.round(remain / sp))
}

export function fmtSpeed(bytesPerSec) {
  if (!(bytesPerSec > 0)) return '--'
  const units = ['B/s', 'KB/s', 'MB/s', 'GB/s']
  let v = bytesPerSec, i = 0
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++ }
  return v.toFixed(v >= 100 || i === 0 ? 0 : 1) + ' ' + units[i]
}
```

- [ ] **Step 4: 跑 vitest 确认通过**

Run: `cd frontend && npx vitest run`
Expected: 98 + 新增全绿

- [ ] **Step 5: 接线 UI**

`TransferQueue.vue`：每行加进度条（`percentOf`，`null` 时用不定条）、`fmtSpeed(speedOf(t, now))`、ETA（`etaOf`）；`status==='处理中'` 显示「取消」按钮（`emit('cancel', t)`），`失败/取消` 显示「重试」「续传」「清理」（`emit('retry'|'resume'|'clean', t)`）。

`SftpView.vue`：`runBatch` 每项生成 id（`t<seq>-<n>`）并保存 `partPath`；订阅 `sftp:transfer-progress`（`onActivated` 订阅、`onDeactivated` 用保存的退订函数退订，先例见 `SftpView.vue:695/704`）；两个面板的 item 列表过滤 `isInternalTempName`。四个动作的落法：

- `cancel` = `SftpTransferCancel(rec.id)` + 停止派发后续项（已完成项保留）；
- `retry` = 用 `rec.src/rec.dst/direction` 重跑该项（`resume=false, partPath=''`，后端会先删同名 `.part`）；
- `resume` = 同 `retry` 但 `resume=true, partPath=rec.partPath`（仅 `!rec.isDir` 项显示该按钮）；
- `clean` = 下载方向 `DeleteLocal(rec.partPath)`、上传方向 `SftpRemove(host,'',rec.partPath)`（都走既有绑定，失败只提示不抛）。

- [ ] **Step 6: 跑全量前端验证并提交**

```bash
cd frontend && npm run build && npx vitest run
cd .. && git add frontend/src
git commit -m "feat(web): 传输队列进度/取消/重试/续传/清理 + 临时文件隐藏"
```

---

## Task 15: e2e 基建（双包 + 后端矩阵 + 垫片透传 `-s`）

**Files:**
- Modify: `e2e/test_local.sh`（`:219-231` 附近）
- Modify: `internal/sync/e2e_test.go`（`:28` 的 `LookPath("sftp")` → `"ssh"`）

**Interfaces:**
- Consumes: Task 6–13 的 e2e 用例
- Produces: `e2e/test_local.sh` 同时跑 `./internal/sftp/` 与 `./internal/sync/`，并对 `SSHORE_SFTP_TRANSPORT` 跑 `batch`/`gosftp` 两遍

- [ ] **Step 1: 改脚本（先让它失败）**

把 `e2e/test_local.sh` 末尾的单次 `go test` 改成：

```bash
echo "== go tests: sftp + sync, 后端矩阵 =="
for backend in batch gosftp; do
  echo "--- backend=$backend ---"
  HOME="$HOME" SSHORE_SFTP_TRANSPORT="$backend" go test ./internal/sftp/ ./internal/sync/ -run 'E2E$' -count=1 -v || exit 1
done
```

- [ ] **Step 2: 跑脚本确认失败**

Run: `bash e2e/test_local.sh`
Expected: **PASS**（自审 M13 修正：Task 15 在 6-13 之后，接线已完成；循环本身在 Task 6 就装好了）。若这里 FAIL，按失败信息修：`batch` 遍失败 ⇒ 垫片/环境问题；`gosftp` 遍失败 ⇒ Task 6-13 有回归。

- [ ] **Step 3: 修垫片与 skip 条件**

`e2e/test_local.sh` 的 `ssh` 垫片（`:219-227`）必须**原样透传 `-s`**（不能被参数重写吞掉）；确认 `ssh` 垫片内容形如：

```bash
cat > "$SHIM/ssh" <<SHIMSSH
#!/bin/sh
exec /usr/bin/ssh -F "$HOME/.ssh/config" -o IdentitiesOnly=yes "\$@"
SHIMSSH
```

`internal/sync/e2e_test.go:28` 的 `exec.LookPath("sftp")` 改成 `exec.LookPath("ssh")`（新底座只要 ssh）。

再在 `internal/sync/ctrl_test.go` 加一条断言「远端出现内部临时文件时不得进入规则范围」（Task 13 的内置忽略）：

```go
func TestInScopeRejectsInternalTemp(t *testing.T) {
	rule := config.SyncRule{Host: "h", RemotePath: "/d", MaxDepth: -1}
	if inScope(rule, "sub/a.bin"+".sshore-sftppart"+"-t1-ab") {
		t.Fatal("内部临时文件不得进入规则范围（否则 sync 会把传输中的 .part 当新文件下载）")
	}
	if !inScope(rule, "sub/a.bin") {
		t.Fatal("业务文件必须仍在范围内")
	}
}
```

- [ ] **Step 4: 跑脚本确认通过**

Run: `bash e2e/test_local.sh`
Expected: PASS 两遍（batch 与 gosftp），且 `gosftp` 遍打印 `posix-rename 能力: true`

- [ ] **Step 5: 提交**

```bash
git add e2e/test_local.sh internal/sync/e2e_test.go
git commit -m "test(e2e): 临时 sshd 同时跑 sftp/sync 两包与双后端矩阵"
```

---

## Task 16: 文档 + 默认切换 + Windows 真机验收与发布

**Files:**
- Modify: `README.md`（`:22` 依赖、`:164-174` 架构清单、配置段）
- Modify: `internal/sftp/backend.go`（`defaultTransport` 切 `KindGo`）
- Modify: `docs/superpowers/specs/2026-09-18-sftp-gosftp-transport-design.md`（把 D1 的 `-s` 写法与 D15 的 Windows 结论按 Task 0 实测回写）

**Interfaces:**
- Consumes: 前序所有 task
- Produces: v0.7.0 可发布状态

- [ ] **Step 1: Windows 真机验收（人眼 + 三点进程断言）**

先构建产物：

```bash
make windows   # 见 Makefile 的 windows 目标；产物在 build/bin/
```

再在 `win10` 客户机上安装本轮构建的 `sshore-windows-amd64.exe`。**Seek(partSize) 续传视为「待边界验证」**：Task 0 只验了文件内部回填（Seek(3) on 5B），未验 Seek(5)/Seek(7)（末尾与越过 EOF）→ 真机验收第一步先补这两个边界跑，再验续传（Task 0 评审 Minor-3）。

**必须在交互桌面会话（Session 1）里运行**（Task 0 的 Session 0 现象；用 schtasks /it 或直接桌面启动），且客户机免密键名非默认（`id_winlocal`），本机自连验证要带 `-i`。逐项打勾：

- 进度条在下载/上传都随字节推进；取消按钮在百毫秒级生效且目标名不出现半截文件；
- 失败项「重试」「续传」可用；续传后 sha256 与一次性完整传输一致（在客户机上用 `certutil -hashfile` 比对）；
- 传输后 / 取消后 / 退出后各跑一次：`tasklist /FI "IMAGENAME eq ssh.exe"` 必须无残留；
- 大文件（>2GB）与长文件名（>200 字节名）各一笔，确认短名退化路径可用。

**任一项失败 ⇒ 不改默认，停在 `batch` 并在 spec 里记 issue。**

- [ ] **Step 2: 切默认（仅在 Step 1 全绿后）**

`internal/sftp/backend.go`：`const defaultTransport = KindGo`；跑 `make ci` 全绿。

- [ ] **Step 3: 文档更新**

`README.md`：`:22` 依赖改成「系统 `ssh`（新传输底座不再需要 `sftp` 二进制；用 `sftp_transport = "batch"` 回退时需要）」；架构清单 `:164-174` 的 `internal/sftp` 一行改成「门面 + 双后端（`batch` / `gosftp`），会话池 + `pkg/sftp`」；配置示例段加 `sftp_transport = ""  # ""/auto | "gosftp" | "batch"`。

- [ ] **Step 4: 全量回归门**

```bash
make ci
bash e2e/test_local.sh
```

Expected: 全绿（`npm run build` + `go vet` + `go test -race` + vitest；两包 e2e × 双后端）。

- [ ] **Step 5: 提交**

```bash
git add README.md internal/sftp/backend.go docs/superpowers/specs/
git commit -m "feat(sftp): 默认切 gosftp 底座 + 文档更新（README 依赖/架构/配置段）"
```

---

## 后续（不在本计划内，v0.8 另立计划）

- 删除 `BatchBackend`、`NewBatchBackend`、`parser.go`、`listmany_parse.go`、`backend.go` 的选择器与 `[app] sftp_transport` 配置键（三处），以及 `e2e/test_local.sh` 的批处理时代用例与探针（TEST 3 `:102-111`、PROBE A/B/C `:113-195`、`:215-227` 的 `sftp` 垫片）。
- 跨重启续传、目录级逐文件续传、并发传输、限速、校验和 UI（spec §3.2 非目标）。

---

## Self-Review

**1. Spec coverage（逐节对照）**

| spec 要求 | 落点 |
|---|---|
| D1 连接归 OpenSSH | Task 0（写法探测）+ Task 6（dial） |
| D2 会话池/状态机/非阻塞 | Task 4 |
| D3 开关（env > config > 默认、三处、前端 load/save、懒解析） | Task 1 + Task 16（切默认） |
| D4 包布局/门面双面/raw-pipe | Task 5（门面）+ Task 3（raw-pipe） |
| D5 Item/ModTime 格式 | Task 13（`itemFrom` 单测） |
| D6 语义等价（参数基准、rename 覆盖、缺失 key） | Task 13 + Task 12（合并语义） |
| D7 进度真实性 + 节流 + 末帧 | Task 7（`progressEmitter`）+ Task 9（事件） |
| D8/D16 枚举与阈值降级 | Task 12 |
| D9 取消整批 + 幂等 + 四断言 | Task 10 |
| D10 重试/续传（Three 规则、去重、单文件） | Task 11 |
| D11 递归语义与 OpenSSH 一致 | Task 12 |
| D12 错误模型/远端原文/stderr 合并 | Task 6（stderr）+ Task 7/8（`TransferError`） |
| D13 Connect/Disconnect/Connected | Task 4 + Task 13 |
| D14 生命周期/清理/journal | Task 8（journal）+ Task 13（退出清理） |
| D15 原子落盘/能力探测/backup-swap/短名退化 | Task 2 + Task 7 + Task 8 |
| D17 进度 UI 范围 | Task 14（不做批级聚合） |
| D18 内部临时文件治理 | Task 2（中缀）+ Task 14（面板过滤）+ Task 13（sync/watch 忽略，见下） |
| §9.0 决策→验证矩阵 | 各 task 的 e2e 用例 |

**D18 的 sync/watch 内置忽略**：已落到 **Task 13 Step 3**（`watch.IsInternalTemp` + `MatchExclude`/`inScope` 两处判定 + 单测）与 **Task 15 Step 3**（`TestInScopeRejectsInternalTemp` 断言）。执行完 Task 13/15 后，D1–D18 与 spec §9 矩阵均有落点。

**2. Placeholder scan**：无 `TBD`/`TODO`/「稍后补」；`gosftp.go` 在 Task 6 使用 `errors.New("未实现")` **不是**未定义占位，而是**可编译、可测试**的中间态（Task 6 的 e2e 已相应只断言会话与能力探测），且每处都标明补齐它的 task（7/8/11/12/13）。

**3. Type consistency**：全计划统一使用 `TransferRequest{ID,Host,User,Remote,Local,Resume,PartPath,Atomic}`、`Progress{ID,Host,Direction,Name,PartPath,Done,Total,FilesDone,FilesTotal,Phase}`、`BatchBackend/NewBatchBackend`、`GoBackend/NewGoBackend`、`Pool/NewPool/AcquireList/AcquireTransfer/Release/Probe/Disconnect/CloseAll`、`PartName/ShortPartName/BakName/IsInternalTemp/PartMarker`、`decideCommit/commitOK/commitShortRead`、`decideResume/resumeFull/resumeAppend/resumeCommit`、`progressEmitter.send(Progress, bool)`、`startPipes/PipedProcess`、绑定 `SftpGet(id,host,user,remote,local,resume,partPath)` 与 `SftpTransferCancel(id)`；前端统一 `percentOf/speedOf/etaOf/fmtSpeed/isInternalTempName/PART_MARKER`（与 Go 侧 `PartMarker` 同字面量，两边各自单测钉住）。
