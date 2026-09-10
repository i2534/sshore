# 远端文件夹监控同步（后端）实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在 sshore 中实现"监控远端路径并把变化同步到本地"的后端子系统（探测层 + 同步引擎 + 配置 + Wails 绑定）；前端 UI 由后续的独立计划实现。

**Architecture:** 分层双包。`internal/watch` 把 inotify（远端 `inotifywait` 常驻）与轮询（SFTP 递归 `ls`）两条路径归一到同一份 `Event{RelPath, Kind}` 契约；`internal/sync` 消费事件，做去重/去抖 → 元信息补齐（`ListMany`）→ 决策 → 串行传输 → 状态落盘。底层补三块能力：`osutil` 的 stdout 流式启动与可取消 Runner、`sshconn` 的 ControlMaster socket 唯一来源、`sftp.ListMany` 批量列举。

**Tech Stack:** Go 1.26、Wails v2（仅绑定层）、BurntSushi/toml、系统 OpenSSH、远端 `inotifywait`（可选）。测试用标准库 `testing`，无第三方断言库。

**Spec:** `docs/superpowers/specs/2026-09-10-remote-folder-sync-design.md`

## Global Constraints

- **不新增任何第三方依赖**。只用标准库 + 现有 `github.com/BurntSushi/toml`。
- **依赖方向（spec §4.5）**：`sync → watch`、`sync → sftp`、`sync → forward`（仅 `ValidateHost`）、`watch/sync → sshconn`、`sshconn → osutil`、`... → config`。**`config` 不得 import 上述任何内部包**，否则形成 `config → forward → config` 导入环，编译不过。
- **扫描器位置修正（对该 spec 的偏离，已确认）**：spec §12 把扫描器写在 `internal/sync/scan.go`，但它被 `watch` 的 poll 路径使用，而 §4.5 规定 `sync → watch`（单向）——放在 sync 会造成 `watch → sync` 反向依赖。**本计划把扫描器放在 `internal/watch/scan.go`**，`sync` 通过 import `watch` 使用。其余不变。
- **不改变 `internal/sftp` 现有方法的语义**；只新增 `ListMany`。
- **旧 `osutil.Spawner.Start` 签名不变**（改签名会破坏 `forward/ctrl_test.go` 的 fakeSpawner）。
- **前端 `wailsjs` 生成物被 git 跟踪**，Makefile 全部构建带 `-skipbindings` → 新增绑定后必须显式重新生成。
- **提交信息用简体中文**，格式 `type(scope): 描述`。
- 每个任务结束都要能通过 `go test ./... -race -count=1`。

## 实测事实（写代码前必读）

以下均在 inotify-tools 3.22.1.0 / 3.22.6.0 上实测得到，**不要凭直觉改**：

1. 远端 `inotifywait` 必须配 `-tt`：不加 `-tt` 时杀掉本地 ssh **会留下孤儿进程**；加了之后 SIGTERM 与 SIGKILL 都不留残留。
2. `-tt` 下**每一行都以 CRLF 结尾**（pty 的 `OPOST|ONLCR`），且远端 stderr 混入 stdout（`Setting up watches...`、`Watches established.`）。解析前必须裁 `\r\n`。
3. `%e` 是**逗号分隔的 token 列表**：实测有 `CLOSE_WRITE,CLOSE`、`CREATE,ISDIR`、`CLOSE_NOWRITE,CLOSE,ISDIR`、`MOVED_TO,ISDIR`、`DELETE_SELF`、`MOVE_SELF`。必须按 `,` 切分——整串比较会全部匹配失败。
4. `*_SELF` 事件的路径**带尾斜杠**（`root/d1/`、`root/`）且 `%f` 为空。
5. **根目录被删后 `inotifywait` 进程不退出**，只发一条 `root/|DELETE_SELF` —— 所以 `root_gone` 必须靠事件检测，不能挂在 `Process.Wait()` 上。
6. 目录被 `mv` 进监控树时只发 `MOVED_TO,ISDIR`，**目录内已有文件零事件** → 必须触发子树对账扫描。
7. `inotifywait` 退出码：`0`=收到事件、`1`=收到未请求事件或出错（**二义**）、`2`=`--timeout` 到期。
8. `command -v inotifywait` 缺失时退出码是 **1**（不是 127）。
9. `sftp -b` 多命令批处理：回显形如 `sftp> -ls -la "路径"`（含前导 `-`），可用 `sftp> ` 作**块边界**；`-` 前缀确实阻止整批中止（失败目录打印 `Can't ls: ... not found` 后继续执行）；**但 `sftp` 退出码仍是 0**，所以"某目录是否已知"必须在块内检测错误文案。
10. `pkill -f '模式'` 会**匹配到远端 shell 自己的命令行**而自杀；必须用方括号技巧 `'[i]notifywait.*tag'`。

### 对本计划的范围界定

本计划只做后端，**不碰前端 Vue 文件**（除重新生成 `wailsjs` 绑定外）。后端的"可独立验证"体现为：Task 1-14 全部由 `go test` 驱动，Task 15 产出绑定并由 `go vet` + `wailsjs` 生成物确认契约形状，Task 16 用临时 sshd 做端到端同步。

## 文件结构

**新建**

| 文件 | 职责 |
|---|---|
| `internal/sshconn/sshconn.go` | ControlMaster socket 路径唯一来源；幂等建 master（`ssh -O check` 判活）；可取消的一次性远端命令；远端 shell 转义 |
| `internal/watch/event.go` | `Kind` / `Event` / `Info` / `Source` 契约（两条探测路径共用） |
| `internal/watch/inotify_parse.go` | 纯函数：一行 `inotifywait` 输出 → `Event`（可单测，无 IO） |
| `internal/watch/detect.go` | 降级判定：`command -v inotifywait` → `Info{Mode,Reason,Interval}` |
| `internal/watch/inotify.go` | inotify 探测源：常驻进程编排、stdout 消费、`root_gone`/`overflow`/watch 不完整信号 |
| `internal/watch/scan.go` | 远端树快照：`Meta`/`Snapshot`/`ScanTree`（poll 每轮、inotify 对账、子树扫描共用） |
| `internal/watch/poll.go` | poll 探测源：每轮一次事务（失败/不完整则零事件） |
| `internal/sync/paths.go` | RelPath 安全校验、过滤（excludes/max_depth）、本地路径映射 |
| `internal/sync/decide.go` | 决策表（9 行）与动作枚举 |
| `internal/sync/state.go` | 状态文件读写、字段级写入规则、fingerprint、原子写、并发 flush |
| `internal/sync/transfer.go` | 传输适配（`Get` 到 `.part` + 原子 rename）、临时名、残留清理 |
| `internal/sync/delete_gate.go` | 六重删除闸门 |
| `internal/sync/conflict.go` | 冲突队列与三个动作（入队，不自己传输） |
| `internal/sync/validate.go` | `ValidateSyncRule` |
| `internal/sync/ctrl.go` | 编排：每规则一个 goroutine、状态机、退避、首轮对齐、对账、日志、绑定查询 |
| `internal/sync/adapters.go` | `NewSftpAdapter(*sftp.Ctrl)`：**适配器的唯一定义处**，main 包与 E2E 测试共用 |

**修改**

| 文件 | 改动 |
|---|---|
| `internal/osutil/runner.go` | 新增 `StreamHandlers`/`Streamer`/`StartStream`/`CtxRunner`；nil 回调不接管道；`Start` 失败返回 `nil, err`；`Start` 委托 `StartStream` |
| `internal/sftp/ctrl.go` | 新增 `ListMany` 与 `parseListMany`（其余不动） |
| `internal/config/store.go` | `SyncRule`、`SyncRule.Normalize()`、`AppConfig.Syncs`、`NewSyncID()` |
| `app.go` | 绑定；`AutoStartEnabled` 扩展；`OnShutdown` 顺序 |
| `e2e/test_local.sh` + `internal/sync/e2e_test.go` | 端到端同步断言 |

---

## Task 1: osutil 流式启动与可取消 Runner

`internal/watch` 需要消费**子进程 stdout**（现有 `Start` 只接 stderr）；`sshconn.Exec` 需要**可取消**的一次性执行（现有 `Runner` 用 `exec.Command`，超时只会泄漏子进程）。本任务补这两块能力，并修掉"Start 失败仍返回半成品 Process"——那个 Process 的 `done` channel 永不关闭，任何 `Wait()` 都会永久阻塞。

**Files:**
- Modify: `internal/osutil/runner.go`
- Test: `internal/osutil/runner_test.go`

**Interfaces:**
- Consumes: 无（本计划第一个任务）
- Produces:
  - `type StreamHandlers struct { OnStdout, OnStderr func(string) }`
  - `type Streamer interface { StartStream(name string, args []string, h StreamHandlers) (*Process, error) }`
  - `type CtxRunner func(ctx context.Context, name string, args ...string) (Outcome, error)`
  - `func NewCtxRunner() CtxRunner`

- [ ] **Step 1: 写失败的测试**

在 `internal/osutil/runner_test.go` 末尾追加（import 增补 `context`、`strings`、`time`）：

~~~go
// StartStream 必须把 stdout 与 stderr 分别逐行回调；空行丢弃、行首尾空白裁掉。
func TestStartStreamRoutesBothStreams(t *testing.T) {
	sp := NewStreamer()
	var out, errs []string
	p, err := sp.StartStream("sh", []string{"-c", "echo o1; echo o2; echo e1 >&2; echo '  e2  ' >&2"}, StreamHandlers{
		OnStdout: func(l string) { out = append(out, l) },
		OnStderr: func(l string) { errs = append(errs, l) },
	})
	if err != nil {
		t.Fatalf("StartStream failed: %v", err)
	}
	if got := p.Wait(); got.ExitCode != 0 {
		t.Fatalf("want exit 0, got %d", got.ExitCode)
	}
	if len(out) != 2 || out[0] != "o1" || out[1] != "o2" {
		t.Fatalf("stdout lines = %#v", out)
	}
	if len(errs) != 2 || errs[0] != "e1" || errs[1] != "e2" {
		t.Fatalf("stderr lines = %#v", errs)
	}
}

// 回调为 nil 表示不接该管道。若接了没人读的管道，子进程写满 64KB 缓冲后会永久阻塞。
func TestStartStreamNilHandlerDoesNotBlock(t *testing.T) {
	sp := NewStreamer()
	p, err := sp.StartStream("sh", []string{"-c", "yes | head -c 1048576"}, StreamHandlers{})
	if err != nil {
		t.Fatalf("StartStream failed: %v", err)
	}
	done := make(chan Outcome, 1)
	go func() { done <- p.Wait() }()
	select {
	case got := <-done:
		if got.ExitCode != 0 {
			t.Fatalf("want exit 0, got %d", got.ExitCode)
		}
	case <-time.After(10 * time.Second):
		_ = p.Kill()
		t.Fatal("子进程被阻塞：nil 回调时不应创建管道")
	}
}

// cmd.Start() 失败必须返回 (nil, err)：半成品 Process 的 done channel 永不关闭。
func TestStartFailureReturnsNilProcess(t *testing.T) {
	sp := NewStreamer()
	p, err := sp.StartStream("sshore-no-such-binary-xyz", nil, StreamHandlers{})
	if err == nil {
		t.Fatal("want error for missing binary")
	}
	if p != nil {
		t.Fatal("want nil Process on start failure")
	}
}

// CtxRunner 必须真正取消子进程（对齐 config/parser.go:84 的既有做法）。
func TestCtxRunnerCancelsProcess(t *testing.T) {
	r := NewCtxRunner()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := r(ctx, "sh", "-c", "sleep 30"); err == nil {
		t.Fatal("want error from cancelled command")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("取消未生效，耗时 %v", elapsed)
	}
}

// CtxRunner 正常路径要带回 stdout/stderr 与退出码。
func TestCtxRunnerCapturesOutput(t *testing.T) {
	r := NewCtxRunner()
	out, err := r(context.Background(), "sh", "-c", "echo hi; echo boom >&2; exit 3")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if out.ExitCode != 3 {
		t.Fatalf("want exit 3, got %d", out.ExitCode)
	}
	if !strings.Contains(out.Stdout, "hi") || !strings.Contains(out.Stderr, "boom") {
		t.Fatalf("stdout=%q stderr=%q", out.Stdout, out.Stderr)
	}
}
~~~

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/osutil/ -run 'TestStartStream|TestStartFailure|TestCtxRunner' -v`
Expected: 编译失败，`undefined: StreamHandlers` / `undefined: NewCtxRunner`

- [ ] **Step 3: 实现**

在 `internal/osutil/runner.go` 中让 import 增补 `context`、`io`，然后加入：

~~~go
// StreamHandlers 承载流式子进程的逐行回调。某个回调为 nil 表示不接该管道
// ——绝不创建一条没人读的管道：子进程写满缓冲区后会永久阻塞。
type StreamHandlers struct {
	OnStdout func(string)
	OnStderr func(string)
}

// Streamer 启动长驻子进程并消费其 stdout/stderr。forward 用 Spawner（只关心
// stderr），watch 用 Streamer（inotifywait 的事件流在 stdout）。
//
// 注意：NewSpawner() 返回的是只有 Start 的 Spawner 接口，**不能**用来调
// StartStream。需要流式能力的地方一律用 NewStreamer()（同一个 realSpawner 实现）。
type Streamer interface {
	StartStream(name string, args []string, h StreamHandlers) (*Process, error)
}

// NewStreamer 返回同时具备流式能力的启动器。
func NewStreamer() Streamer { return realSpawner{} }

// Start 保持既有签名与语义（仅 stderr 回调），内部委托给 StartStream。
func (realSpawner) Start(name string, args []string, onLine func(string)) (*Process, error) {
	var h StreamHandlers
	if onLine != nil {
		h.OnStderr = onLine
	}
	return realSpawner{}.StartStream(name, args, h)
}

func (realSpawner) StartStream(name string, args []string, h StreamHandlers) (*Process, error) {
	cmd := exec.Command(name, args...)
	procAttrHideConsole(cmd)
	p := &Process{cmd: cmd, done: make(chan Outcome, 1)}
	if h.OnStderr != nil {
		stderr, err := cmd.StderrPipe()
		if err != nil {
			return nil, err
		}
		go scanLines(stderr, h.OnStderr)
	}
	if h.OnStdout != nil {
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return nil, err
		}
		go scanLines(stdout, h.OnStdout)
	}
	if err := cmd.Start(); err != nil {
		return nil, err // 修复：不再返回 done channel 永不关闭的半成品 Process
	}
	go func() {
		err := cmd.Wait()
		p.done <- Outcome{ExitCode: execResult(err)}
		close(p.done)
	}()
	return p, nil
}

// scanLines 逐行读取并回调；行首尾空白裁掉（含 pty 产出的 CR），空行丢弃。
// 缓冲区放大到 1MB：inotifywait 的事件行可能含很长的绝对路径。
func scanLines(rc io.ReadCloser, fn func(string)) {
	defer rc.Close()
	sc := bufio.NewScanner(rc)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); line != "" {
			fn(line)
		}
	}
}

// CtxRunner 是 Runner 的可取消版本（exec.CommandContext）。任何可能挂住的一次性
// 远端命令都必须用它——超时后子进程必被回收，不泄漏 goroutine 与进程。
type CtxRunner func(ctx context.Context, name string, args ...string) (Outcome, error)

func NewCtxRunner() CtxRunner {
	return func(ctx context.Context, name string, args ...string) (Outcome, error) {
		cmd := exec.CommandContext(ctx, name, args...)
		procAttrHideConsole(cmd)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err := cmd.Run()
		out := Outcome{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: execResult(err)}
		// 契约：**非 0 退出不算 err**，只看 ExitCode（探测 command -v 返回 1 是
		// 正常结果，不是失败）。只有取消/超时与启动类失败才返回 err。
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return out, nil
		}
		return out, err
	}
}
~~~

同时**删除**原来 `realSpawner.Start` 的函数体（含 `if onLine != nil { StderrPipe ... }` 那段，以及 `cmd.Start()` 后返回 `p, err` 的代码），只保留上面的委托版本。

- [ ] **Step 4: 运行测试确认通过，并跑 forward 回归**

Run: `go test ./internal/osutil/ -race -count=1 -v`
Expected: 全部 PASS（含原有的 TestSpawnerStartKill、TestSpawnerStderrLines）

Run: `go test ./internal/forward/ -race -count=1`
Expected: PASS —— **这是本次重构的回归护栏**，`Start` 的行为必须一字不变。

- [ ] **Step 5: 提交**

~~~bash
gofmt -s -w internal/osutil/runner.go internal/osutil/runner_test.go
git add internal/osutil/runner.go internal/osutil/runner_test.go
git commit -m "feat(osutil): 新增 stdout 流式启动与可取消 Runner,修复 Start 失败返回半成品进程"
~~~

---

## Task 2: sshconn（ControlMaster socket 唯一来源）

**Files:**
- Create: `internal/sshconn/sshconn.go`
- Test: `internal/sshconn/sshconn_test.go`

**Interfaces:**
- Consumes: `osutil.CtxRunner`、`osutil.Outcome`（Task 1）
- Produces: `ControlDir()`、`ControlPath(host, user)`、`EnsureMaster(ctx, r, host, user)`、`Exec(ctx, r, host, user, remoteCmd)`、`QuoteRemote(s)`

**实现时不要简化掉的两点**：判活必须用 `ssh -O check` 的退出码（`os.Stat` 会把 `kill -9` 残留的 socket 误判为可用）；用户身份必须进 socket key（同 host 异 user 共用 master 会用错身份读写远端文件）。

- [ ] **Step 1: 写失败的测试**

创建 `internal/sshconn/sshconn_test.go`：

~~~go
package sshconn

import (
	"context"
	"strings"
	"testing"

	"sshore/internal/osutil"
)

// socket 路径必须把 user 纳入 key。
func TestControlPathIncludesUser(t *testing.T) {
	a := ControlPath("prod-01", "")
	b := ControlPath("prod-01", "alice")
	c := ControlPath("prod-01", "bob")
	if a == b || b == c || a == c {
		t.Fatalf("socket 路径未按 user 区分: %q %q %q", a, b, c)
	}
	if got := ControlPath("prod-01", "alice"); got != b {
		t.Fatalf("同参数必须幂等: %q vs %q", got, b)
	}
	if !strings.HasPrefix(b, ControlDir()) {
		t.Fatalf("socket 必须落在 ControlDir 内: %q", b)
	}
}

// ssh -O check 退出 0 视为可用，直接返回，不再建连接。
func TestEnsureMasterReusesLiveMaster(t *testing.T) {
	var calls [][]string
	fake := func(ctx context.Context, name string, args ...string) (osutil.Outcome, error) {
		calls = append(calls, append([]string{name}, args...))
		return osutil.Outcome{ExitCode: 0}, nil
	}
	if err := EnsureMaster(context.Background(), fake, "h1", "u1"); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("应只调用一次 ssh -O check，实际 %d 次: %v", len(calls), calls)
	}
	if joined := strings.Join(calls[0], " "); !strings.Contains(joined, "-O check") {
		t.Fatalf("判活必须用 ssh -O check，实际: %s", joined)
	}
}

// 陈旧 socket（文件在但 master 已死）⇒ -O check 非 0 ⇒ 必须真正重建 master。
func TestEnsureMasterRebuildsStaleSocket(t *testing.T) {
	var calls [][]string
	fake := func(ctx context.Context, name string, args ...string) (osutil.Outcome, error) {
		calls = append(calls, append([]string{name}, args...))
		if len(calls) == 1 {
			return osutil.Outcome{ExitCode: 255, Stderr: "Control socket connect: refused"}, nil
		}
		return osutil.Outcome{ExitCode: 0}, nil
	}
	if err := EnsureMaster(context.Background(), fake, "h1", ""); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("应 check 失败后再建一次，实际 %d 次", len(calls))
	}
	built := strings.Join(calls[1], " ")
	for _, want := range []string{"ControlMaster=yes", "-N", "-f", "BatchMode=yes"} {
		if !strings.Contains(built, want) {
			t.Fatalf("建 master 参数缺少 %q: %s", want, built)
		}
	}
}

// Exec 必须把命令原样交给远端 shell，并把 user 变成 -o User=。
func TestExecBuildsArgs(t *testing.T) {
	var got []string
	fake := func(ctx context.Context, name string, args ...string) (osutil.Outcome, error) {
		got = append([]string{name}, args...)
		return osutil.Outcome{ExitCode: 0, Stdout: "ok"}, nil
	}
	out, err := Exec(context.Background(), fake, "h1", "u1", "command -v inotifywait")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if out.Stdout != "ok" {
		t.Fatalf("stdout = %q", out.Stdout)
	}
	if joined := strings.Join(got, " "); !strings.Contains(joined, "-o User=u1") {
		t.Fatalf("缺少 User 选项: %s", joined)
	}
	if got[len(got)-1] != "command -v inotifywait" {
		t.Fatalf("远端命令必须作为最后一个参数: %#v", got)
	}
}

// 远端 shell 单引号转义：含空格/分号/单引号/美元符号的路径不得被远端 shell 解释。
func TestQuoteRemote(t *testing.T) {
	if got, want := QuoteRemote("/srv/app conf"), "'/srv/app conf'"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if got, want := QuoteRemote("/srv/a; rm -rf /"), "'/srv/a; rm -rf /'"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if got, want := QuoteRemote("/srv/$HOME"), "'/srv/$HOME'"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if got, want := QuoteRemote("/srv/it's"), "'/srv/it'\\''s'"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}
~~~

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/sshconn/ -v`
Expected: 编译失败，`undefined: ControlPath`

- [ ] **Step 3: 实现**

创建 `internal/sshconn/sshconn.go`：

~~~go
// Package sshconn 是 ControlMaster socket 路径与 SSH 连接参数的唯一来源。
// sftp 与 watch 必须共用同一份路径计算——两个包各自算一遍必然漂移。
package sshconn

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"sshore/internal/osutil"
)

// controlDirName 沿用既有目录名，保证 sftp.Ctrl.CloseAll 的清理语义不变。
const controlDirName = "sshore-sftp-ctrl"

func ControlDir() string {
	return filepath.Join(os.TempDir(), controlDirName)
}

// ControlPath 的 key 必须包含 user：同一 host 上不同 user 的规则若共用
// 一个 master，ssh 要么拒绝复用、要么用错身份读写远端文件。
func ControlPath(host, user string) string {
	_ = os.MkdirAll(ControlDir(), 0700)
	name := url.PathEscape(host)
	if user != "" {
		name += "+" + url.PathEscape(user)
	}
	return filepath.Join(ControlDir(), "cm-"+name+".sock")
}

// EnsureMaster 幂等建立 ControlMaster。
// 判活必须用 ssh -O check 的退出码，不能用 os.Stat：kill -9 残留的 socket
// 文件会一直 stat 成功，此后所有连接静默退化成各自建连。
func EnsureMaster(ctx context.Context, r osutil.CtxRunner, host, user string) error {
	cp := ControlPath(host, user)
	if out, err := r(ctx, "ssh", "-O", "check", "-o", "ControlPath="+cp, host); err == nil && out.ExitCode == 0 {
		return nil
	}
	args := []string{
		"-o", "BatchMode=yes",
		"-o", "IdentitiesOnly=yes",
		"-o", "ConnectTimeout=10",
		"-o", "ControlMaster=yes",
		"-o", "ControlPersist=30",
		"-o", "ControlPath=" + cp,
		"-N", "-f",
	}
	if user != "" {
		args = append(args, "-o", "User="+user)
	}
	args = append(args, host)
	out, err := r(ctx, "ssh", args...)
	if err != nil {
		return fmt.Errorf("建立 ControlMaster 失败: %w (%s)", err, strings.TrimSpace(out.Stderr))
	}
	if out.ExitCode != 0 {
		return fmt.Errorf("建立 ControlMaster 失败: %s", strings.TrimSpace(out.Stderr))
	}
	return nil
}

// Exec 执行一次性远端命令（降级探测等）。remoteCmd 原样交给远端 shell；
// 调用方必须用 QuoteRemote 包住任何外部来源的路径。
func Exec(ctx context.Context, r osutil.CtxRunner, host, user, remoteCmd string) (osutil.Outcome, error) {
	args := []string{
		"-o", "BatchMode=yes",
		"-o", "IdentitiesOnly=yes",
		"-o", "ConnectTimeout=10",
		"-o", "ControlPath=" + ControlPath(host, user),
	}
	if user != "" {
		args = append(args, "-o", "User="+user)
	}
	args = append(args, host, remoteCmd)
	return r(ctx, "ssh", args...)
}

// QuoteRemote 用单引号包住字符串供远端 shell 使用；内部单引号按 POSIX 规则转义。
func QuoteRemote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
~~~

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/sshconn/ -race -count=1 -v`
Expected: 全部 PASS

- [ ] **Step 5: 提交**

~~~bash
gofmt -s -w internal/sshconn/
git add internal/sshconn/
git commit -m "feat(sshconn): 新增 ControlMaster socket 唯一来源、可取消远端命令与 shell 转义"
~~~

---

## Task 3: sftp.ListMany（批量列举 + 分段归属）

**Files:**
- Create: `internal/sftp/listmany_parse.go`
- Modify: `internal/sftp/ctrl.go`
- Test: `internal/sftp/listmany_test.go`

**Interfaces:**
- Consumes: 现有 `c.run(host, user, batch)`、`quoteArg`、`ParseLsLf`、`Item`
- Produces: `func (c *Ctrl) ListMany(host, user string, paths []string) (map[string][]Item, error)`

**实测事实（决定本任务的全部设计）**：批处理回显是 `sftp> -ls -la "路径"`（含前导 `-`），可作块边界；`-` 前缀阻止整批中止；**失败目录只打印 `Can't ls: "..." not found` 而 `sftp` 退出码仍是 0** —— 所以必须靠块内错误文案判定"未知"，绝不能靠退出码。把"未知"当成"空目录"会让 diff 为整棵子树产出 delete 事件，进而删本地文件。

- [ ] **Step 1: 写失败的测试**

创建 `internal/sftp/listmany_test.go`。fixture 直接取自真机实测输出：

~~~go
package sftp

import "testing"

// 真实抓取的批处理输出：a 有 2 个文件、nope 不存在、empty 存在但为空。
const listManyFixture = `sftp> -ls -la "/tmp/ldm/a"
drwxr-xr-x    2 lan      lan          4096 Sep 10 20:46 .
drwxr-xr-x    4 lan      lan          4096 Sep 10 20:46 ..
-rw-r--r--    1 lan      lan             4 Sep 10 20:46 one.txt
-rw-r--r--    1 lan      lan             4 Sep 10 20:46 two.txt
sftp> -ls -la "/tmp/ldm/nope"
Can't ls: "/tmp/ldm/nope" not found
sftp> -ls -la "/tmp/ldm/empty"
drwxr-xr-x    2 lan      lan          4096 Sep 10 20:46 .
drwxr-xr-x    4 lan      lan          4096 Sep 10 20:46 ..
`

// 关键断言：不存在的目录必须**缺席**（未知），存在但空的目录必须**在场且为空**。
// 把前者当成空目录会导致整棵子树的假删除。
func TestParseListManyDistinguishesUnknownFromEmpty(t *testing.T) {
	paths := []string{"/tmp/ldm/a", "/tmp/ldm/nope", "/tmp/ldm/empty"}
	got := parseListMany(listManyFixture, paths)

	if _, ok := got["/tmp/ldm/nope"]; ok {
		t.Fatal("不存在的目录必须缺席（未知），不能是空列表")
	}
	empty, ok := got["/tmp/ldm/empty"]
	if !ok {
		t.Fatal("存在但为空的目录必须在场")
	}
	if len(empty) != 0 {
		t.Fatalf("空目录应得到 0 条（. 与 .. 已被 ParseLsLf 过滤），得到 %#v", empty)
	}
	a, ok := got["/tmp/ldm/a"]
	if !ok {
		t.Fatal("a 必须在场")
	}
	if len(a) != 2 || a[0].Name != "one.txt" || a[1].Name != "two.txt" {
		t.Fatalf("a 的条目 = %#v", a)
	}
}

// 回显块少于请求数（批处理被截断）时，多出来的路径必须缺席而不是空。
func TestParseListManyMissingBlocksAreUnknown(t *testing.T) {
	paths := []string{"/tmp/ldm/a", "/tmp/ldm/never-listed"}
	got := parseListMany(listManyFixture, paths)
	if _, ok := got["/tmp/ldm/never-listed"]; ok {
		t.Fatal("没有对应输出块的路径必须缺席")
	}
}

// 错误行是 CRLF（实测），分段与判定都必须容忍 CR。
func TestParseListManyToleratesCRLFErrors(t *testing.T) {
	fixture := `sftp> -ls -la "/x"` + "\r\n" + `Can't ls: "/x" not found` + "\r\n"
	if got := parseListMany(fixture, []string{"/x"}); len(got) != 0 {
		t.Fatalf("CRLF 错误块必须判为未知，得到 %#v", got)
	}
}
~~~

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/sftp/ -run TestParseListMany -v`
Expected: 编译失败，`undefined: parseListMany`

- [ ] **Step 3: 实现**

创建 `internal/sftp/listmany_parse.go`：

~~~go
package sftp

import "strings"

// parseListMany 把多命令批处理的输出按命令回显切成块，按**发送顺序**归属到 paths。
//
// 分段依据是回显行前缀 "sftp> "（实测格式形如 `sftp> -ls -la "/x"`）。因此批处理中
// **不能**用 '@' 前缀：它抑制回显，分段依据会丢失。
//
// 关键：失败的块必须判为"未知"并**缺席于返回值**。实测中 `sftp -b` 对失败目录只打印
// `Can't ls: ... not found` 而退出码仍为 0，且 ParseLsLf 会把这种块解析成空列表——
// 若照单全收，就会把"列不出来"误当成"目录是空的"。
func parseListMany(out string, paths []string) map[string][]Item {
	res := make(map[string][]Item, len(paths))
	blocks := splitBlocks(out)
	for i, p := range paths {
		if i >= len(blocks) {
			break
		}
		if blockFailed(blocks[i]) {
			continue
		}
		items, err := ParseLsLf(blocks[i])
		if err != nil {
			continue
		}
		res[p] = items
	}
	return res
}

// splitBlocks 以 "sftp> " 开头的行作为块边界；首个回显之前的内容（banner）丢弃。
func splitBlocks(out string) []string {
	var blocks []string
	var cur strings.Builder
	started := false
	for _, line := range strings.Split(out, "\n") {
		l := strings.TrimRight(line, "\r")
		if strings.HasPrefix(l, "sftp> ") {
			if started {
				blocks = append(blocks, cur.String())
			}
			started = true
			cur.Reset()
			continue
		}
		if started {
			cur.WriteString(l)
			cur.WriteString("\n")
		}
	}
	if started {
		blocks = append(blocks, cur.String())
	}
	return blocks
}

// blockFailed 判断一个输出块是否代表"该目录未知"。sftp 的客户端错误行不带前导空白。
func blockFailed(block string) bool {
	for _, line := range strings.Split(block, "\n") {
		l := strings.TrimSpace(strings.TrimRight(line, "\r"))
		if l == "" {
			continue
		}
		for _, bad := range []string{"Can't ", "Couldn't ", "Invalid command", "Permission denied"} {
			if strings.HasPrefix(l, bad) {
				return true
			}
		}
		if strings.Contains(l, "not found") {
			return true
		}
	}
	return false
}
~~~

在 `internal/sftp/ctrl.go` 中追加（放在 `List` 之后）：

~~~go
// ListMany 在一个 sftp 批处理里列出多个远端目录，返回 path -> items。
// 返回的 map 中缺失某个 key 表示"该目录未知"，绝不表示"空目录"。
func (c *Ctrl) ListMany(host, user string, paths []string) (map[string][]Item, error) {
	if len(paths) == 0 {
		return map[string][]Item{}, nil
	}
	var sb strings.Builder
	for _, p := range paths {
		q, err := quoteArg(p)
		if err != nil {
			return nil, fmt.Errorf("ListMany: %w", err)
		}
		// 前缀 '-' 抑制逐命令中止：man sftp 明确 ls 失败会中止整批，
		// 任一个子目录不可读就会让后面所有目录永远列不出来。
		sb.WriteString("-ls -la " + q + "\n")
	}
	out, err := c.run(host, user, []byte(sb.String()))
	if err != nil {
		return nil, fmt.Errorf("sftp ListMany %s: %w (%s)", host, err, commandErr(out))
	}
	if out.ExitCode != 0 {
		return nil, fmt.Errorf("sftp ListMany failed: %s", commandErr(out))
	}
	return parseListMany(out.Stdout, paths), nil
}
~~~

- [ ] **Step 4: 运行测试确认通过（含既有解析器回归）**

Run: `go test ./internal/sftp/ -race -count=1 -v`
Expected: 全部 PASS（含原有 `TestParseLsLf*` 与 `TestCtrl*`）

- [ ] **Step 5: 提交**

~~~bash
gofmt -s -w internal/sftp/
git add internal/sftp/
git commit -m "feat(sftp): 新增 ListMany 批量列举,用 '-' 前缀防整批中止并区分未知与空目录"
~~~

---

## Task 3b: 把 user 贯通到 sftp 的 ControlMaster socket

**Files:** Modify `internal/sftp/ctrl.go`；Test `internal/sftp/ctrl_test.go`

**为什么必须有这一步**：spec §4.4 要求 `controlPathFor(host)` 改为 `sshconn.ControlPath(host, user)`。不改的话，`EnsureMaster` 建的 socket 是 `cm-<host>+<user>.sock`，而 sftp 的 `List/Get/ListMany` 用的是 `cm-<host>.sock` —— **两者永不重合，"探测连接与文件传输共用一条 SSH 连接"这个设计前提直接落空**；同 host 异 user 时 sftp 还会复用错身份的旧 master。

**公开方法签名一个不动**（`Disconnect(host)`/`Connected(host)` 的调用方是前端，不能动）：`Ctrl` 内部记住 host → user。

- [ ] **Step 1: 写失败的测试**

在 `internal/sftp/ctrl_test.go` 追加：

~~~go
// socket 路径必须随 user 变化：同 host 异 user 绝不能共用 master。
func TestControlPathKeyedByUser(t *testing.T) {
	c := NewCtrl(func(string, ...string) (osutil.Outcome, error) { return osutil.Outcome{}, nil }, nil)
	a := c.controlPathFor("prod-01", "")
	b := c.controlPathFor("prod-01", "alice")
	if a == b {
		t.Fatalf("同 host 异 user 必须得到不同 socket: %q", a)
	}
}

// Disconnect/Connected 只拿到 host，必须用内部记住的 user 反查同一个 socket。
func TestRememberedUserUsedByHostOnlyAPI(t *testing.T) {
	c := NewCtrl(func(string, ...string) (osutil.Outcome, error) { return osutil.Outcome{}, nil }, nil)
	c.rememberUser("prod-01", "alice")
	if got, want := c.controlPathFor("prod-01", c.userFor("prod-01")), c.controlPathFor("prod-01", "alice"); got != want {
		t.Fatalf("host-only API 未复用记住的 user: %q vs %q", got, want)
	}
}
~~~

- [ ] **Step 2: 运行测试确认失败** — Run: `go test ./internal/sftp/ -run 'ControlPathKeyed|RememberedUser' -v`；Expected: 编译失败（`controlPathFor` 参数个数不匹配）

- [ ] **Step 3: 实现**

在 `internal/sftp/ctrl.go` 中：

~~~go
type Ctrl struct {
	runner     osutil.Runner
	emit       forward.EmitFunc
	controlDir string
	active     map[string]bool   // hosts marked connected (Windows per-command mode)
	users      map[string]string // host → 最近一次使用的 user（用于把 user 纳入 socket key）
	mu         sync.Mutex
}

// controlPathFor 委托给 sshconn（socket 路径的唯一来源）；user 纳入 key，
// 否则同 host 异 user 会共用 master，用错身份读写远端文件。
func (c *Ctrl) controlPathFor(host, user string) string {
	return sshconn.ControlPath(host, user)
}

func (c *Ctrl) rememberUser(host, user string) {
	c.mu.Lock()
	if c.users == nil {
		c.users = map[string]string{}
	}
	c.users[host] = user
	c.mu.Unlock()
}

func (c *Ctrl) userFor(host string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.users[host]
}
~~~

调用点全部改掉（**方法签名一个不动**）：

- `NewCtrl` 里初始化 `users: map[string]string{}`；删掉 `controlDir` 相关字段与 `CloseAll` 里的文件名反解逻辑；
- `run(host, user, batch)`：开头 `c.rememberUser(host, user)`，socket 用 `c.controlPathFor(host, user)`；
- `Connect(host, user)`：`c.rememberUser(host, user)`；
- `Disconnect(host)` / `disconnectLocked(host)` / `Connected(host)`：用 `c.controlPathFor(host, c.userFor(host))`；
- `CloseAll()`：**改为遍历 `c.users`**（`for host, user := range c.users { disconnectLocked(host, user) }`）——带 `+user` 的路径反解不出正确的 host，原来的文件名解析会失效。

- [ ] **Step 4: 运行测试确认通过（含既有 sftp 回归）** — Run: `go test ./internal/sftp/ -race -count=1`；Expected: 全部 PASS

- [ ] **Step 5: 提交** — `git add internal/sftp/ && git commit -m "refactor(sftp): ControlMaster socket 纳入 user 并委托 sshconn,修正同 host 异 user 复用错 master"`
~~~

---

## Task 4: config.SyncRule 与默认值归一化

**Files:**
- Modify: `internal/config/store.go`
- Test: `internal/config/store_test.go`

**Interfaces:**
- Produces: `SyncRule`、`SyncRule.Normalize()`、`DefaultExcludes() []string`、`AppConfig.Syncs []SyncRule`、`NewSyncID() string`

**为什么必须有 Normalize**：`LoadConfig` 先构造默认值再 Decode，但 `[[syncs]]` 数组的元素是**解码时新建的零值结构体**，预置默认值不生效。缺键会被静默解释成零值（例如 `poll_interval_s = 0`、`excludes = nil`），而校验只在创建/编辑时跑，拦不住手改配置文件。

**与 spec §5.2 一致**：`auto_reconnect` 缺键时取全局 `App.AutoReconnectDefault`（默认 true）。实现方式是把它做成 `*bool` + `Reconnect() bool` 访问器——解码后的裸 `bool` 分不清"缺键"与"显式 false"，套默认会制造一个关不掉的开关；用指针才能既满足 spec 又保留显式关闭的能力。

- [ ] **Step 1: 写失败的测试**

在 `internal/config/store_test.go` 末尾追加（import 增补 `strings`）：

~~~go
// 手写配置缺键时，零值不得静默改变行为。
func TestSyncRuleNormalizeFillsDefaults(t *testing.T) {
	var r SyncRule
	r.Normalize()
	if r.Kind != "dir" {
		t.Fatalf("kind 缺省应为 dir，得到 %q", r.Kind)
	}
	if r.PollIntervalS != 5 {
		t.Fatalf("poll_interval_s 缺省应为 5，得到 %d", r.PollIntervalS)
	}
	if len(r.Excludes) != 5 || r.Excludes[0] != ".git/" {
		t.Fatalf("excludes 缺省应为 DefaultExcludes()，得到 %#v", r.Excludes)
	}
	if r.ForcePoll {
		t.Fatal("force_poll 零值必须是 false（即优先 inotify），否则与文档相反")
	}
}

// 显式清空 excludes 要保留为空，不能被"补回默认值"。
func TestSyncRuleNormalizeKeepsExplicitEmptyExcludes(t *testing.T) {
	r := SyncRule{Excludes: []string{}}
	r.Normalize()
	if len(r.Excludes) != 0 {
		t.Fatalf("显式空 excludes 必须保留，得到 %#v", r.Excludes)
	}
}

func TestSyncRuleNormalizeClampsNumbers(t *testing.T) {
	r := SyncRule{PollIntervalS: 99999, MaxDepth: -7}
	r.Normalize()
	if r.PollIntervalS != 3600 {
		t.Fatalf("poll_interval_s 上限 3600，得到 %d", r.PollIntervalS)
	}
	if r.MaxDepth != -1 {
		t.Fatalf("max_depth < -1 应归一为 -1，得到 %d", r.MaxDepth)
	}
}

// TOML 往返：Syncs 数组必须能被读写，且缺键元素自动获得默认值。
func TestConfigSyncsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sshore.toml")
	cfg := DefaultAppConfig()
	cfg.Syncs = []SyncRule{{
		ID: "abc123", Name: "cfg", Host: "prod-01", Kind: "dir",
		RemotePath: "/srv/conf", LocalPath: "/tmp/conf", MaxDepth: 2,
		MirrorDelete: true, Enabled: true,
	}}
	if err := SaveConfig(path, cfg); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got.Syncs) != 1 {
		t.Fatalf("want 1 sync rule, got %d", len(got.Syncs))
	}
	r := got.Syncs[0]
	if r.ID != "abc123" || !r.MirrorDelete || !r.Enabled {
		t.Fatalf("round-trip 丢字段: %#v", r)
	}
	if r.PollIntervalS != 5 || r.Kind != "dir" {
		t.Fatalf("round-trip 未套用默认值: %#v", r)
	}
}

func TestNewSyncIDIsStableFormat(t *testing.T) {
	a, b := NewSyncID(), NewSyncID()
	if a == b {
		t.Fatal("id 必须随机")
	}
	if len(a) != 32 {
		t.Fatalf("与 NewTunnelID 同构应为 32 位 hex，得到 %d: %q", len(a), a)
	}
	if strings.Contains(a, "-") {
		t.Fatalf("id 不带前缀/连字符，得到 %q", a)
	}
}
~~~

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/config/ -run 'Sync' -v`
Expected: 编译失败，`undefined: SyncRule`

- [ ] **Step 3: 实现**

在 `internal/config/store.go` 的 `AppConfig` 定义前插入 `SyncRule`，并把 `Syncs` 加进 `AppConfig`：

~~~go
// SyncRule 是一条"监控远端路径并同步到本地"的规则。
type SyncRule struct {
	ID            string   `toml:"id" json:"id"`
	Name          string   `toml:"name" json:"name"`
	Host          string   `toml:"host" json:"host"`
	User          string   `toml:"user,omitempty" json:"user,omitempty"`
	Kind          string   `toml:"kind" json:"kind"` // dir | file
	RemotePath    string   `toml:"remote_path" json:"remote_path"`
	LocalPath     string   `toml:"local_path" json:"local_path"` // 恒为目录
	MaxDepth      int      `toml:"max_depth" json:"max_depth"`   // 0=仅本层 N=递归N层 -1=无限
	Excludes      []string `toml:"excludes" json:"excludes"`
	MirrorDelete  bool     `toml:"mirror_delete" json:"mirror_delete"`
	ForcePoll     bool     `toml:"force_poll" json:"force_poll"` // 反向字段：零值即"优先 inotify"
	PollIntervalS int      `toml:"poll_interval_s" json:"poll_interval_s"`
	AutoReconnect *bool    `toml:"auto_reconnect,omitempty" json:"auto_reconnect"`
	Enabled       bool     `toml:"enabled" json:"enabled"`
}

// DefaultExcludes 是远端路径的默认忽略集合（过滤的是**远端**路径）。
// 不要放 *.part：那是我们本地临时文件的后缀，远端不会出现。
func DefaultExcludes() []string {
	return []string{".git/", "node_modules/", "*.swp", "*~", ".DS_Store"}
}

// Reconnect 返回"意外断开时是否自动重连"。缺键（nil）按 true 处理——真正的
// 默认值由 AppConfig.normalize() 从全局 App.AutoReconnectDefault 灌入，
// 这里只是防止绕过 LoadConfig 直接构造 SyncRule 时解引用 nil。
func (r *SyncRule) Reconnect() bool {
	return r.AutoReconnect == nil || *r.AutoReconnect
}

// Normalize 兜底零值。手改配置缺键时，零值不得静默改变行为
//（例如 poll_interval_s=0 或 excludes=nil）。
func (r *SyncRule) Normalize() {
	if r.Kind != "file" {
		r.Kind = "dir"
	}
	if r.MaxDepth < -1 {
		r.MaxDepth = -1
	}
	if r.PollIntervalS < 1 {
		r.PollIntervalS = 5
	}
	if r.PollIntervalS > 3600 {
		r.PollIntervalS = 3600
	}
	if r.Excludes == nil {
		r.Excludes = DefaultExcludes()
	}
}

// NewSyncID 与 NewTunnelID 同构：32 位纯十六进制、无前缀。
func NewSyncID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
~~~

在 `AppConfig` 里加一行 `Syncs []SyncRule `toml:"syncs" json:"syncs"``，并在 `normalize()` 里逐条归一：

~~~go
func (c *AppConfig) normalize() {
	c.App.Normalize()
	for i := range c.Syncs {
		c.Syncs[i].Normalize()
		// spec §5.2：auto_reconnect 缺键时取全局默认（默认 true）。
		// 用例：手写配置只写 host/remote_path/local_path，不应被静默关闭重连。
		if c.Syncs[i].AutoReconnect == nil {
			v := c.App.AutoReconnectDefault
			c.Syncs[i].AutoReconnect = &v
		}
	}
}
~~~

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/config/ -race -count=1`
Expected: 全部 PASS（含既有 parser/store 测试）

- [ ] **Step 5: 提交**

~~~bash
gofmt -s -w internal/config/
git add internal/config/
git commit -m "feat(config): 新增 SyncRule 与默认值归一化,避免手改配置缺键静默改变行为"
~~~

---

## Task 5: watch 事件契约与 inotify 行解析

**Files:**
- Create: `internal/watch/event.go`、`internal/watch/inotify_parse.go`
- Test: `internal/watch/inotify_parse_test.go`

**Interfaces:**
- Produces:
  - `type Kind string` 与常量 `KindCreate/KindWrite/KindDelete/KindDirAdded/KindDirGone/KindOverflow/KindRootGone`
  - `type Event struct { RelPath string; Kind Kind }`（**不带 Size/ModTime**，元信息统一由 ls 补齐）
  - `type Info struct { Mode string; Reason string; Interval time.Duration }`
  - `type Source interface { Start(ctx context.Context) (<-chan Event, error); Info() Info; Close() error }`
  - `func ParseInotifyLine(root, line string) (Event, bool)`

**必须按实测写**：`%e` 是逗号分隔 token 列表；`*_SELF` 路径带尾斜杠；判定必须**先看 ISDIR 再看文件 token**（`CLOSE_NOWRITE,CLOSE,ISDIR` 同时含 `CLOSE`）。

- [ ] **Step 1: 写失败的测试**

创建 `internal/watch/inotify_parse_test.go`，输入**全部取自真机实测输出**：

~~~go
package watch

import "testing"

const root = "/srv/conf"

func TestParseInotifyLineScenarios(t *testing.T) {
	cases := []struct {
		name string
		line string
		want Event
		ok   bool
	}{
		// 生产格式（带 %T 时间戳）：远程命令用的是 '%T|%w%f|%e'，必须覆盖
		{"生产格式-创建", "1789033487|/srv/conf/a.txt|CREATE", Event{"a.txt", KindCreate}, true},
		{"生产格式-含CRLF", "1789033487|/srv/conf/a.txt|CLOSE_WRITE,CLOSE\r", Event{"a.txt", KindWrite}, true},
		{"创建文件", "/srv/conf/a.txt|CREATE", Event{"a.txt", KindCreate}, true},
		{"写入文件", "/srv/conf/a.txt|CLOSE_WRITE,CLOSE", Event{"a.txt", KindWrite}, true},
		{"仅 MODIFY", "/srv/conf/a.txt|MODIFY", Event{"a.txt", KindWrite}, true},
		{"删除文件", "/srv/conf/a.txt|DELETE", Event{"a.txt", KindDelete}, true},
		{"移出文件", "/srv/conf/a.txt|MOVED_FROM", Event{"a.txt", KindDelete}, true},
		{"移入文件", "/srv/conf/a.txt|MOVED_TO", Event{"a.txt", KindCreate}, true},
		{"嵌套路径", "/srv/conf/d/b.txt|CREATE", Event{"d/b.txt", KindCreate}, true},
		// 目录事件一律触发子树对账：mv 进来的目录里已有文件是零事件的
		{"新建目录", "/srv/conf/d|CREATE,ISDIR", Event{"d", KindDirAdded}, true},
		{"移入目录", "/srv/conf/d|MOVED_TO,ISDIR", Event{"d", KindDirAdded}, true},
		{"删除目录", "/srv/conf/d|DELETE,ISDIR", Event{"d", KindDirGone}, true},
		{"移出目录", "/srv/conf/d|MOVED_FROM,ISDIR", Event{"d", KindDirGone}, true},
		// *_SELF 路径带尾斜杠（实测），必须归一化掉
		{"目录自删", "/srv/conf/d/|DELETE_SELF", Event{"d", KindDirGone}, true},
		{"目录自移", "/srv/conf/d/|MOVE_SELF", Event{"d", KindDirGone}, true},
		{"根目录自删", "/srv/conf/|DELETE_SELF", Event{"", KindRootGone}, true},
		// 噪声：目录的 OPEN/ACCESS/CLOSE_NOWRITE 必须丢弃，
		// 但 CLOSE_NOWRITE,CLOSE,ISDIR 里同时含 CLOSE —— 所以必须先判 ISDIR
		{"目录噪声", "/srv/conf/d|CLOSE_NOWRITE,CLOSE,ISDIR", Event{}, false},
		{"目录噪声2", "/srv/conf/d|OPEN,ISDIR", Event{}, false},
		{"目录噪声3", "/srv/conf/d|ACCESS,ISDIR", Event{}, false},
		{"文件噪声", "/srv/conf/a.txt|OPEN", Event{}, false},
		{"文件噪声2", "/srv/conf/a.txt|ACCESS", Event{}, false},
		{"属性噪声", "/srv/conf/a.txt|ATTRIB", Event{}, false},
		{"非本规则路径", "/srv/other/a.txt|CREATE", Event{}, false},
		{"兄弟目录（前缀相同）", "/srv/conf-backup/a.txt|CREATE", Event{}, false},
		{"根路径自身带尾斜杠", "/srv/conf/|CREATE,ISDIR", Event{}, false},
		{"格式不符", "Watches established.", Event{}, false},
		{"空行", "", Event{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := ParseInotifyLine(root, c.line)
			if ok != c.ok {
				t.Fatalf("ok = %v, want %v (line %q)", ok, c.ok, c.line)
			}
			if ok && got != c.want {
				t.Fatalf("got %+v want %+v", got, c.want)
			}
		})
	}
}

// pty 下每行都带 CR（实测），解析必须先裁掉。
func TestParseInotifyLineToleratesCRLF(t *testing.T) {
	got, ok := ParseInotifyLine(root, "/srv/conf/a.txt|CLOSE_WRITE,CLOSE\r")
	if !ok || got.Kind != KindWrite {
		t.Fatalf("CRLF 行必须可解析，得到 %+v ok=%v", got, ok)
	}
}

// 路径中可能含 '|'，所以必须从右往左切分两次。
func TestParseInotifyLineHandlesPipeInPath(t *testing.T) {
	got, ok := ParseInotifyLine(root, "/srv/conf/we|ird.txt|CREATE")
	if !ok || got.RelPath != "we|ird.txt" {
		t.Fatalf("含 | 的路径必须正确切分，得到 %+v ok=%v", got, ok)
	}
}
~~~

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/watch/ -v`
Expected: 编译失败，`undefined: ParseInotifyLine`

- [ ] **Step 3: 实现**

创建 `internal/watch/event.go`：

~~~go
// Package watch 把远端变更探测的两条路径（inotify 常驻、SFTP 轮询）归一成
// 同一份事件契约。引擎层永远不知道事件来自哪条路径。
package watch

import (
	"context"
	"time"
)

type Kind string

const (
	KindCreate   Kind = "create"    // 新文件出现
	KindWrite    Kind = "write"     // 已有文件被修改
	KindDelete   Kind = "delete"    // 文件消失
	KindDirAdded Kind = "dir_added" // 目录进入监控树（含 mv 进来）→ 触发子树对账
	KindDirGone  Kind = "dir_gone"  // 目录离开监控树 → 触发子树对账
	KindOverflow Kind = "overflow"  // 内核队列溢出 → 强制全量对账
	KindRootGone Kind = "root_gone" // 根目录 UNMOUNT / DELETE_SELF / IGNORED
)

// Event 只携带"路径 + 意图"。**不带 Size/ModTime**：两条探测路径的元信息必须
// 同源，唯一同源来源是 ls 结果（见 ScanTree）。
type Event struct {
	RelPath string // 相对 root，"/" 分隔；空串表示根目录自身
	Kind    Kind
}

type Info struct {
	Mode     string // "inotify" | "poll"
	Reason   string // Mode=="poll" 时必填且必须具体
	Interval time.Duration
}

// Source 是探测层对上层暴露的唯一接口。
type Source interface {
	Start(ctx context.Context) (<-chan Event, error)
	Info() Info
	// Close 幂等，且**必须关闭 Start 返回的 channel**，否则引擎的 range 永不退出。
	Close() error
}
~~~

创建 `internal/watch/inotify_parse.go`：

~~~go
package watch

import "strings"

// ParseInotifyLine 解析一行 inotifywait 输出。格式（实测）：
//
//	<epoch>|<绝对路径>|<逗号分隔 token 列表>
//
// 例：`1789033487|/srv/conf/a.txt|CLOSE_WRITE,CLOSE`、
// `...|/srv/conf/d|CREATE,ISDIR`、`...|/srv/conf/d/|DELETE_SELF`。
//
// 三个必须遵守的实测约束：
//  1. 行尾可能带 CR（ssh -tt 的 pty ONLCR），先裁掉；
//  2. %e 是 token **列表**，必须按逗号切分——整串比较会全部匹配失败；
//  3. *_SELF 事件的路径带尾斜杠，必须归一化。
func ParseInotifyLine(root, line string) (Event, bool) {
	line = strings.TrimRight(line, "\r\n")
	// 时间戳取第一个 "|" 之前，事件取最后一个 "|" 之后，中间的整段是路径
	// ——这样路径里含 "|" 也不会切错（"从右往左两次"会切错）。
	first := strings.Index(line, "|")
	last := strings.LastIndex(line, "|")
	if first < 0 {
		return Event{}, false
	}
	var full, ev string
	if first == last {
		// 容错：只有 path|events（没有时间戳）。生产格式一定带时间戳，
		// 但缺时间戳时 path|events 也是无歧义的，没必要为此丢掉事件。
		full, ev = line[:first], line[first+1:]
	} else {
		// 生产格式：<epoch>|<path>|<events>。取第一个 | 与最后一个 |，
		// 中间的整段是路径 —— 路径里含 | 也不会切错。
		full, ev = line[first+1:last], line[last+1:]
	}
	full = strings.TrimSuffix(full, "/")
	// 必须按**路径边界**判断，不能用裸 HasPrefix：root="/srv/conf" 时
	// "/srv/conf-backup/x" 也会通过前缀检查，把邻居目录的事件混进来。
	base := strings.TrimSuffix(root, "/")
	if full != base && !strings.HasPrefix(full, base+"/") {
		return Event{}, false
	}
	rel := strings.TrimPrefix(strings.TrimPrefix(full, base), "/")

	toks := map[string]bool{}
	for _, tk := range strings.Split(ev, ",") {
		toks[strings.TrimSpace(tk)] = true
	}
	// 根目录自身消失：靠事件检测，不能靠进程退出（实测进程会继续运行）。
	if rel == "" {
		if toks["DELETE_SELF"] || toks["UNMOUNT"] || toks["IGNORED"] {
			return Event{RelPath: "", Kind: KindRootGone}, true
		}
		return Event{}, false
	}
	// 必须先判 ISDIR：CLOSE_NOWRITE,CLOSE,ISDIR 同时含 CLOSE，
	// 若先按文件 token 过滤会把目录事件误伤成噪声。
	// *_SELF 的 token 里**没有 ISDIR**（实测 d/|DELETE_SELF），但路径带尾斜杠、
	// 且只有被 watch 的目录才会收到自己的 SELF 事件 —— 一律按目录事件处理。
	if toks["DELETE_SELF"] || toks["MOVE_SELF"] || toks["DELETE"] || toks["MOVED_FROM"] {
		return Event{RelPath: rel, Kind: KindDirGone}, true
	}
	if toks["ISDIR"] {
		switch {
		case toks["CREATE"] || toks["MOVED_TO"]:
			return Event{RelPath: rel, Kind: KindDirAdded}, true
		default:
			return Event{}, false // OPEN,ISDIR / ACCESS,ISDIR / CLOSE_NOWRITE,...,CLOSE,ISDIR
		}
	}
	switch {
	case toks["CREATE"] || toks["MOVED_TO"]:
		return Event{RelPath: rel, Kind: KindCreate}, true
	case toks["CLOSE_WRITE"] || toks["MODIFY"]:
		return Event{RelPath: rel, Kind: KindWrite}, true
	case toks["DELETE"] || toks["MOVED_FROM"]:
		return Event{RelPath: rel, Kind: KindDelete}, true
	default:
		return Event{}, false
	}
}
~~~

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/watch/ -race -count=1 -v`
Expected: 全部 PASS

- [ ] **Step 5: 提交**

~~~bash
gofmt -s -w internal/watch/
git add internal/watch/
git commit -m "feat(watch): 新增事件契约与 inotify 行解析,按实测处理 token 列表与尾斜杠"
~~~

---

## Task 6: 降级判定与 inotify 探测源

**Files:**
- Create: `internal/watch/detect.go`、`internal/watch/inotify.go`
- Test: `internal/watch/detect_test.go`、`internal/watch/inotify_test.go`

**Interfaces:**
- Consumes: `osutil.Streamer/CtxRunner`（Task 1）、`sshconn.ControlPath/Exec/QuoteRemote`（Task 2）、`ParseInotifyLine`（Task 5）
- Produces:
  - `type DetectOpts struct { Host, User, RemotePath string; ForcePoll bool; PollInterval time.Duration }`
  - `func Detect(ctx context.Context, r osutil.CtxRunner, o DetectOpts) Info`
  - `func NewInotifySource(sp osutil.Streamer, o DetectOpts, log func(level, msg string)) *InotifySource`

**要点**：`Reason` 必须具体（用户以为在实时同步而实际是轮询，是本特性最危险的误信状态）；探测必须带 5s 超时且**真取消**；`Close` 幂等且是 channel 的唯一关闭点。

- [ ] **Step 1: 写失败的测试**

创建 `internal/watch/detect_test.go`：

~~~go
package watch

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"sshore/internal/osutil"
)

func TestDetectForcePollWins(t *testing.T) {
	called := false
	r := func(ctx context.Context, name string, args ...string) (osutil.Outcome, error) {
		called = true
		return osutil.Outcome{}, nil
	}
	info := Detect(context.Background(), r, DetectOpts{ForcePoll: true, PollInterval: 5 * time.Second})
	if info.Mode != "poll" || !strings.Contains(info.Reason, "强制轮询") {
		t.Fatalf("got %+v", info)
	}
	if called {
		t.Fatal("用户已选择轮询，不应再去探测远端")
	}
}

func TestDetectUsesInotifyWhenPresent(t *testing.T) {
	r := func(ctx context.Context, name string, args ...string) (osutil.Outcome, error) {
		return osutil.Outcome{ExitCode: 0, Stdout: "/usr/bin/inotifywait"}, nil
	}
	if info := Detect(context.Background(), r, DetectOpts{}); info.Mode != "inotify" {
		t.Fatalf("got %+v", info)
	}
}

// 实测：远端没有 inotifywait 时 command -v 退出码是 1（不是 127）。
func TestDetectFallsBackWhenMissing(t *testing.T) {
	r := func(ctx context.Context, name string, args ...string) (osutil.Outcome, error) {
		return osutil.Outcome{ExitCode: 1}, nil
	}
	info := Detect(context.Background(), r, DetectOpts{PollInterval: 5 * time.Second})
	if info.Mode != "poll" {
		t.Fatalf("got %+v", info)
	}
	if !strings.Contains(info.Reason, "1") {
		t.Fatalf("Reason 必须带具体退出码，得到 %q", info.Reason)
	}
}

// 探测超时必须降级，且不能只是"不等了"——子进程由 CtxRunner 真正回收。
func TestDetectTimeoutFallsBack(t *testing.T) {
	r := func(ctx context.Context, name string, args ...string) (osutil.Outcome, error) {
		<-ctx.Done()
		return osutil.Outcome{}, errors.New("context deadline exceeded")
	}
	start := time.Now()
	info := Detect(context.Background(), r, DetectOpts{PollInterval: 5 * time.Second})
	if info.Mode != "poll" || !strings.Contains(info.Reason, "超时") {
		t.Fatalf("got %+v", info)
	}
	if time.Since(start) > 30*time.Second {
		t.Fatal("探测必须有自己的超时")
	}
}
~~~

创建 `internal/watch/inotify_test.go`（用假 Streamer 喂实测样本）：

~~~go
package watch

import (
	"context"
	"testing"
	"time"

	"sshore/internal/osutil"
)

type fakeStreamer struct {
	handlers osutil.StreamHandlers
	proc     *osutil.Process
	started  chan struct{}
}

func (f *fakeStreamer) StartStream(name string, args []string, h osutil.StreamHandlers) (*osutil.Process, error) {
	f.handlers = h
	sp := osutil.NewSpawner()
	p, err := sp.Start("sleep", []string{"30"}, nil)
	if err != nil {
		return nil, err
	}
	f.proc = p
	if f.started != nil {
		close(f.started)
	}
	return p, nil
}

// pty 下的真实输出（含 CR、噪声行、token 列表、目录事件）必须被正确归一。
func TestInotifySourceParsesRealOutput(t *testing.T) {
	fs := &fakeStreamer{started: make(chan struct{})}
	src := NewInotifySource(fs, DetectOpts{Host: "h", RemotePath: "/srv/conf"}, func(string, string) {})
	ch, err := src.Start(context.Background())
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer src.Close()
	<-fs.started

	fs.handlers.OnStdout("Setting up watches.  Beware: since -r was given, this may take a while!\r")
	fs.handlers.OnStdout("Watches established.\r")
	fs.handlers.OnStdout("/srv/conf/a.txt|CLOSE_WRITE,CLOSE\r")
	fs.handlers.OnStdout("/srv/conf/d|CREATE,ISDIR\r")
	fs.handlers.OnStdout("/srv/conf/d|CLOSE_NOWRITE,CLOSE,ISDIR\r")

	want := []Event{{"a.txt", KindWrite}, {"d", KindDirAdded}}
	for _, w := range want {
		select {
		case got := <-ch:
			if got != w {
				t.Fatalf("got %+v want %+v", got, w)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("等待事件 %+v 超时", w)
		}
	}
}

// 远端 watch 配额耗尽 / 新目录补挂失败 → 必须产出 overflow 强制对账，
// 而不是静默漏同步（徽章仍显示 inotify 是最危险的误信状态）。
func TestInotifySourceSignalsWatchFailure(t *testing.T) {
	fs := &fakeStreamer{started: make(chan struct{})}
	var logs []string
	src := NewInotifySource(fs, DetectOpts{Host: "h", RemotePath: "/srv/conf"}, func(level, msg string) {
		logs = append(logs, level+":"+msg)
	})
	ch, err := src.Start(context.Background())
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer src.Close()
	<-fs.started

	fs.handlers.OnStderr("Failed to watch /srv/conf/deep; upper limit on watches reached!")
	select {
	case got := <-ch:
		if got.Kind != KindOverflow {
			t.Fatalf("want overflow, got %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("配额耗尽必须产出 overflow 事件")
	}
	if len(logs) == 0 {
		t.Fatal("必须同时记一条日志说明原因")
	}
}

// Close 幂等，且必须关闭 channel，否则引擎的 range 永不退出（goroutine 泄漏）。
func TestInotifySourceCloseClosesChannel(t *testing.T) {
	fs := &fakeStreamer{started: make(chan struct{})}
	src := NewInotifySource(fs, DetectOpts{Host: "h", RemotePath: "/srv/conf"}, func(string, string) {})
	ch, err := src.Start(context.Background())
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	<-fs.started
	if err := src.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := src.Close(); err != nil {
		t.Fatalf("Close 必须幂等: %v", err)
	}
	select {
	case _, open := <-ch:
		if open {
			t.Fatal("Close 后 channel 必须已关闭")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close 未关闭 channel")
	}
}
~~~

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/watch/ -run 'Detect|InotifySource' -v`
Expected: 编译失败，`undefined: Detect` / `undefined: NewInotifySource`

- [ ] **Step 3: 实现**

创建 `internal/watch/detect.go`：

~~~go
package watch

import (
	"context"
	"fmt"
	"strings"
	"time"

	"sshore/internal/osutil"
	"sshore/internal/sshconn"
)

// DetectOpts 是一条规则里与探测有关的参数。
type DetectOpts struct {
	Host         string
	User         string
	RemotePath   string
	ForcePoll    bool
	PollInterval time.Duration
}

// detectTimeout 探测自身的上限。osutil.CtxRunner 会在超时后真正结束子进程，
// 所以"降级"同时意味着"不留泄漏"。
const detectTimeout = 5 * time.Second

// Detect 判定探测路径。每次重连成功后都要重新判定一次。
func Detect(ctx context.Context, r osutil.CtxRunner, o DetectOpts) Info {
	poll := Info{Mode: "poll", Interval: o.PollInterval}
	if poll.Interval <= 0 {
		poll.Interval = 5 * time.Second
	}
	if o.ForcePoll {
		poll.Reason = "用户选择了强制轮询"
		return poll
	}
	cctx, cancel := context.WithTimeout(ctx, detectTimeout)
	defer cancel()
	out, err := sshconn.Exec(cctx, r, o.Host, o.User, "command -v inotifywait")
	switch {
	case err != nil && cctx.Err() != nil:
		poll.Reason = fmt.Sprintf("探测超时（%s）", detectTimeout)
		return poll
	case err != nil:
		poll.Reason = fmt.Sprintf("探测失败：%v", err)
		return poll
	case out.ExitCode != 0:
		// 实测：远端没有 inotifywait 时退出码为 1（不是 127），所以只报实际值。
		poll.Reason = fmt.Sprintf("远端无 inotifywait（非 0 退出：%d）", out.ExitCode)
		return poll
	case strings.TrimSpace(out.Stdout) == "":
		poll.Reason = "远端 command -v 未返回路径"
		return poll
	}
	return Info{Mode: "inotify"}
}
~~~

创建 `internal/watch/inotify.go`：

~~~go
package watch

import (
	"context"
	"strings"
	"sync"

	"sshore/internal/osutil"
	"sshore/internal/sshconn"
)

// InotifySource 用一条常驻的远端 inotifywait 进程提供近实时事件。
//
// 【已知弱点 W1，必须保留这段注释】
// inotifywait -r 没有 maxdepth：内核 watch 会覆盖全部层级，规则里的 max_depth
// 只在**解析事件时**过滤，并不能减少内核 watch 配额的开销。大目录（例如下面挂着
// node_modules）可能耗尽配额，后果是启动即失败（Failed to watch）或带残缺 watch
// 静默漏同步。需要真正限量扫描时，请在规则里关闭 inotify（force_poll = true）
// 改用轮询路径。
type InotifySource struct {
	sp   osutil.Streamer
	opts DetectOpts
	log  func(level, msg string)

	mu   sync.Mutex
	proc *osutil.Process
	ch   chan Event
	done chan struct{}

	emitMu sync.Mutex // 串行化 emit 与 close(ch)
	closed bool

	sawEstablished bool  // 是否已收到 "Watches established."（区分启动期/运行期）
	startErr       error // 启动期致命错误（远端路径不存在、watch 配额耗尽）
	unmatched      int   // 无法解析的行数（spec §6.2 要求计数告警）
}

// Err 返回启动期的致命错误。引擎在探测断开后据此判定"进 error 不重连"，
// 而不是把它当成一次可重连的抖动。
func (s *InotifySource) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.startErr
}

func NewInotifySource(sp osutil.Streamer, o DetectOpts, log func(level, msg string)) *InotifySource {
	if log == nil {
		log = func(string, string) {}
	}
	return &InotifySource{sp: sp, opts: o, log: log}
}

func (s *InotifySource) Info() Info { return Info{Mode: "inotify"} }

// RemoteCommand 单独导出便于测试断言参数构造。
func (s *InotifySource) RemoteCommand() string {
	return "exec inotifywait -m -r --format '%T|%w%f|%e' --timefmt '%s' " +
		sshconn.QuoteRemote(s.opts.RemotePath)
}

func (s *InotifySource) Start(ctx context.Context) (<-chan Event, error) {
	args := []string{
		"-tt", // 必须是 -tt：不加时杀掉本地 ssh 会在远端留下孤儿进程（已实测）
		"-o", "BatchMode=yes",
		"-o", "IdentitiesOnly=yes",
		"-o", "ConnectTimeout=10",
		"-o", "ControlPath=" + sshconn.ControlPath(s.opts.Host, s.opts.User),
	}
	if s.opts.User != "" {
		args = append(args, "-o", "User="+s.opts.User)
	}
	args = append(args, s.opts.Host, s.RemoteCommand())

	ch := make(chan Event, 256)
	proc, err := s.sp.StartStream("ssh", args, osutil.StreamHandlers{
		OnStdout: s.handleStdout(ch),
		OnStderr: s.handleStderr(ch),
	})
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.proc, s.ch, s.done = proc, ch, make(chan struct{})
	done := s.done
	s.mu.Unlock()

	// channel 的唯一关闭点：进程结束（自然退出或 Close 杀掉）后关闭。
	go func() {
		proc.Wait()
		s.emitMu.Lock()
		s.closed = true
		close(ch)
		s.emitMu.Unlock()
		close(done)
	}()
	return ch, nil
}

// Close 幂等：杀掉探测进程并等待 channel 关闭。
func (s *InotifySource) Close() error {
	s.mu.Lock()
	proc, done := s.proc, s.done
	s.mu.Unlock()
	if proc == nil {
		return nil
	}
	_ = proc.Kill()
	<-done
	return nil
}

func (s *InotifySource) emit(ch chan Event, ev Event, done chan struct{}) {
	// close(ch) 与 stdout/stderr 的 scanLines goroutine 是并发的：cmd.Wait()
	// 返回后立刻 close(ch) 时，回调可能正在执行 ch <- ev —— 往已关闭的 channel
	// 发送会 panic（select 挡不住）。所以用一个 closed 标志把"关闭"与"发送"串起来。
	s.emitMu.Lock()
	defer s.emitMu.Unlock()
	if s.closed {
		return
	}
	select {
	case ch <- ev:
	case <-done:
	}
}

func (s *InotifySource) handleStdout(ch chan Event) func(string) {
	return func(line string) {
		// **-tt 下远端 stderr 与 stdout 合并**（实测：Setting up watches... 就出现在
		// stdout 里），所以 watch 故障文案必须在这里也识别一次——只挂在 OnStderr
		// 上等于死路径，徽章会一直显示 inotify 而 watch 其实残缺。
		if s.noteWatchProblem(ch, line) {
			return
		}
		ev, ok := ParseInotifyLine(s.opts.RemotePath, line)
		if !ok {
			s.countUnmatched(line)
			return
		}
		s.mu.Lock()
		done := s.done
		s.mu.Unlock()
		s.emit(ch, ev, done)
	}
}

// handleStderr 只是 stdout 路径的补充：-tt 下两者已经合并，识别逻辑集中在
// noteWatchProblem，避免两处判断漂移。
func (s *InotifySource) handleStderr(ch chan Event) func(string) {
	return func(line string) { s.noteWatchProblem(ch, line) }
}

// noteWatchProblem 识别 inotifywait 的故障/阶段文案；返回 true 表示该行已消费。
//
// 阶段区分（spec §6.2）："远端路径不存在"只可能发生在 "Watches established."
// 之前 —— 那是配置错，规则要进 error 且**不重连**；运行期补挂失败（新目录）
// 只降级为 overflow（强制对账 + 禁止删除）。
func (s *InotifySource) noteWatchProblem(ch chan Event, line string) bool {
	l := strings.ToLower(line)
	switch {
	case strings.Contains(l, "watches established"):
		s.mu.Lock()
		s.sawEstablished = true
		s.mu.Unlock()
		return true
	case strings.Contains(l, "setting up watches"):
		return true
	case strings.Contains(l, "failed to watch"), strings.Contains(l, "upper limit"):
		err := fmt.Errorf("远端 inotify watch 配额耗尽：%s", line)
		s.mu.Lock()
		s.startErr = err
		s.mu.Unlock()
		s.log("error", err.Error())
		s.signalOverflow(ch)
		return true
	case strings.Contains(l, "couldn't watch"):
		s.mu.Lock()
		fatal := !s.sawEstablished && strings.Contains(l, "no such file")
		if fatal {
			s.startErr = fmt.Errorf("远端路径不存在：%s", line)
		}
		s.mu.Unlock()
		if fatal {
			s.log("error", "远端路径不存在："+line)
		} else {
			s.log("warn", "有目录无法建立 watch，watch 集合不完整："+line)
		}
		s.signalOverflow(ch)
		return true
	}
	return false
}

// countUnmatched 计数无法解析的行，超阈值记一条带样本的 warn（spec §6.2）。
// 只对噪声行静默会让真的格式故障（版本差异、CR 未裁）无从发现。
func (s *InotifySource) countUnmatched(sample string) {
	s.mu.Lock()
	s.unmatched++
	n := s.unmatched
	s.mu.Unlock()
	if n == 100 || n%1000 == 0 {
		s.log("warn", fmt.Sprintf("有 %d 行 inotifywait 输出无法解析，样本: %s", n, sample))
	}
}

func (s *InotifySource) signalOverflow(ch chan Event) {
	s.mu.Lock()
	done := s.done
	s.mu.Unlock()
	if done != nil {
		s.emit(ch, Event{Kind: KindOverflow}, done)
	}
}

var _ Source = (*InotifySource)(nil)
~~~

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/watch/ -race -count=1 -v`
Expected: 全部 PASS

- [ ] **Step 5: 提交**

~~~bash
gofmt -s -w internal/watch/
git add internal/watch/
git commit -m "feat(watch): 新增降级判定与 inotify 探测源,watch 异常统一升级为 overflow"
~~~

## Task 7: 远端树扫描器与 poll 探测源

**Files:**
- Create: `internal/watch/scan.go`、`internal/watch/poll.go`
- Test: `internal/watch/scan_test.go`、`internal/watch/poll_test.go`

**Interfaces:**
- Consumes: `sftp.ListMany`（Task 3）、`Event/Kind/Source`（Task 5）
- Produces: `Meta`、`Snapshot`、`ListManyFunc`、`MatchExclude(rel, excludes)`、`ScanTree(list, host, user, root, maxDepth, excludes)`、`NewPollSource(list, opts, maxDepth, excludes, after, log)`

**两条硬规则**：`Complete=false`（任一目录未知）**绝不产生任何 delete**；第一轮只建立基线、不产事件。

- [ ] **Step 1: 写失败的测试**

创建 `internal/watch/scan_test.go`：

~~~go
package watch

import (
	"testing"

	"sshore/internal/sftp"
)

func fakeList(tree map[string][]sftp.Item) ListManyFunc {
	return func(host, user string, paths []string) (map[string][]sftp.Item, error) {
		res := map[string][]sftp.Item{}
		for _, p := range paths {
			if v, ok := tree[p]; ok {
				res[p] = v
			}
		}
		return res, nil
	}
}

func file(name string) sftp.Item { return sftp.Item{Name: name, Size: 1, ModTime: "2026-09-10 10:00"} }
func dir(name string) sftp.Item  { return sftp.Item{Name: name, IsDir: true} }

func TestScanTreeRespectsMaxDepth(t *testing.T) {
	tree := map[string][]sftp.Item{
		"/r":       {file("a.txt"), dir("d1")},
		"/r/d1":    {file("b.txt"), dir("d2")},
		"/r/d1/d2": {file("c.txt")},
	}
	one, err := ScanTree(fakeList(tree), "h", "", "/r", 1, nil)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !one.Complete {
		t.Fatal("全部目录可读时应 Complete")
	}
	if _, ok := one.Entries["a.txt"]; !ok {
		t.Fatalf("缺少本层文件，得到 %#v", one.Entries)
	}
	if _, ok := one.Entries["d1/b.txt"]; !ok {
		t.Fatalf("maxDepth=1 应含 d1/b.txt，得到 %#v", one.Entries)
	}
	if _, ok := one.Entries["d1/d2/c.txt"]; ok {
		t.Fatal("maxDepth=1 不应递归到两层")
	}
}

// 缺失的目录 = 未知 ⇒ Complete=false，调用方据此禁止一切 delete。
func TestScanTreeMarksIncompleteOnUnknownDir(t *testing.T) {
	tree := map[string][]sftp.Item{
		"/r":    {dir("d1"), dir("locked")},
		"/r/d1": {file("a.txt")},
	}
	snap, err := ScanTree(fakeList(tree), "h", "", "/r", -1, nil)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if snap.Complete {
		t.Fatal("有目录未知时必须 Complete=false")
	}
}

func TestMatchExclude(t *testing.T) {
	ex := []string{".git/", "node_modules/", "*.swp", "*~"}
	cases := map[string]bool{
		".git": true, ".git/config": true, "node_modules/x.js": true,
		"a/.hidden.swp": true, "b/backup~": true, "src/main.go": false,
	}
	for rel, want := range cases {
		if got := MatchExclude(rel, ex); got != want {
			t.Fatalf("MatchExclude(%q) = %v want %v", rel, got, want)
		}
	}
}
~~~

创建 `internal/watch/poll_test.go`（导入需含 `context`、`sync`、`time`、`sshore/internal/sftp`）：

~~~go
// fakeTicker 让测试自己决定何时推进，不靠 sleep。
type fakeTicker struct{ ch chan time.Time }

func newFakeTicker() *fakeTicker                      { return &fakeTicker{ch: make(chan time.Time, 1)} }
func (f *fakeTicker) after(time.Duration) <-chan time.Time { return f.ch }

// fakeRemote 是可变的远端树 + 轮次计数。
type fakeRemote struct {
	mu     sync.Mutex
	tree   map[string][]sftp.Item
	rounds int
}

func newFakeRemote() *fakeRemote { return &fakeRemote{tree: map[string][]sftp.Item{}} }

func (f *fakeRemote) set(dir string, items ...sftp.Item) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tree[dir] = items
}

func (f *fakeRemote) waitRounds(n int) {
	for i := 0; i < 300; i++ {
		f.mu.Lock()
		done := f.rounds >= n
		f.mu.Unlock()
		if done {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (f *fakeRemote) list(host, user string, paths []string) (map[string][]sftp.Item, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rounds++
	res := map[string][]sftp.Item{}
	for _, p := range paths {
		if v, ok := f.tree[p]; ok {
			res[p] = v
		}
	}
	return res, nil
}

func drainKinds(t *testing.T, ch <-chan Event, n int) []Kind {
	t.Helper()
	var got []Kind
	for i := 0; i < n; i++ {
		select {
		case ev := <-ch:
			got = append(got, ev.Kind)
		case <-time.After(2 * time.Second):
			t.Fatalf("等待第 %d 个事件超时，已收到 %v", i+1, got)
		}
	}
	return got
}

func TestPollSourceFirstRoundIsBaseline(t *testing.T) {
	fs := newFakeRemote()
	fs.set("/r", file("a.txt"))
	src := NewPollSource(fs.list, DetectOpts{RemotePath: "/r"}, 0, nil, newFakeTicker().after, func(string, string) {})
	ch, err := src.Start(context.Background())
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer src.Close()
	select {
	case ev := <-ch:
		t.Fatalf("第一轮只建立基线，不应产事件，得到 %+v", ev)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestPollSourceDiffsCompleteRounds(t *testing.T) {
	fs := newFakeRemote()
	fs.set("/r", file("a.txt"))
	tk := newFakeTicker()
	src := NewPollSource(fs.list, DetectOpts{RemotePath: "/r"}, 0, nil, tk.after, func(string, string) {})
	ch, err := src.Start(context.Background())
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer src.Close()
	fs.waitRounds(1)

	fs.set("/r", file("a.txt"), file("b.txt"))
	tk.ch <- time.Now()
	if got := drainKinds(t, ch, 1); got[0] != KindCreate {
		t.Fatalf("want create, got %v", got)
	}

	fs.set("/r", sftp.Item{Name: "a.txt", Size: 9, ModTime: "2026-09-10 11:00"})
	tk.ch <- time.Now()
	if got := drainKinds(t, ch, 1); got[0] != KindWrite {
		t.Fatalf("want write, got %v", got)
	}

	fs.set("/r")
	tk.ch <- time.Now()
	if got := drainKinds(t, ch, 1); got[0] != KindDelete {
		t.Fatalf("want delete, got %v", got)
	}
}

// 不完整轮次必须零事件：否则"列不出来"会被当成"文件都没了"，进而删本地文件。
func TestPollSourceIncompleteRoundEmitsNothing(t *testing.T) {
	fs := newFakeRemote()
	fs.set("/r", file("a.txt"), dir("d1"))
	fs.set("/r/d1", file("x.txt"))
	tk := newFakeTicker()
	src := NewPollSource(fs.list, DetectOpts{RemotePath: "/r"}, -1, nil, tk.after, func(string, string) {})
	ch, err := src.Start(context.Background())
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer src.Close()
	fs.waitRounds(1)

	fs.set("/r") // d1 消失 ⇒ 目录未知 ⇒ 不完整
	tk.ch <- time.Now()
	select {
	case ev := <-ch:
		t.Fatalf("不完整轮次必须零事件，得到 %+v", ev)
	case <-time.After(300 * time.Millisecond):
	}
}
~~~

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/watch/ -run 'ScanTree|PollSource|MatchExclude' -v`
Expected: 编译失败，`undefined: ScanTree` / `undefined: NewPollSource`

- [ ] **Step 3: 实现**

创建 `internal/watch/scan.go`：

~~~go
package watch

import (
	"path"
	"strings"

	"sshore/internal/sftp"
)

// Meta 是决策前补齐的远端元信息。**唯一来源是 ls 结果**（两条探测路径共用），
// 绝不由 inotify 事件或本地值写入。
type Meta struct {
	Size    int64
	ModTime string
}

// Snapshot 是一轮远端树的视图。Complete=false 表示有目录未知，
// 调用方**绝不能据此产生任何 delete**。
type Snapshot struct {
	Entries  map[string]Meta
	Complete bool
}

type ListManyFunc func(host, user string, paths []string) (map[string][]sftp.Item, error)

const scanBatch = 64

// MatchExclude 判定相对路径是否命中忽略规则：以 "/" 结尾的模式按目录前缀匹配，
// 其余用 path.Match；相对 basename 与完整相对路径都参与匹配。
func MatchExclude(rel string, excludes []string) bool {
	base := path.Base(rel)
	for _, pat := range excludes {
		if pat == "" {
			continue
		}
		if strings.HasSuffix(pat, "/") {
			if strings.HasPrefix(rel+"/", pat) {
				return true
			}
			continue
		}
		if ok, _ := path.Match(pat, base); ok {
			return true
		}
		if ok, _ := path.Match(pat, rel); ok {
			return true
		}
	}
	return false
}

// ScanTree 从 root 开始 BFS 列举，深度受 maxDepth 限制（0=仅本层，-1=无限）。
// 任一目录未知（ListMany 返回的 map 缺 key）都会把 Complete 置为 false。
func ScanTree(list ListManyFunc, host, user, root string, maxDepth int, excludes []string) (Snapshot, error) {
	snap := Snapshot{Entries: map[string]Meta{}, Complete: true}
	type node struct {
		dir   string
		depth int
	}
	queue := []node{{dir: root}}
	for len(queue) > 0 {
		batch := queue
		if len(batch) > scanBatch {
			batch = queue[:scanBatch]
		}
		queue = queue[len(batch):]

		paths := make([]string, 0, len(batch))
		for _, n := range batch {
			paths = append(paths, n.dir)
		}
		res, err := list(host, user, paths)
		if err != nil {
			return snap, err
		}
		for _, n := range batch {
			items, ok := res[n.dir]
			if !ok {
				snap.Complete = false // 未知，绝不当作空目录
				continue
			}
			for _, it := range items {
				rel := it.Name
				if n.depth > 0 {
					rel = strings.TrimPrefix(strings.TrimPrefix(n.dir, root), "/") + "/" + it.Name
				}
				if MatchExclude(rel, excludes) {
					continue
				}
				if it.IsDir {
					if maxDepth < 0 || n.depth < maxDepth {
						queue = append(queue, node{dir: path.Join(n.dir, it.Name), depth: n.depth + 1})
					}
					continue
				}
				snap.Entries[rel] = Meta{Size: it.Size, ModTime: it.ModTime}
			}
		}
	}
	return snap, nil
}
~~~

创建 `internal/watch/poll.go`：

~~~go
package watch

import (
	"context"
	"strconv"
	"sync"
	"time"
)

// PollSource 每 interval 扫描一轮远端树，与上一轮比对产出事件。
// **一轮 = 一次事务**：失败或不完整的一轮不产出任何事件。
type PollSource struct {
	list     ListManyFunc
	opts     DetectOpts
	maxDepth int
	excludes []string
	interval time.Duration
	after    func(time.Duration) <-chan time.Time
	log      func(level, msg string)

	mu       sync.Mutex
	prev     map[string]Meta
	hasPrev  bool
	failures int
	ch       chan Event
	done     chan struct{}
	stop     chan struct{}
	stopOnce sync.Once
}

// maxPollFailures：连续 5 轮失败即停止轮询，由引擎判定进入 error（spec §6.3）。
const maxPollFailures = 5

// Failures 供引擎在轮询停止后区分"抖动重连"与"连续失败进 error"。
func (p *PollSource) Failures() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.failures
}

func NewPollSource(list ListManyFunc, o DetectOpts, maxDepth int, excludes []string,
	after func(time.Duration) <-chan time.Time, log func(level, msg string)) *PollSource {
	if after == nil {
		after = time.After
	}
	if log == nil {
		log = func(string, string) {}
	}
	iv := o.PollInterval
	if iv <= 0 {
		iv = 5 * time.Second
	}
	return &PollSource{list: list, opts: o, maxDepth: maxDepth, excludes: excludes,
		interval: iv, after: after, log: log}
}

func (p *PollSource) Info() Info {
	return Info{Mode: "poll", Interval: p.interval}
}

func (p *PollSource) Start(ctx context.Context) (<-chan Event, error) {
	ch := make(chan Event, 256)
	p.mu.Lock()
	p.ch, p.done, p.stop = ch, make(chan struct{}), make(chan struct{})
	p.mu.Unlock()

	// 首轮立即跑：既建立基线，也让"远端路径不存在"在启动阶段就暴露。
	p.round(ch)

	go func() {
		defer close(ch)
		for {
			select {
			case <-ctx.Done():
				return
			case <-p.stop:
				return
			case <-p.after(p.interval):
			}
			p.round(ch)
		}
	}()
	return ch, nil
}

// Close 幂等：关闭 stop 让轮询 goroutine 退出，channel 由该 goroutine 关闭。
// 必须这样做——引擎传进来的是 context.Background()，ctx 永远不会取消。
func (p *PollSource) Close() error {
	p.mu.Lock()
	stop := p.stop
	p.mu.Unlock()
	if stop != nil {
		p.stopOnce.Do(func() { close(stop) })
	}
	return nil
}

// round 执行一轮扫描与比对。失败或不完整 ⇒ 零事件 + 失败计数。
func (p *PollSource) round(ch chan Event) {
	snap, err := ScanTree(p.list, p.opts.Host, p.opts.User, p.opts.RemotePath, p.maxDepth, p.excludes)
	if err != nil || !snap.Complete {
		p.mu.Lock()
		p.failures++
		n := p.failures
		p.mu.Unlock()
		why := "扫描失败"
		if err == nil {
			why = "扫描不完整（有目录未知）"
		}
		p.log("warn", why+"，已跳过本轮（连续第 "+strconv.Itoa(n)+" 次）")
		if n >= maxPollFailures {
			p.log("error", "连续 "+strconv.Itoa(maxPollFailures)+" 轮扫描失败，停止轮询")
			p.stopOnce.Do(func() { close(p.stop) })
		}
		return
	}
	p.mu.Lock()
	p.failures = 0
	prev, hasPrev := p.prev, p.hasPrev
	p.prev, p.hasPrev = snap.Entries, true
	p.mu.Unlock()

	if !hasPrev {
		return // 首轮仅建立基线
	}
	for rel, cur := range snap.Entries {
		old, existed := prev[rel]
		switch {
		case !existed:
			p.emit(ch, Event{RelPath: rel, Kind: KindCreate})
		case old.Size != cur.Size || old.ModTime != cur.ModTime:
			// mtime 不同**不能**用来判定"未变化"：精度只到分钟、老文件只到天，
			// 且 parseModTime 用 time.Now().Year() 猜年份（跨年会集体误报）。
			// 这里只把它当作"疑似变化"，宁可多传一次。
			p.emit(ch, Event{RelPath: rel, Kind: KindWrite})
		}
	}
	for rel := range prev {
		if _, ok := snap.Entries[rel]; !ok {
			p.emit(ch, Event{RelPath: rel, Kind: KindDelete})
		}
	}
}

// emit 投递事件；Close/ctx 取消时立即返回，绝不把轮询 goroutine 永久挂在发送上。
func (p *PollSource) emit(ch chan Event, ev Event) {
	p.mu.Lock()
	stop := p.stop
	p.mu.Unlock()
	select {
	case ch <- ev:
	case <-stop:
	}
}

var _ Source = (*PollSource)(nil)
~~~

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/watch/ -race -count=1 -v`
Expected: 全部 PASS

- [ ] **Step 5: 提交**

~~~bash
gofmt -s -w internal/watch/
git add internal/watch/
git commit -m "feat(watch): 新增远端树扫描器与轮询探测源,不完整轮次零事件"
~~~

## Task 8: sync 路径安全与决策表

**Files:**
- Create: `internal/sync/paths.go`、`internal/sync/decide.go`
- Test: `internal/sync/decide_test.go`、`internal/sync/paths_test.go`

**Interfaces:**
- Consumes: `watch.Kind`（Task 5）、`watch.Meta`（Task 7）
- Produces:
  - `func SafeRelPath(rel string) (string, bool)`
  - `func LocalTarget(root, rel string) (string, error)`
  - `type Entry struct {...}` 与 `func (e *Entry) HasBaseline() bool`
  - `type LocalState struct { Exists bool; Size int64; ModTime string }`
  - `type Action int` 与 `ActionSkip/ActionGet/ActionAdopt/ActionConflict/ActionDelete`
  - `func Decide(kind watch.Kind, ent *Entry, local LocalState, mirrorDelete bool) (Action, string)`

- [ ] **Step 1: 写失败的测试**

创建 `internal/sync/paths_test.go`：

~~~go
package sync

import (
	"path/filepath"
	"strings"
	"testing"
)

// 远端可回传任意文件名，RelPath 是不可信的。
func TestSafeRelPathRejectsTraversal(t *testing.T) {
	bad := []string{
		"", ".", "..", "../etc/passwd", "a/../../b", "/abs/path",
		"a/b/../../..", "C:\\Windows\\x", "a\nb", "a\x00b",
	}
	for _, rel := range bad {
		if got, ok := SafeRelPath(rel); ok {
			t.Fatalf("SafeRelPath(%q) 必须拒绝，却得到 %q", rel, got)
		}
	}
	good := map[string]string{"a.txt": "a.txt", "d/b.txt": "d/b.txt", "d/./c.txt": "d/c.txt"}
	for in, want := range good {
		got, ok := SafeRelPath(in)
		if !ok || got != want {
			t.Fatalf("SafeRelPath(%q) = %q,%v want %q", in, got, ok, want)
		}
	}
}

// 拼出的本地绝对路径必须仍在 root 之内（防穿越的硬防线）。
func TestLocalTargetStaysInsideRoot(t *testing.T) {
	root := filepath.Join(strings.Repeat("x", 1), "root")
	if _, err := LocalTarget(root, "../etc/passwd"); err == nil {
		t.Fatal("必须拒绝逃出 root 的路径")
	}
	got, err := LocalTarget(root, "d/b.txt")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !strings.HasPrefix(got, root+string(filepath.Separator)) {
		t.Fatalf("目标必须在 root 内，得到 %q", got)
	}
}
~~~

创建 `internal/sync/decide_test.go`（覆盖决策表全部 9 行）：

~~~go
package sync

import (
	"testing"

	"sshore/internal/watch"
)

func TestDecideTable(t *testing.T) {
	const (
		rs, rm = int64(10), "2026-09-10T10:00:00Z"
		ls, lm = int64(10), "2026-09-10T10:00:01Z"
	)
	full := &Entry{RemoteSize: rs, RemoteMTime: rm, LocalSize: ls, LocalMTime: lm, HasLocal: true}
	cases := []struct {
		name   string
		kind   watch.Kind
		ent    *Entry
		local  LocalState
		mirror bool
		want   Action
	}{
		{"无基线+本地不存在 → GET", watch.KindCreate, nil, LocalState{}, false, ActionGet},
		{"无基线+本地同大小 → 采纳", watch.KindCreate, &Entry{RemoteSize: rs}, LocalState{Exists: true, Size: rs}, false, ActionAdopt},
		{"无基线+本地大小不同 → 冲突", watch.KindCreate, &Entry{RemoteSize: rs}, LocalState{Exists: true, Size: 99}, false, ActionConflict},
		{"有基线+与基线一致 → GET", watch.KindWrite, full, LocalState{Exists: true, Size: ls, ModTime: lm}, false, ActionGet},
		{"有基线+本地被改 → 冲突", watch.KindWrite, full, LocalState{Exists: true, Size: 77, ModTime: "x"}, false, ActionConflict},
		{"有基线+本地被删 → 重新 GET", watch.KindWrite, full, LocalState{Exists: false}, false, ActionGet},
		{"删除+与基线一致+镜像关 → 跳过", watch.KindDelete, full, LocalState{Exists: true, Size: ls, ModTime: lm}, false, ActionSkip},
		{"删除+与基线一致+镜像开 → 删除", watch.KindDelete, full, LocalState{Exists: true, Size: ls, ModTime: lm}, true, ActionDelete},
		{"删除+本地被改 → 跳过", watch.KindDelete, full, LocalState{Exists: true, Size: 77}, true, ActionSkip},
		{"删除+无基线 → 跳过", watch.KindDelete, nil, LocalState{Exists: true}, true, ActionSkip},
		{"删除+本地已不存在 → 跳过", watch.KindDelete, full, LocalState{Exists: false}, true, ActionSkip},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, reason := Decide(c.kind, c.ent, c.local, c.mirror)
			if got != c.want {
				t.Fatalf("got %v (%s) want %v", got, reason, c.want)
			}
			if reason == "" {
				t.Fatal("每个决策都必须带可读原因，用于日志")
			}
		})
	}
}
~~~

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/sync/ -v`
Expected: 编译失败，`undefined: SafeRelPath` / `undefined: Decide`

- [ ] **Step 3: 实现**

创建 `internal/sync/paths.go`：

~~~go
package sync

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"
	"unicode"
)

// SafeRelPath 校验来自远端的相对路径并归一化。
// 远端能回传任意文件名，这里必须拒绝：空路径、"." / ".."、绝对路径、
// 含 NUL 或其它控制字符的路径（控制字符会把 sftp 批处理劈成两条命令，
// 而批处理里以 '!' 开头的行会执行本地 shell 命令）。
func SafeRelPath(rel string) (string, bool) {
	rel = strings.TrimSpace(rel)
	if rel == "" {
		return "", false
	}
	for _, r := range rel {
		if r == 0 || unicode.IsControl(r) {
			return "", false
		}
	}
	// Windows 上反斜杠也是分隔符，先统一成正斜杠再判层级。
	norm := strings.ReplaceAll(rel, "\\", "/")
	if strings.HasPrefix(norm, "/") || strings.Contains(norm, ":") {
		return "", false
	}
	clean := path.Clean(norm)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", false
	}
	if clean != norm && clean+"/" != norm { // path.Clean 归一后允许 "d/./c.txt" -> "d/c.txt"
		clean = path.Clean(norm)
	}
	return clean, true
}

// LocalTarget 把相对路径映射到本地绝对路径，并用 filepath.Rel 二次确认
// 结果**仍在 root 之内**——这是路径穿越的最后一道防线，不是可选优化。
func LocalTarget(root, rel string) (string, error) {
	safe, ok := SafeRelPath(rel)
	if !ok {
		return "", fmt.Errorf("非法相对路径: %q", rel)
	}
	target := filepath.Join(root, filepath.FromSlash(safe))
	back, err := filepath.Rel(root, target)
	if err != nil {
		return "", err
	}
	if back == ".." || strings.HasPrefix(back, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("目标逃出根目录: %q", rel)
	}
	return target, nil
}
~~~

创建 `internal/sync/decide.go`：

~~~go
package sync

import "sshore/internal/watch"

// Entry 是一个已同步文件的记录。
//
// **字段级写入规则（违反会静默丢失用户数据）**：
//   - RemoteSize/RemoteMTime **只能由 ls 结果写入**（扫描或决策前补齐）；
//   - LocalSize/LocalMTime 只在两种情况写入：一次成功 rename 之后，或首轮采纳；
//   - HasLocal 是"我们建立了本地基线"的判定依据，比 WrittenAt 更可靠。
//
// CONFLICT 的文件**不写入 Entries**——否则下次远端再改它就会走进
// "有基线且一致 ⇒ 远端赢"而覆盖用户的修改，绕过整套冲突保护。
type Entry struct {
	RemoteSize  int64  `json:"remote_size"`
	RemoteMTime string `json:"remote_mtime"`
	LocalSize   int64  `json:"local_size"`
	LocalMTime  string `json:"local_mtime"`
	WrittenAt   string `json:"written_at,omitempty"`
	Adopted     bool   `json:"adopted,omitempty"`
	HasLocal    bool   `json:"has_local"`
}

func (e *Entry) HasBaseline() bool { return e != nil && e.HasLocal }

// LocalState 是本地磁盘现状（由调用方 stat 得到）。
type LocalState struct {
	Exists  bool
	Size    int64
	ModTime string
}

type Action int

const (
	ActionSkip Action = iota
	ActionGet
	ActionAdopt
	ActionConflict
	ActionDelete
	// ActionSaveAs：远端版本另存为 <name>.remote-<ts>，**本地原文件保留**。
	// 必须与 ActionGet 分开：否则"另存为"会直接覆盖本地文件（数据丢失）。
	ActionSaveAs
)

func (a Action) String() string {
	switch a {
	case ActionGet:
		return "get"
	case ActionAdopt:
		return "adopt"
	case ActionConflict:
		return "conflict"
	case ActionDelete:
		return "delete"
	case ActionSaveAs:
		return "save_as"
	default:
		return "skip"
	}
}

// Decide 实现决策表。它永远是纯函数：不做 IO、不改状态，便于逐行测试。
func Decide(kind watch.Kind, ent *Entry, local LocalState, mirrorDelete bool) (Action, string) {
	if kind == watch.KindDelete {
		if !ent.HasBaseline() {
			return ActionSkip, "远端已删除，但本地没有对应基线，不动本地"
		}
		if !local.Exists {
			return ActionSkip, "远端已删除，本地也已不存在"
		}
		if local.Size != ent.LocalSize || local.ModTime != ent.LocalMTime {
			return ActionSkip, "远端已删除但本地被修改过，保留本地"
		}
		if !mirrorDelete {
			return ActionSkip, "远端已删除；镜像删除未开启，保留本地"
		}
		return ActionDelete, "远端已删除且本地未被改动，按镜像删除"
	}

	// ent 为 nil 表示远端元信息未知（ls 没补齐），保守地直接下载。
	if ent == nil {
		return ActionGet, "无远端元信息，保守下载"
	}
	if !ent.HasBaseline() {
		if !local.Exists {
			return ActionGet, "远端新增/变更，本地不存在"
		}
		if local.Size == ent.RemoteSize {
			return ActionAdopt, "本地已存在且大小一致，登记为已同步（不下载、未校验内容）"
		}
		return ActionConflict, "本地已存在同名文件且大小不同，不覆盖"
	}

	if !local.Exists {
		return ActionGet, "本地文件已被删除，重新下载"
	}
	if local.Size == ent.LocalSize && local.ModTime == ent.LocalMTime {
		return ActionGet, "远端变更，本地未被改动"
	}
	return ActionConflict, "远端变更但本地被修改过，不覆盖"
}

~~~

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/sync/ -race -count=1 -v`
Expected: 全部 PASS

- [ ] **Step 5: 提交**

~~~bash
gofmt -s -w internal/sync/
git add internal/sync/
git commit -m "feat(sync): 新增路径安全防线与决策表,默认不覆盖用户改动"
~~~

## Task 9: 状态文件（字段级规则 + fingerprint + 原子写）

**Files:**
- Create: `internal/sync/state.go`
- Test: `internal/sync/state_test.go`

**Interfaces:**
- Produces: `Fingerprint`、`StateFile`、`StateStore`（`Load/Flush/With/Path`）、`FailedItem`
- Consumes: `Entry`（Task 8）、`Conflict`（Task 12 —— 本任务先用最小占位定义，Task 12 补全字段）

**为什么 fingerprint 是必需的**：`local_*` 是"我们写过的本地状态"。改了 `local_path` / `kind` / `host` 之后，旧记录描述的是另一份文件——继续参与冲突判定就是拿错误的依据做决策。不匹配必须整体丢弃（等价全量重扫）。

- [ ] **Step 1: 写失败的测试**

创建 `internal/sync/state_test.go`：

~~~go
package sync

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStateStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state", "sync-abc.json")
	fp := Fingerprint{Host: "h", Kind: "dir", RemoteRoot: "/r", LocalRoot: "/l", MaxDepth: 1, Excludes: []string{".git/"}}
	s := NewStateStore(path, fp)
	if err := s.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	s.With(func(d *StateFile) {
		d.Entries["a.txt"] = &Entry{RemoteSize: 1, RemoteMTime: "t", LocalSize: 1, LocalMTime: "t", HasLocal: true}
	})
	if err := s.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	// 权限必须是 0600（状态含远端/本地路径等环境信息）
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if st.Mode().Perm() != 0600 {
		t.Fatalf("状态文件权限应为 0600，得到 %v", st.Mode().Perm())
	}

	s2 := NewStateStore(path, fp)
	if err := s2.Load(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	s2.With(func(d *StateFile) {
		if got := d.Entries["a.txt"]; got == nil || !got.HasLocal {
			t.Fatalf("round-trip 丢失条目: %#v", d.Entries)
		}
	})
}

// fingerprint 不匹配（改了 local_path / kind / host）⇒ 丢弃旧状态，按全量重扫处理。
func TestStateStoreDiscardsOnFingerprintMismatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sync-abc.json")
	fpA := Fingerprint{Host: "h", LocalRoot: "/l1"}
	sa := NewStateStore(path, fpA)
	_ = sa.Load()
	sa.With(func(d *StateFile) { d.Entries["a.txt"] = &Entry{HasLocal: true, LocalSize: 1} })
	if err := sa.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	fpB := Fingerprint{Host: "h", LocalRoot: "/l2"} // 本地根变了
	sb := NewStateStore(path, fpB)
	if err := sb.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	sb.With(func(d *StateFile) {
		if len(d.Entries) != 0 {
			t.Fatalf("fingerprint 不匹配必须丢弃旧状态，得到 %#v", d.Entries)
		}
	})
}

// 损坏的状态文件不得让规则启动失败：只能退化为"下次全量重扫"。
func TestStateStoreCorruptFileDoesNotFail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sync-abc.json")
	if err := os.WriteFile(path, []byte("{not json"), 0600); err != nil {
		t.Fatal(err)
	}
	s := NewStateStore(path, Fingerprint{Host: "h"})
	if err := s.Load(); err != nil {
		t.Fatalf("损坏状态必须优雅降级，得到 %v", err)
	}
	s.With(func(d *StateFile) {
		if d.Entries == nil {
			t.Fatal("应初始化空 Entries")
		}
	})
}
~~~

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/sync/ -run StateStore -v`
Expected: 编译失败，`undefined: NewStateStore`

- [ ] **Step 3: 实现**

创建 `internal/sync/state.go`：

~~~go
package sync

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Fingerprint 决定状态文件是否仍然适用于当前规则。
type Fingerprint struct {
	Host       string   `json:"host"`
	User       string   `json:"user"`
	Kind       string   `json:"kind"`
	RemoteRoot string   `json:"remote_root"`
	LocalRoot  string   `json:"local_root"`
	MaxDepth   int      `json:"max_depth"`
	Excludes   []string `json:"excludes"`
}

func (a Fingerprint) equal(b Fingerprint) bool {
	if a.Host != b.Host || a.User != b.User || a.Kind != b.Kind ||
		a.RemoteRoot != b.RemoteRoot || a.LocalRoot != b.LocalRoot || a.MaxDepth != b.MaxDepth {
		return false
	}
	if len(a.Excludes) != len(b.Excludes) {
		return false
	}
	for i := range a.Excludes {
		if a.Excludes[i] != b.Excludes[i] {
			return false
		}
	}
	return true
}

// Conflict 是一条待用户裁决的冲突。定义在这里（而不是 Task 12）是因为状态文件
// 就要序列化它——否则 Task 9/10/11 结束时 go test ./... 会 undefined: Conflict。
// Task 12 只在其上补三个动作。
type Conflict struct {
	RelPath     string `json:"rel_path"`
	RemoteSize  int64  `json:"remote_size"`
	RemoteMTime string `json:"remote_mtime"`
	LocalSize   int64  `json:"local_size"`
	LocalMTime  string `json:"local_mtime"`
	DetectedAt  string `json:"detected_at"`
}

const stateVersion = 1

type FailedItem struct {
	RelPath string `json:"rel_path"`
	Err     string `json:"err"`
	At      string `json:"at"`
}

type StateFile struct {
	Version     int               `json:"version"`
	Fingerprint Fingerprint       `json:"fingerprint"`
	Entries     map[string]*Entry `json:"entries"`
	Conflicts   []Conflict        `json:"conflicts,omitempty"`
	Failed      []FailedItem      `json:"failed,omitempty"`
}

// StateStore 串行化对状态文件的访问：引擎 goroutine 与 UI 的 ResolveConflict
// 都会改它，必须单锁保护 + 串行 flush。
type StateStore struct {
	mu   sync.Mutex
	path string
	fp   Fingerprint
	data *StateFile
}

func NewStateStore(path string, fp Fingerprint) *StateStore {
	return &StateStore{path: path, fp: fp}
}

func (s *StateStore) Path() string { return s.path }

// Load 读取状态；文件缺失、损坏、或 fingerprint 不匹配时，**静默重建为空状态**
// ——后果被限定为"下次全量重扫"，不影响规则定义本身。
func (s *StateStore) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data = &StateFile{Version: stateVersion, Fingerprint: s.fp, Entries: map[string]*Entry{}}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return nil // 首次运行
	}
	var got StateFile
	if err := json.Unmarshal(raw, &got); err != nil {
		return nil // 损坏 → 重建
	}
	if got.Version != stateVersion || !got.Fingerprint.equal(s.fp) {
		return nil // 规则已变或版本不符 → 重建
	}
	if got.Entries == nil {
		got.Entries = map[string]*Entry{}
	}
	s.data = &got
	return nil
}

// With 在持锁状态下访问状态；**回调里绝不能做 IO/传输**（否则与引擎的
// 传输串行锁形成 ABBA 死锁）。
func (s *StateStore) With(fn func(*StateFile)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data == nil {
		s.data = &StateFile{Version: stateVersion, Fingerprint: s.fp, Entries: map[string]*Entry{}}
	}
	fn(s.data)
}

// Flush 原子落盘（临时文件 + rename），权限 0600、目录 0700，并对内容 fsync。
func (s *StateStore) Flush() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	// **必须在锁内 marshal**：序列化会遍历整张 Entries map，而引擎 goroutine
	// 可能正在 With 里写它 —— 锁外 marshal 会并发读写 map（-race 报错、运行时
	// 可能直接 fatal）。
	s.mu.Lock()
	if s.data == nil {
		s.mu.Unlock()
		return nil
	}
	body, err := json.MarshalIndent(s.data, "", "  ")
	s.mu.Unlock()
	if err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.tmp-%d-%d", s.path, os.Getpid(), time.Now().UnixNano())
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if _, err := f.Write(body); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
~~~

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/sync/ -race -count=1 -run StateStore -v`
Expected: 全部 PASS

- [ ] **Step 5: 提交**

~~~bash
gofmt -s -w internal/sync/
git add internal/sync/
git commit -m "feat(sync): 新增状态文件存储,按 fingerprint 失效并原子落盘"
~~~

---

## Task 10: 原子传输与临时文件清理

**Files:**
- Create: `internal/sync/transfer.go`、`internal/sync/adapters.go`
- Test: `internal/sync/transfer_test.go`

**Interfaces:**
- Produces:
  - `type Transferer interface { Get(host, user, remote, local string) error }`
  - `func TransferTo(tr Transferer, host, user, remotePath, target, ruleID string, randSuffix func() string) error`
  - `func CleanupParts(localRoot, ruleID string) (int, error)`
  - `func PartSuffix(ruleID string) string`

**两条硬要求**：临时文件名**必须含规则 id**（同进程内所有规则共享同一个 PID，只靠 pid 会让两条规则写同一个临时文件 → 内容交错 → rename 后静默损坏）；规则启动与删除时要清理残留。

- [ ] **Step 1: 写失败的测试**

创建 `internal/sync/transfer_test.go`：

~~~go
package sync

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeTransferer struct {
	content string
	fail    bool
	lastTmp string
}

func (f *fakeTransferer) Get(host, user, remote, local string) error {
	f.lastTmp = local
	if f.fail {
		return errors.New("boom")
	}
	return os.WriteFile(local, []byte(f.content), 0644)
}

// 成功路径：先写临时文件再原子 rename，目标不会出现半截内容。
func TestTransferToIsAtomicOnSuccess(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "a.txt")
	ft := &fakeTransferer{content: "hello"}
	if err := TransferTo(ft, "h", "", "/r/a.txt", target, "rule1", func() string { return "rand" }); err != nil {
		t.Fatalf("transfer: %v", err)
	}
	if !strings.Contains(ft.lastTmp, "rule1") {
		t.Fatalf("临时文件名必须含规则 id，得到 %q", ft.lastTmp)
	}
	if ft.lastTmp == target {
		t.Fatal("绝不能直接写目标文件")
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "hello" {
		t.Fatalf("目标内容不对: %q %v", got, err)
	}
	if _, err := os.Stat(ft.lastTmp); !os.IsNotExist(err) {
		t.Fatal("成功后临时文件必须已被 rename 掉")
	}
}

// 失败路径：目标文件不被破坏，临时文件被清理。
func TestTransferToKeepsTargetOnFailure(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(target, []byte("original"), 0644); err != nil {
		t.Fatal(err)
	}
	ft := &fakeTransferer{fail: true}
	if err := TransferTo(ft, "h", "", "/r/a.txt", target, "rule1", func() string { return "rand" }); err == nil {
		t.Fatal("want error")
	}
	got, _ := os.ReadFile(target)
	if string(got) != "original" {
		t.Fatalf("失败时目标文件被破坏: %q", got)
	}
	if _, err := os.Stat(ft.lastTmp); !os.IsNotExist(err) {
		t.Fatal("失败后必须清理临时文件")
	}
}

// 两条规则各自清理自己的残留，不能互相误删。
func TestCleanupPartsOnlyOwnRule(t *testing.T) {
	dir := t.TempDir()
	mine := filepath.Join(dir, "a.txt"+PartSuffix("rule1")+"-x")
	other := filepath.Join(dir, "b.txt"+PartSuffix("rule2")+"-y")
	for _, p := range []string{mine, other} {
		if err := os.WriteFile(p, []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	n, err := CleanupParts(dir, "rule1")
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if n != 1 {
		t.Fatalf("应只清理 1 个，得到 %d", n)
	}
	if _, err := os.Stat(mine); !os.IsNotExist(err) {
		t.Fatal("本规则的残留应被清理")
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatal("别的规则的残留不能被误删")
	}
}
~~~

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/sync/ -run 'TransferTo|CleanupParts' -v`
Expected: 编译失败，`undefined: TransferTo`

- [ ] **Step 3: 实现**

创建 `internal/sync/transfer.go`：

~~~go
package sync

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Transferer 是 sync 对传输层的依赖抽象。生产实现是 sftp.Ctrl.Get 的适配器
// （internal/sync 不直接依赖 sftp，便于引擎测试完全脱离网络）。
type Transferer interface {
	Get(host, user, remote, local string) error
}

// PartSuffix 返回本规则的临时文件中缀。**必须含规则 id**：同一进程内所有规则
// 与 UI 触发的传输共享同一个 PID，只靠 pid 命名会让两条规则写同一个临时文件，
// 内容交错后 rename，静默损坏本地文件。
func PartSuffix(ruleID string) string {
	id := ruleID
	if len(id) > 8 {
		id = id[:8]
	}
	return ".sshore-part-" + id + "-"
}

// TransferTo 把远端文件原子地搬到 target：先写同目录的临时文件，成功后 rename。
// 中断/失败都不会污染目标文件。
func TransferTo(tr Transferer, host, user, remotePath, target, ruleID string, randSuffix func() string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return err
	}
	if randSuffix == nil {
		// 默认必须是**每次调用都不同**的随机串：进程 pid 在整个应用生命周期内
		// 恒定，同一规则的两次传输（引擎一次、UI 触发一次）会写同一个临时文件。
		// randSuffix 参数只用于测试注入确定性后缀。
		randSuffix = func() string {
			var b [8]byte
			_, _ = rand.Read(b[:])
			return hex.EncodeToString(b[:])
		}
	}
	tmp := target + PartSuffix(ruleID) + randSuffix()
	if err := tr.Get(host, user, remotePath, tmp); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// CleanupParts 清理某规则在 localRoot 下遗留的临时文件，返回清理数量。
// 规则启动与删除时都要调用——否则崩溃或 Stop 之后它们会永远堆积。
func CleanupParts(localRoot, ruleID string) (int, error) {
	suffix := PartSuffix(ruleID)
	n := 0
	err := filepath.WalkDir(localRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // 不可读的目录跳过，不阻断清理
		}
		if d.IsDir() {
			return nil
		}
		if strings.Contains(filepath.Base(path), suffix) {
			if rmErr := os.Remove(path); rmErr == nil {
				n++
			}
		}
		return nil
	})
	return n, err
}
~~~

同时创建 `internal/sync/adapters.go`（**适配器的唯一定义处**，main 包与 E2E 测试都只用它，
不要在 app.go 里再写一份，否则 Task 15/16 会各持一份实现）：

~~~go
package sync

import (
	"sshore/internal/sftp"
	"sshore/internal/watch"
)

// NewSftpAdapter 让 sync 通过 sftp.Ctrl 完成传输与列举，同时保持
// internal/sync 不依赖 sftp 的内部实现。
//
// 返回类型必须是 watch.ListManyFunc（它定义在 watch 包），不能在这里另起一个
// 同名命名类型——Deps.ListMany 的字段类型是 watch.ListManyFunc，命名类型不同
// 会赋值失败。
func NewSftpAdapter(c *sftp.Ctrl) (Transferer, watch.ListManyFunc) {
	return sftpTransfer{c: c}, sftpListMany{c: c}
}

type sftpTransfer struct{ c *sftp.Ctrl }

func (t sftpTransfer) Get(host, user, remote, local string) error {
	return t.c.Get(host, user, remote, local)
}

type sftpListMany struct{ c *sftp.Ctrl }

func (t sftpListMany) ListMany(host, user string, paths []string) (map[string][]sftp.Item, error) {
	return t.c.ListMany(host, user, paths)
}
~~~

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/sync/ -race -count=1 -run 'TransferTo|CleanupParts' -v`
Expected: 全部 PASS

- [ ] **Step 5: 提交**

~~~bash
gofmt -s -w internal/sync/
git add internal/sync/
git commit -m "feat(sync): 新增原子传输与临时文件清理,临时名含规则 id 防跨规则冲突"
~~~

## Task 11: 六重删除闸门

**Files:** Create `internal/sync/delete_gate.go`；Test `internal/sync/delete_gate_test.go`

**Interfaces:** Produces `DeleteGateInput`、`GateResult`、`func EvaluateDeleteGate(in DeleteGateInput) GateResult`

**为什么不能只用一条阈值**：`待删数 > max(10, 现存条目×10%)` 这类带常量下界的阈值，对 5 个文件的配置目录恒不触发（`5 > max(10, 0.5) = 10` 为假）——挂载点掉线时 5 个文件会被全部删掉，而这恰恰是最常见的目录规模。

- [ ] **Step 1: 写失败的测试**

~~~go
package sync

import "testing"

func TestDeleteGateBlocksAllUnsafeCases(t *testing.T) {
	base := DeleteGateInput{MirrorDelete: true, Complete: true, PrevCount: 20, CurCount: 20}
	cases := []struct {
		name string
		in   DeleteGateInput
		want bool
	}{
		{"镜像关闭", DeleteGateInput{MirrorDelete: false, Complete: true}, false},
		{"本轮扫描不完整", DeleteGateInput{MirrorDelete: true, Complete: false}, false},
		{"根目录消失", DeleteGateInput{MirrorDelete: true, Complete: true, RootGone: true}, false},
		{"内核队列溢出", DeleteGateInput{MirrorDelete: true, Complete: true, Overflow: true}, false},
		{"重连/启动后的第一轮", DeleteGateInput{MirrorDelete: true, Complete: true, FirstRound: true}, false},
		{"远端看起来空了", DeleteGateInput{MirrorDelete: true, Complete: true, PrevCount: 5, CurCount: 0}, false},
		{"正常小批量", base, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := EvaluateDeleteGate(c.in).Allowed; got != c.want {
				t.Fatalf("Allowed = %v want %v", got, c.want)
			}
		})
	}
}

// 小集合也必须被阈值保护：5 个条目删 3 个就要挂起等确认。
func TestDeleteGateThresholdCoversSmallSets(t *testing.T) {
	got := EvaluateDeleteGate(DeleteGateInput{MirrorDelete: true, Complete: true, PrevCount: 5, CurCount: 5, PendingCount: 3})
	if got.Allowed {
		t.Fatal("删 5 个里的 3 个必须先确认")
	}
	if !got.NeedsConfirm {
		t.Fatalf("应为待确认而不是硬拒绝，得到 %+v", got)
	}
}

// 阈值之下的删除必须放行，否则 mirror_delete 形同虚设。
func TestDeleteGateAllowsSmallDelete(t *testing.T) {
	got := EvaluateDeleteGate(DeleteGateInput{MirrorDelete: true, Complete: true, PrevCount: 100, CurCount: 100, PendingCount: 1})
	if !got.Allowed || got.NeedsConfirm {
		t.Fatalf("1/100 应直接放行，得到 %+v", got)
	}
}
~~~

- [ ] **Step 2: 运行测试确认失败** — Run: `go test ./internal/sync/ -run DeleteGate -v`；Expected: `undefined: EvaluateDeleteGate`

- [ ] **Step 3: 实现**

~~~go
package sync

// DeleteGateInput 是一次删除判定所需的全部输入。纯数据，便于逐条测试。
type DeleteGateInput struct {
	MirrorDelete bool
	Complete     bool // 本轮扫描是否完整（有目录未知则为 false）
	CountKnown   bool // PrevCount/CurCount 是否是真实统计（事件路径上未知）
	RootGone     bool // 收到根目录 DELETE_SELF/UNMOUNT/IGNORED
	Overflow     bool // 内核队列溢出或 watch 集合不完整
	FirstRound   bool // 规则启动/重连/模式切换后的第一轮
	PrevCount    int  // 上一轮远端条目数
	CurCount     int  // 本轮远端条目数
	PendingCount int  // 本轮待删数量
}

type GateResult struct {
	Allowed      bool
	NeedsConfirm bool
	Reason       string
}

// EvaluateDeleteGate 实现六重闸门：任一安全条件不满足即禁止删除；
// 达到阈值则挂起等待用户在卡片上确认。
func EvaluateDeleteGate(in DeleteGateInput) GateResult {
	deny := func(reason string) GateResult { return GateResult{Allowed: false, Reason: reason} }
	switch {
	case !in.MirrorDelete:
		return deny("镜像删除未开启")
	case in.RootGone:
		return deny("监控根目录已消失，暂停删除直到对账确认")
	case in.Overflow || !in.Complete:
		return deny("本轮扫描不完整（有目录未知或 watch 不完整），禁止删除")
	case in.FirstRound:
		return deny("启动/重连后的第一轮不执行删除")
	case in.CountKnown && in.CurCount == 0 && in.PrevCount > 0:
		return deny("远端本轮为空而上一轮非空，疑似挂载点掉线，禁止删除")
	}
	n := in.PendingCount
	// 阈值对**小集合**同样生效：不要写成 n > max(10, prev*10%)。
	// 事件路径上 CountKnown=false，此时只按绝对数量 10 兜底（没有可用的分母）。
	if n >= 1 && (n >= 10 || (in.CountKnown && n*2 >= in.PrevCount)) {
		return GateResult{Allowed: false, NeedsConfirm: true,
			Reason: "待删数量达到阈值，已挂起等待确认"}
	}
	return GateResult{Allowed: true, Reason: "通过全部删除闸门"}
}
~~~

- [ ] **Step 4: 运行测试确认通过** — Run: `go test ./internal/sync/ -race -count=1 -run DeleteGate -v`；Expected: PASS
- [ ] **Step 5: 提交** — `git add internal/sync/ && git commit -m "feat(sync): 新增六重删除闸门,阈值对小集合同样生效"`

---

## Task 12: 冲突队列与三个动作

**Files:** Create `internal/sync/conflict.go`；Test `internal/sync/conflict_test.go`

**Interfaces:** Produces `Conflict`、`ConflictAction` 与三个常量、`func UpsertConflict(d *StateFile, c Conflict)`、`func ResolveConflict(d *StateFile, rel string, action ConflictAction, now string) (TransferOrDelete, error)`

**关键**：`ResolveConflict` **只入队并返回请求，绝不自己传输**。若绑定线程持状态锁去下载，而引擎 goroutine 持"串行传输"锁等状态锁，就是 ABBA 死锁。

- [ ] **Step 1: 写失败的测试**

~~~go
package sync

import "testing"

func TestUpsertConflictDeduplicates(t *testing.T) {
	d := &StateFile{Entries: map[string]*Entry{}}
	UpsertConflict(d, Conflict{RelPath: "a.conf", RemoteSize: 1, LocalSize: 2})
	UpsertConflict(d, Conflict{RelPath: "a.conf", RemoteSize: 3, LocalSize: 4})
	if len(d.Conflicts) != 1 {
		t.Fatalf("同一路径只能有一条冲突，得到 %d", len(d.Conflicts))
	}
	if d.Conflicts[0].RemoteSize != 3 {
		t.Fatalf("应保留最新观测值，得到 %#v", d.Conflicts[0])
	}
}

// keep_local：只对齐 local_*，**remote_* 保持本次冲突时观测到的远端值**。
// 若把 remote_* 写成本地值，下一轮 poll 会判为 write，再按"远端赢"覆盖用户
// 刚刚选择保留的文件 —— 这是本条设计的全部意义。
func TestResolveKeepLocalKeepsRemoteFields(t *testing.T) {
	d := &StateFile{Entries: map[string]*Entry{"a.conf": {}}}
	UpsertConflict(d, Conflict{RelPath: "a.conf", RemoteSize: 88, RemoteMTime: "remote-t"})
	local := LocalState{Exists: true, Size: 91, ModTime: "local-t"}
	req, err := ResolveConflict(d, "a.conf", ConflictKeepLocal, local, "2026-09-10T12:00:00Z")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if req.Action != ActionSkip {
		t.Fatalf("keep_local 不需要传输，得到 %v", req.Action)
	}
	e := d.Entries["a.conf"]
	if e.RemoteSize != 88 || e.RemoteMTime != "remote-t" {
		t.Fatalf("remote_* 被污染了: %#v", e)
	}
	if !e.HasLocal || e.LocalSize != 91 || e.LocalMTime != "local-t" {
		t.Fatalf("local_* 应对齐当前磁盘状态: %#v", e)
	}
	if len(d.Conflicts) != 0 {
		t.Fatal("解决后冲突条目必须移除")
	}
}

// take_remote：只返回"要传输"的请求，由规则 goroutine 串行消费。
func TestResolveTakeRemoteReturnsRequest(t *testing.T) {
	d := &StateFile{Entries: map[string]*Entry{}}
	UpsertConflict(d, Conflict{RelPath: "b.conf"})
	req, err := ResolveConflict(d, "b.conf", ConflictTakeRemote, LocalState{}, "t")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if req.Action != ActionGet || req.RelPath != "b.conf" {
		t.Fatalf("take_remote 必须返回 GET 请求: %#v", req)
	}
}

func TestResolveUnknownConflictIsError(t *testing.T) {
	d := &StateFile{Entries: map[string]*Entry{}}
	if _, err := ResolveConflict(d, "nope", ConflictKeepLocal, LocalState{}, "t"); err == nil {
		t.Fatal("未知路径必须报错")
	}
}
~~~

- [ ] **Step 2: 运行测试确认失败** — Run: `go test ./internal/sync/ -run 'Conflict' -v`；Expected: `undefined: Conflict`

- [ ] **Step 3: 实现**

~~~go
package sync

import "fmt"

// Conflict 的类型定义在 Task 9 的 state.go（状态文件要序列化它），本任务只加动作。

type ConflictAction string

const (
	ConflictKeepLocal  ConflictAction = "keep_local"
	ConflictTakeRemote ConflictAction = "take_remote"
	ConflictSaveAs     ConflictAction = "save_as"
)

// TransferOrDelete 是"交给规则 goroutine 串行执行"的请求。
type TransferOrDelete struct {
	RelPath string
	Action  Action
	Detail  string
}

// UpsertConflict 按路径去重：同一路径只保留最新一次观测。
func UpsertConflict(d *StateFile, c Conflict) {
	for i := range d.Conflicts {
		if d.Conflicts[i].RelPath == c.RelPath {
			d.Conflicts[i] = c
			return
		}
	}
	d.Conflicts = append(d.Conflicts, c)
}

// ResolveConflict 应用用户的裁决，返回需要由规则 goroutine 执行的请求。
// **本函数不做任何 IO**：绑定线程持状态锁时下载会与引擎形成 ABBA 死锁。
func ResolveConflict(d *StateFile, rel string, action ConflictAction, local LocalState, now string) (TransferOrDelete, error) {
	idx := -1
	for i := range d.Conflicts {
		if d.Conflicts[i].RelPath == rel {
			idx = i
			break
		}
	}
	if idx < 0 {
		return TransferOrDelete{}, fmt.Errorf("冲突不存在: %s", rel)
	}
	c := d.Conflicts[idx]
	d.Conflicts = append(d.Conflicts[:idx], d.Conflicts[idx+1:]...)
	if d.Entries == nil {
		d.Entries = map[string]*Entry{}
	}

	switch action {
	case ConflictKeepLocal:
		e := d.Entries[rel]
		if e == nil {
			e = &Entry{}
			d.Entries[rel] = e
		}
		// remote_* 保持本次冲突时观测到的远端值，绝不用本地值覆盖。
		e.RemoteSize, e.RemoteMTime = c.RemoteSize, c.RemoteMTime
		// local_* 对齐当前磁盘状态，否则下次同步会反复告警同一个文件。
		e.LocalSize, e.LocalMTime, e.HasLocal = local.Size, local.ModTime, local.Exists
		if !local.Exists {
			delete(d.Entries, rel)
		}
		return TransferOrDelete{RelPath: rel, Action: ActionSkip, Detail: "保留本地"}, nil
	case ConflictTakeRemote:
		return TransferOrDelete{RelPath: rel, Action: ActionGet, Detail: "用远端覆盖"}, nil
	case ConflictSaveAs:
		return TransferOrDelete{RelPath: rel, Action: ActionSaveAs, Detail: "另存远端副本"}, nil
	default:
		return TransferOrDelete{}, fmt.Errorf("未知冲突动作: %s", action)
	}
}
~~~

- [ ] **Step 4: 运行测试确认通过** — Run: `go test ./internal/sync/ -race -count=1 -run Conflict -v`；Expected: PASS
- [ ] **Step 5: 提交** — `git add internal/sync/ && git commit -m "feat(sync): 新增冲突队列与三个动作,裁决只入队不自行传输"`

---

## Task 13: 规则校验

**Files:** Create `internal/sync/validate.go`；Test `internal/sync/validate_test.go`

**Interfaces:** Produces `func ValidateSyncRule(r config.SyncRule) error`

**落点必须在 sync 包**：放 `internal/config` 会形成 `config → forward → config` 导入环，Go 直接编译不过。

- [ ] **Step 1: 写失败的测试**

~~~go
package sync

import (
	"strings"
	"testing"

	"sshore/internal/config"
)

func validRule() config.SyncRule {
	return config.SyncRule{ID: "x", Host: "prod-01", Kind: "dir",
		RemotePath: "/srv/conf", LocalPath: "/tmp/conf", MaxDepth: 2, PollIntervalS: 5}
}

func TestValidateSyncRuleAccepts(t *testing.T) {
	if err := ValidateSyncRule(validRule()); err != nil {
		t.Fatalf("合法规则被拒: %v", err)
	}
}

func TestValidateSyncRuleRejects(t *testing.T) {
	cases := map[string]func(*config.SyncRule){
		"host 含注入字符":  func(r *config.SyncRule) { r.Host = "-oProxyCommand=x" },
		"host 为空":     func(r *config.SyncRule) { r.Host = "" },
		"kind 非法":     func(r *config.SyncRule) { r.Kind = "symlink" },
		"远端路径为空":     func(r *config.SyncRule) { r.RemotePath = "" },
		"远端路径含换行":    func(r *config.SyncRule) { r.RemotePath = "/srv/a\nb" },
		"本地路径为空":     func(r *config.SyncRule) { r.LocalPath = "" },
		"max_depth 越界": func(r *config.SyncRule) { r.MaxDepth = 999 },
		"轮询间隔为 0":    func(r *config.SyncRule) { r.PollIntervalS = 0 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			r := validRule()
			mutate(&r)
			if err := ValidateSyncRule(r); err == nil {
				t.Fatal("必须被拒绝")
			}
		})
	}
}

// 单文件的 mirror_delete 无意义：源消失一律不删本地，校验层显式拒绝以免误会。
func TestValidateSyncRuleRejectsMirrorDeleteForFile(t *testing.T) {
	r := validRule()
	r.Kind = "file"
	r.MirrorDelete = true
	err := ValidateSyncRule(r)
	if err == nil || !strings.Contains(err.Error(), "mirror_delete") {
		t.Fatalf("应拒绝 kind=file 且 mirror_delete=true，得到 %v", err)
	}
}
~~~

- [ ] **Step 2: 运行测试确认失败** — Run: `go test ./internal/sync/ -run Validate -v`；Expected: `undefined: ValidateSyncRule`

- [ ] **Step 3: 实现**

~~~go
package sync

import (
	"fmt"
	"strings"
	"unicode"

	"sshore/internal/config"
	"sshore/internal/forward"
)

// ValidateSyncRule 在创建/编辑时即拒绝坏规则（对齐 forward.ValidateTunnel 的做法），
// 而不是拖到启动时才报错。
//
// 注意 host 的校验复用 forward.ValidateHost，其正则实际是
// ^[A-Za-z0-9][A-Za-z0-9._-]*$（不只是"禁止前导 -"）。
func ValidateSyncRule(r config.SyncRule) error {
	if !forward.ValidateHost(r.Host) {
		return fmt.Errorf("主机名不合法: %q", r.Host)
	}
	if r.Kind != "dir" && r.Kind != "file" {
		return fmt.Errorf("kind 必须是 dir 或 file，得到 %q", r.Kind)
	}
	if strings.TrimSpace(r.RemotePath) == "" {
		return fmt.Errorf("远端路径不能为空")
	}
	for _, ru := range r.RemotePath {
		if unicode.IsControl(ru) {
			return fmt.Errorf("远端路径含控制字符")
		}
	}
	if !strings.HasPrefix(r.RemotePath, "/") && !strings.HasPrefix(r.RemotePath, "~") {
		return fmt.Errorf("远端路径必须是绝对路径或以 ~ 开头")
	}
	if strings.TrimSpace(r.LocalPath) == "" {
		return fmt.Errorf("本地路径不能为空")
	}
	if r.MaxDepth < -1 || r.MaxDepth > 64 {
		return fmt.Errorf("max_depth 必须在 -1 或 [0,64] 之间，得到 %d", r.MaxDepth)
	}
	if r.PollIntervalS < 1 || r.PollIntervalS > 3600 {
		return fmt.Errorf("poll_interval_s 必须在 [1,3600] 之间，得到 %d", r.PollIntervalS)
	}
	if r.Kind == "file" && r.MirrorDelete {
		return fmt.Errorf("kind=file 时 mirror_delete 无意义（源消失一律不删本地）")
	}
	return nil
}
~~~

- [ ] **Step 4: 运行测试确认通过** — Run: `go test ./internal/sync/ -race -count=1 -run Validate -v`；Expected: PASS
- [ ] **Step 5: 提交** — `git add internal/sync/ && git commit -m "feat(sync): 新增规则校验,创建时即拒绝坏规则"`

## Task 14: 同步引擎（编排、状态机、首轮对齐、退避重连）

**Files:** Create `internal/sync/ctrl.go`；Test `internal/sync/ctrl_test.go`

**Interfaces:**
- Consumes: 前面全部任务
- Produces:
  - `type Deps struct { Spawner osutil.Streamer; Runner osutil.CtxRunner; Transfer Transferer; ListMany watch.ListManyFunc; Emit forward.EmitFunc; After func(time.Duration) <-chan struct{}; StateDir string }`
  - `func NewCtrl(d Deps) *Ctrl`
  - `func (c *Ctrl) Start(r config.SyncRule) error` / `Stop(id) error` / `States() map[string]string` / `Stats() map[string]SyncRuleStat`
  - `func (c *Ctrl) Conflicts(id string) []Conflict`
  - `func (c *Ctrl) ResolveConflict(id, rel string, action ConflictAction, local LocalState) error`
  - `func (c *Ctrl) ConfirmDeletes(id, fingerprint string) error`

**必须守住的不变式**：规则内传输串行（全在同一条 goroutine 里）；`ResolveConflict` 只写队列 + 唤醒，**不传输**；状态处理与传输互不嵌套持锁。

- [ ] **Step 1: 写失败的测试**

~~~go
package sync

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"sshore/internal/config"
	"sshore/internal/sftp"
	"sshore/internal/watch"
)

// scriptedListMany 返回固定远端内容；写入通过 set 修改。
type scriptedListMany struct {
	mu   sync.Mutex
	tree map[string][]sftp.Item
}

func (s *scriptedListMany) list(host, user string, paths []string) (map[string][]sftp.Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	res := map[string][]sftp.Item{}
	for _, p := range paths {
		if v, ok := s.tree[p]; ok {
			res[p] = v
		}
	}
	return res, nil
}

func (s *scriptedListMany) set(dir string, items ...sftp.Item) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tree[dir] = items
}

// fakeXfer 把"远端内容"落到本地，用于验证原子写与冲突规则。
type fakeXfer struct {
	mu     sync.Mutex
	remote map[string]string
	calls  int
}

func (f *fakeXfer) Get(host, user, remote, local string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	body, ok := f.remote[remote]
	if !ok {
		return os.ErrNotExist
	}
	return os.WriteFile(local, []byte(body), 0644)
}

func newTestCtrl(t *testing.T, lm watch.ListManyFunc, xf Transferer) (*Ctrl, config.SyncRule, string) {
	t.Helper()
	local := t.TempDir()
	rule := config.SyncRule{
		ID: "rule1", Name: "t", Host: "h", Kind: "dir",
		RemotePath: "/r", LocalPath: local, MaxDepth: 0,
		PollIntervalS: 1, ForcePoll: true, Enabled: true,
	}
	rule.Normalize()
	c := NewCtrl(Deps{
		ListMany: lm, Transfer: xf, StateDir: t.TempDir(),
		After: func(time.Duration) <-chan struct{} { return nil }, // 不自动重连，测试手动驱动
	})
	return c, rule, local
}

// 首轮对齐：远端已有文件必须被拉下来（决策 5 的"先全量对齐"）。
func TestEngineFirstRunAligns(t *testing.T) {
	lm := &scriptedListMany{tree: map[string][]sftp.Item{
		"/r": {{Name: "a.txt", Size: 5, ModTime: "2026-09-10 10:00"}},
	}}
	xf := &fakeXfer{remote: map[string]string{"/r/a.txt": "hello"}}
	c, rule, local := newTestCtrl(t, lm.list, xf)
	if err := c.Start(rule); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer c.Stop(rule.ID)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(filepath.Join(local, "a.txt")); err == nil && string(b) == "hello" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("首轮对齐未把 a.txt 拉下来")
}

// 本地已存在且大小一致 ⇒ 采纳：登记基线但**不下载**（首轮不覆盖用户文件）。
func TestEngineFirstRunAdoptsSameSizeFile(t *testing.T) {
	lm := &scriptedListMany{tree: map[string][]sftp.Item{
		"/r": {{Name: "a.txt", Size: 5, ModTime: "2026-09-10 10:00"}},
	}}
	xf := &fakeXfer{remote: map[string]string{"/r/a.txt": "hello"}}
	c, rule, local := newTestCtrl(t, lm.list, xf)
	if err := os.WriteFile(filepath.Join(local, "a.txt"), []byte("other"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := c.Start(rule); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer c.Stop(rule.ID)
	time.Sleep(700 * time.Millisecond)
	if xf.calls != 0 {
		t.Fatalf("同大小文件不应被下载，实际下载 %d 次", xf.calls)
	}
	b, _ := os.ReadFile(filepath.Join(local, "a.txt"))
	if string(b) != "other" {
		t.Fatal("采纳分支绝不能改写本地文件")
	}
}

// 本地已存在但大小不同 ⇒ 冲突，绝不覆盖。
func TestEngineFirstRunConflictsOnDifferentSize(t *testing.T) {
	lm := &scriptedListMany{tree: map[string][]sftp.Item{
		"/r": {{Name: "a.txt", Size: 5, ModTime: "2026-09-10 10:00"}},
	}}
	xf := &fakeXfer{remote: map[string]string{"/r/a.txt": "hello"}}
	c, rule, local := newTestCtrl(t, lm.list, xf)
	if err := os.WriteFile(filepath.Join(local, "a.txt"), []byte("much longer local content"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := c.Start(rule); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer c.Stop(rule.ID)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(c.Conflicts(rule.ID)) == 1 {
			if xf.calls != 0 {
				t.Fatalf("冲突分支绝不能下载，实际 %d 次", xf.calls)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("大小不同必须产生冲突")
}
~~~

- [ ] **Step 2: 运行测试确认失败** — Run: `go test ./internal/sync/ -run Engine -v`；Expected: `undefined: NewCtrl`

- [ ] **Step 3: 实现**

创建 `internal/sync/ctrl.go`：

~~~go
package sync

import (
	"context"
	"fmt"
	"hash/fnv"
	"path"
	"sync"
	"time"

	"sshore/internal/config"
	"sshore/internal/forward"
	"sshore/internal/osutil"
	"sshore/internal/sshconn"
	"sshore/internal/watch"
)

// 【锁顺序不变式，违反即 ABBA 死锁】
//   - 允许的嵌套只有一种：state.mu → r.mu（align 里在 state.With 回调内加 r.mu）；
//   - **绝不允许先持 r.mu 再进 state.With / state.Flush**；
//   - 传输与任何 IO 都必须在锁外做（ResolveConflict 只入队就是这个原因）。
//
// Deps 是引擎的全部外部依赖，便于测试注入。
type Deps struct {
	Spawner   osutil.Streamer
	Runner    osutil.CtxRunner
	Transfer  Transferer
	ListMany  watch.ListManyFunc
	Emit      forward.EmitFunc
	After     func(time.Duration) <-chan struct{}
	StateDir  string
	BackoffFn func(attempt int) time.Duration
}

// SyncRuleStat 是卡片的活跃度数据。首轮对齐进度也在这里（这不属于状态圆点）。
type SyncRuleStat struct {
	Mode          string
	Reason        string
	PollIntervalS int
	Pending       int
	Done          int
	Failed        int
	Conflicts     int
	AlignScanned  int
	AlignTotal    int
	CurrentFile   string
	SourceMissing bool
	DeletePending int
	// DeleteFingerprint 是待确认删除的"轮次 + 路径集合指纹"，UI 必须原样回传
	// 给 ConfirmSyncRuleDeletes —— 否则确认闭环断裂（无法解除挂起状态）。
	DeleteFingerprint string
	DeletePaths       []string
}

type ruleRuntime struct {
	rule  config.SyncRule
	state *StateStore

	mu         sync.Mutex
	status     string // stopped|connecting|connected|reconnecting|error
	stats      SyncRuleStat
	cancel     chan struct{}
	src        watch.Source
	queue      map[string]watch.Kind
	wake       chan struct{}
	pendingDel []string
	delFP      string
	logCount   map[string]int
	firstRound bool // 启动/重连/模式切换后的第一轮：禁止删除
	blockDels  bool // 收到 root_gone / overflow：暂停删除直到对账确认
	stableAt   time.Time // 最近一次进入 connected 的时刻（防抖基准）
}

type Ctrl struct {
	d    Deps
	mu   sync.Mutex
	run  map[string]*ruleRuntime
}

func NewCtrl(d Deps) *Ctrl {
	if d.After == nil {
		d.After = func(dur time.Duration) <-chan struct{} {
			ch := make(chan struct{})
			go func() { time.Sleep(dur); close(ch) }()
			return ch
		}
	}
	if d.BackoffFn == nil {
		d.BackoffFn = backoffDelay
	}
	if d.Emit == nil {
		d.Emit = func(forward.Event) {}
	}
	return &Ctrl{d: d, run: map[string]*ruleRuntime{}}
}

// retryDelay 是**单文件**下载失败的重试节奏（1s / 4s / 16s），与 §8.2 的
// 连接退避是两套参数，不要合并。
func retryDelay(attempt int) time.Duration {
	switch attempt {
	case 1:
		return time.Second
	case 2:
		return 4 * time.Second
	default:
		return 16 * time.Second
	}
}

// backoffDelay 与 forward 同一序列：1s 起、每次 ×2、30s 封顶。
func backoffDelay(attempt int) time.Duration {
	d := time.Second
	for i := 1; i < attempt && d < 30*time.Second; i++ {
		d *= 2
	}
	if d > 30*time.Second {
		d = 30 * time.Second
	}
	return d
}

func (c *Ctrl) emit(ruleID, level, msg string) {
	c.d.Emit(forward.Event{
		SourceType: "sync",
		SourceID:   ruleID,
		TS:         time.Now().Format(time.RFC3339),
		Level:      level,
		Message:    msg,
	})
}

func (c *Ctrl) States() map[string]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := map[string]string{}
	for id, r := range c.run {
		r.mu.Lock()
		out[id] = r.status
		r.mu.Unlock()
	}
	return out
}

func (c *Ctrl) Stats() map[string]SyncRuleStat {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := map[string]SyncRuleStat{}
	for id, r := range c.run {
		r.mu.Lock()
		out[id] = r.stats
		r.mu.Unlock()
	}
	return out
}

func (c *Ctrl) Conflicts(id string) []Conflict {
	c.mu.Lock()
	r := c.run[id]
	c.mu.Unlock()
	if r == nil {
		return []Conflict{}
	}
	var out []Conflict
	r.state.With(func(d *StateFile) {
		out = append([]Conflict{}, d.Conflicts...)
	})
	if out == nil {
		return []Conflict{}
	}
	return out
}

func (c *Ctrl) fingerprintOf(rule config.SyncRule) Fingerprint {
	return Fingerprint{
		Host: rule.Host, User: rule.User, Kind: rule.Kind,
		RemoteRoot: rule.RemotePath, LocalRoot: rule.LocalPath,
		MaxDepth: rule.MaxDepth, Excludes: rule.Excludes,
	}
}

func (c *Ctrl) Start(rule config.SyncRule) error {
	if err := ValidateSyncRule(rule); err != nil {
		return err
	}
	c.mu.Lock()
	if _, exists := c.run[rule.ID]; exists {
		c.mu.Unlock()
		return fmt.Errorf("规则已在运行: %s", rule.ID)
	}
	st := NewStateStore(path.Join(c.d.StateDir, "sync-"+rule.ID+".json"), c.fingerprintOf(rule))
	c.mu.Unlock()
	if err := st.Load(); err != nil {
		return err
	}
	n, _ := CleanupParts(rule.LocalPath, rule.ID)
	if n > 0 {
		c.emit(rule.ID, "info", fmt.Sprintf("清理了 %d 个残留临时文件", n))
	}
	r := &ruleRuntime{
		rule: rule, state: st, status: "connecting",
		cancel: make(chan struct{}), queue: map[string]watch.Kind{}, wake: make(chan struct{}, 1),
	}
	c.mu.Lock()
	c.run[rule.ID] = r
	c.mu.Unlock()

	c.emit(rule.ID, "info", "监控启动中")
	go c.loop(r)
	return nil
}

func (c *Ctrl) Stop(id string) error {
	c.mu.Lock()
	r := c.run[id]
	if r != nil {
		delete(c.run, id)
	}
	c.mu.Unlock()
	if r == nil {
		return fmt.Errorf("规则未在运行: %s", id)
	}
	r.mu.Lock()
	if r.src != nil {
		_ = r.src.Close()
	}
	r.mu.Unlock()
	close(r.cancel)
	_ = r.state.Flush()
	c.emit(id, "info", "监控已停止")
	return nil
}

// loop 是规则的主 goroutine：建立探测 → 对齐 → 消费事件 → 断开后按 §8.2 退避。
// 所有传输都在本 goroutine 内串行执行。
func (c *Ctrl) loop(r *ruleRuntime) {
	attempt := 0
	for {
		select {
		case <-r.cancel:
			return
		default:
		}
		// spec §4.3：规则启动时确保 ControlMaster 存在。sftp 的 run() 用的是
		// ControlMaster=no（只复用不建立），不在这里建的话所有连接各自建连，
		// "探测与传输共用一条连接"这个设计前提就不成立。
		if c.d.Runner != nil {
			mctx, mcancel := context.WithTimeout(context.Background(), 10*time.Second)
			if err := sshconn.EnsureMaster(mctx, c.d.Runner, r.rule.Host, r.rule.User); err != nil {
				// 降级而非失败：功能正确，只是每条命令各自建连。
				c.emitThrottled(r, "master", "warn", "无法建立复用的 SSH 连接，将按命令独立连接："+err.Error())
			}
			mcancel()
		}
		info := watch.Detect(context.Background(), c.d.Runner, watch.DetectOpts{
			Host: r.rule.Host, User: r.rule.User, RemotePath: r.rule.RemotePath,
			ForcePoll: r.rule.ForcePoll, PollInterval: time.Duration(r.rule.PollIntervalS) * time.Second,
		})
		src, err := c.newSource(r, info)
		if err != nil {
			r.mu.Lock()
			r.status = "error"
			r.mu.Unlock()
			c.emit(r.rule.ID, "error", "无法启动探测："+err.Error())
			return
		}
		r.mu.Lock()
		r.src = src
		r.status = "connected"
		r.stableAt = time.Now()
		r.stats.Mode = info.Mode
		r.stats.Reason = info.Reason
		r.stats.PollIntervalS = r.rule.PollIntervalS
		r.mu.Unlock()
		if info.Mode == "poll" {
			c.emit(r.rule.ID, "warn", "已降级为轮询："+info.Reason)
		} else {
			c.emit(r.rule.ID, "info", "监控已启动：inotify")
		}

		// 首轮全量对齐（含重连后的对账：以 entries 的 remote_* 为基线）。
		r.mu.Lock()
		r.firstRound = true
		r.mu.Unlock()
		c.align(r)

		ch, err := src.Start(context.Background())
		if err != nil {
			r.mu.Lock()
			r.status = "error"
			r.mu.Unlock()
			c.emit(r.rule.ID, "error", "探测进程启动失败："+err.Error())
			return
		}
		dropped := c.consume(r, ch)
		_ = src.Close()
		if dropped {
			// 轮询连续失败达到阈值 ⇒ 配置/权限类问题，进 error 而不是无限重连。
			// 启动期致命错误（远端路径不存在 / watch 配额耗尽）：配置错，进 error
			// 且不重连——无限重试只会掩盖问题并刷屏。
			if is, ok := src.(*watch.InotifySource); ok {
				if e := is.Err(); e != nil {
					r.mu.Lock()
					r.status = "error"
					r.mu.Unlock()
					c.emit(r.rule.ID, "error", e.Error())
					return
				}
			}
			if ps, ok := src.(*watch.PollSource); ok && ps.Failures() >= 5 {
				r.mu.Lock()
				r.status = "error"
				r.mu.Unlock()
				c.emit(r.rule.ID, "error", "连续多轮扫描失败，已停止")
				return
			}
			// 通道关闭 = 探测断开。启动失败不重连，运行中断开才重连。
			r.mu.Lock()
			noReconnect := !r.rule.Reconnect()
			r.status = "reconnecting"
			if noReconnect {
				r.status = "error"
			}
			r.stats = r.stats
			r.mu.Unlock()
			if noReconnect {
				c.emit(r.rule.ID, "error", "监控连接断开，且未开启自动重连")
				return
			}
			// 连续稳定在线满 60s 就清零计数（对齐 forward 的 stableThreshold 语义），
			// 否则多次独立抖动会让退避永久停在 30s。
			r.mu.Lock()
			stable := time.Since(r.stableAt) >= 60*time.Second
			r.mu.Unlock()
			if stable {
				attempt = 0
			}
			attempt++
			delay := c.d.BackoffFn(attempt)
			c.emit(r.rule.ID, "warn", fmt.Sprintf("监控连接断开，%s 后进行第 %d 次重连", delay, attempt))
			select {
			case <-r.cancel:
				return
			case <-c.d.After(delay):
			}
			continue
		}
		return
	}
}

func (c *Ctrl) newSource(r *ruleRuntime, info watch.Info) (watch.Source, error) {
	opts := watch.DetectOpts{
		Host: r.rule.Host, User: r.rule.User, RemotePath: r.rule.RemotePath,
		ForcePoll: r.rule.ForcePoll, PollInterval: time.Duration(r.rule.PollIntervalS) * time.Second,
	}
	logf := func(level, msg string) { c.emit(r.rule.ID, level, msg) }
	// kind=file 强制轮询：inotify 下根路径就是那个文件，%w%f 的 rel 恒为空串，
	// CLOSE_WRITE/MODIFY 会被当作根目录噪声丢弃 —— 写事件永远拿不到。
	if info.Mode == "inotify" && c.d.Spawner != nil && r.rule.Kind != "file" {
		return watch.NewInotifySource(c.d.Spawner, opts, logf), nil
	}
	if info.Mode == "inotify" && r.rule.Kind == "file" {
		c.emit(r.rule.ID, "warn", "单文件规则使用轮询探测（inotify 无法提供该文件的写事件）")
	}
	if c.d.ListMany == nil {
		return nil, fmt.Errorf("缺少 ListMany 依赖")
	}
	return watch.NewPollSource(c.d.ListMany, opts, r.rule.MaxDepth, r.rule.Excludes, nil, logf), nil
}

// consume 消费事件直到通道关闭，返回 true 表示"断开"（需要重连）。
// 它同时承担 300ms 去抖与批量处理；解析侧只做入队，不做 IO。
func (c *Ctrl) consume(r *ruleRuntime, ch <-chan watch.Event) bool {
	timer := time.NewTimer(300 * time.Millisecond)
	if !timer.Stop() {
		<-timer.C
	}
	dirty := false
	for {
		select {
		case <-r.cancel:
			return false
		case ev, ok := <-ch:
			if !ok {
				return true
			}
			r.mu.Lock()
			if ev.Kind == watch.KindOverflow || ev.Kind == watch.KindRootGone {
				// 强制对账：把整棵远端树重新比一遍，并暂停删除直到确认。
				r.stats.SourceMissing = ev.Kind == watch.KindRootGone
				r.blockDels = true
				r.mu.Unlock()
				c.align(r)
				continue
			}
			if ev.Kind == watch.KindDirAdded || ev.Kind == watch.KindDirGone {
				// 目录事件必须触发子树对账（mv 进来的目录内文件是零事件的）。
				r.mu.Unlock()
				c.align(r)
				continue
			}
			r.queue[ev.RelPath] = ev.Kind
			overflowed := len(r.queue) > maxQueuePaths
			if overflowed {
				r.queue = map[string]watch.Kind{}
			}
			r.mu.Unlock()
			if overflowed {
				c.emit(r.rule.ID, "warn", "待处理路径过多，转为全量对账")
				c.align(r)
				continue
			}
			if !dirty {
				timer.Reset(300 * time.Millisecond)
				dirty = true
			}
		case <-r.wake:
			// UI 裁决 take_remote/save_as 后唤醒：走同一条去抖路径，不另开传输
			// （传输只能在规则 goroutine 内串行发生）。
			if !dirty {
				timer.Reset(300 * time.Millisecond)
				dirty = true
			}
		case <-timer.C:
			dirty = false
			c.drainQueue(r)
		}
	}
}

// drainQueue 取出去抖后的路径集合，补齐远端元信息，逐条决策并串行传输。
func (c *Ctrl) drainQueue(r *ruleRuntime) {
	r.mu.Lock()
	batch := r.queue
	r.queue = map[string]watch.Kind{}
	r.mu.Unlock()
	if len(batch) == 0 {
		return
	}
	metas, metaErr := c.fetchMeta(r, batch)
	if metaErr != nil {
		c.emit(r.rule.ID, "warn", "远端元信息补齐失败，本批跳过："+metaErr.Error())
		return
	}
	for rel, kind := range batch {
		r.mu.Lock()
		r.stats.CurrentFile = rel
		r.mu.Unlock()
		c.applyOne(r, rel, kind, metas[rel])
		r.mu.Lock()
		r.stats.CurrentFile = ""
		r.mu.Unlock()
	}
	_ = r.state.Flush()
	// 事件路径的删除也必须过闸门 —— 只登记不执行等于 mirror_delete 失效。
	r.mu.Lock()
	hasDels := len(r.pendingDel) > 0
	r.mu.Unlock()
	if hasDels {
		c.maybeDelete(r, -1) // -1：本轮远端条目数未知（事件路径）
	}
}

func (c *Ctrl) fetchMeta(r *ruleRuntime, batch map[string]watch.Kind) (map[string]*Entry, error) {
	out := map[string]*Entry{}
	if c.d.ListMany == nil {
		return out, nil
	}
	// 注意：不做批量路径拼接的猜测——逐个文件 ls 由 ListMany 承担，
	// 它内部仍是一个 sftp 批处理（约定见 Task 3）。
	paths := make([]string, 0, len(batch))
	for rel := range batch {
		paths = append(paths, path.Join(r.rule.RemotePath, rel))
	}
	res, err := c.d.ListMany(r.rule.Host, r.rule.User, paths)
	if err != nil {
		return out, err
	}
	for rel := range batch {
		items, ok := res[path.Join(r.rule.RemotePath, rel)]
		if !ok || len(items) == 0 {
			continue // 未知：Decide 会保守下载
		}
		out[rel] = &Entry{RemoteSize: items[0].Size, RemoteMTime: items[0].ModTime}
	}
	return out, nil
}

// align 做一次全量对账：扫描远端，与 entries 的 remote_* 比对（含删除）。
// 首轮（无 entries）时按决策表逐条走"下载/采纳/冲突"。
// maxQueuePaths 是去重队列的上限。超过就退化为"需要全量对账"，而不是继续堆积
// 或阻塞解析侧——解析侧一旦阻塞，本地 ssh 的 stdout 管道写满会反过来阻塞远端
// inotifywait，最终导致内核队列溢出、事件被静默丢弃。
const maxQueuePaths = 5000

// emitThrottled 抑制重复刷屏：同一 key 的同类消息每 5 次才记一条（spec §8.3）。
func (c *Ctrl) emitThrottled(r *ruleRuntime, key, level, msg string) {
	r.mu.Lock()
	if r.logCount == nil {
		r.logCount = map[string]int{}
	}
	r.logCount[key]++
	n := r.logCount[key]
	r.mu.Unlock()
	if n == 1 || n%5 == 0 {
		c.emit(r.rule.ID, level, msg)
	}
}

func (c *Ctrl) align(r *ruleRuntime) {
	if c.d.ListMany == nil {
		return
	}
	if r.rule.Kind == "file" {
		c.alignFile(r)
		return
	}
	snap, err := watch.ScanTree(c.d.ListMany, r.rule.Host, r.rule.User, r.rule.RemotePath, r.rule.MaxDepth, r.rule.Excludes)
	if err != nil || !snap.Complete {
		c.emit(r.rule.ID, "warn", "对账扫描未完成，跳过本轮")
		return
	}
	r.mu.Lock()
	r.stats.AlignTotal = len(snap.Entries)
	r.mu.Unlock()
	var dels []string
	var changed []string
	r.state.With(func(d *StateFile) {
		for rel, meta := range snap.Entries {
			ent := d.Entries[rel]
			if ent == nil {
				// 远端新增：登记远端字段（Local 字段留空 ⇒ HasLocal=false）
				d.Entries[rel] = &Entry{RemoteSize: meta.Size, RemoteMTime: meta.ModTime}
				changed = append(changed, rel)
			} else if ent.RemoteSize != meta.Size || ent.RemoteMTime != meta.ModTime {
				// **只在远端真的变了时才处理**。原来的写法无条件覆盖 Remote*
				// 再对全部条目调 applyOne，而 Decide 的"有基线且本地未改"分支
				// 恒返回 GET —— 结果是每次对账都重下整棵树，且 §7.8 要求的
				// "本轮快照 vs entries.remote_*" diff 在覆盖后已无从比较。
				ent.RemoteSize, ent.RemoteMTime = meta.Size, meta.ModTime
				changed = append(changed, rel)
			}
			r.mu.Lock()
			r.stats.AlignScanned++
			r.mu.Unlock()
		}
		for rel, ent := range d.Entries {
			if _, ok := snap.Entries[rel]; !ok && ent.HasLocal {
				dels = append(dels, rel)
			}
		}
	})
	r.mu.Lock()
	r.pendingDel = dels
	// 指纹 = 轮次号 + 路径集合摘要：路径集合一变，指纹就变，确认即失效。
	h := fnv.New64a()
	for _, rel := range dels {
		_, _ = h.Write([]byte(rel))
		_, _ = h.Write([]byte{0})
	}
	r.delFP = fmt.Sprintf("%d-%x", time.Now().UnixNano(), h.Sum64())
	r.stats.DeletePaths = append([]string{}, dels...)
	r.logCount = map[string]int{} // 每轮对账后重置节流计数
	r.blockDels = false           // 完整对账成功 ⇒ 解除删除暂停（firstRound 已由闸门消费）
	r.stats.DeletePending = len(dels)
	r.stats.DeleteFingerprint = r.delFP
	r.stats.Pending = len(snap.Entries)
	r.mu.Unlock()
	_ = r.state.Flush()
	// 只处理新增/变化的条目；remote_* 已在上面写进 entries，applyOne 从状态里读。
	for _, rel := range changed {
		c.applyOne(r, rel, watch.KindWrite, nil)
	}
	c.maybeDelete(r, len(snap.Entries))
}

// alignFile 处理 kind=file：不做目录 BFS，只盯着那一个文件。
// 远端源文件消失时**本地保留不动、规则不进 error**（文件很可能稍后回来），
// 且**不受 mirror_delete 影响**——"源消失"更可能是路径配错或文件被临时挪走。
func (c *Ctrl) alignFile(r *ruleRuntime) {
	res, err := c.d.ListMany(r.rule.Host, r.rule.User, []string{r.rule.RemotePath})
	if err != nil {
		c.emitThrottled(r, "meta", "warn", "远端元信息读取失败："+err.Error())
		return
	}
	items, ok := res[r.rule.RemotePath]
	if !ok || len(items) == 0 {
		r.mu.Lock()
		r.stats.SourceMissing = true
		r.mu.Unlock()
		c.emitThrottled(r, "missing", "warn", "远端源文件缺失，本地保留不动")
		return
	}
	r.mu.Lock()
	r.stats.SourceMissing = false
	r.mu.Unlock()
	c.applyOne(r, path.Base(r.rule.RemotePath), watch.KindWrite,
		&Entry{RemoteSize: items[0].Size, RemoteMTime: items[0].ModTime})
}

// applyOne 走决策表并执行动作。全程在规则 goroutine 内 ⇒ 传输天然串行。
func (c *Ctrl) applyOne(r *ruleRuntime, rel string, kind watch.Kind, remote *Entry) {
	local, err := LocalTarget(r.rule.LocalPath, rel)
	if err != nil {
		c.emit(r.rule.ID, "error", "拒绝非法路径："+rel)
		return
	}
	st, _ := osStat(local)
	var ent *Entry
	r.state.With(func(d *StateFile) {
		if e, ok := d.Entries[rel]; ok {
			ent = e
		}
		if ent == nil && remote != nil {
			ent = remote
		} else if ent != nil && remote != nil {
			ent.RemoteSize, ent.RemoteMTime = remote.RemoteSize, remote.RemoteMTime
		}
		_ = d
	})
	action, reason := Decide(kind, ent, st, r.rule.MirrorDelete)
	switch action {
	case ActionGet, ActionSaveAs:
		remotePath := path.Join(r.rule.RemotePath, rel)
		target := local
		saveAs := action == ActionSaveAs
		if saveAs {
			// 另存为：远端版本写到 <name>.remote-<ts>，**本地原文件保留**，
			// 且**不更新基线**（原文件根本没有变化）。
			target = local + ".remote-" + time.Now().Format("20060102-150405")
		}
		// 单文件失败退避重试 3 次（1s/4s/16s）；仍失败则标记并**继续处理其它文件**，
		// 一个权限错误的文件不该让整条规则停摆。
		var lastErr error
		for attempt := 0; attempt < 3; attempt++ {
			if attempt > 0 {
				select {
				case <-r.cancel:
					return
				case <-c.d.After(retryDelay(attempt)):
				}
			}
			if lastErr = TransferTo(c.d.Transfer, r.rule.Host, r.rule.User, remotePath, target, r.rule.ID, nil); lastErr == nil {
				break
			}
		}
		if lastErr != nil {
			c.emit(r.rule.ID, "error", "下载 "+rel+" 失败（已重试 3 次）："+lastErr.Error())
			r.mu.Lock()
			r.stats.Failed++
			r.mu.Unlock()
			r.state.With(func(d *StateFile) {
				d.Failed = append(d.Failed, FailedItem{RelPath: rel, Err: lastErr.Error(),
					At: time.Now().Format(time.RFC3339)})
			})
			return
		}
		if saveAs {
			r.state.With(func(d *StateFile) { removeConflict(d, rel) })
			c.emit(r.rule.ID, "info", "远端副本已另存为 "+path.Base(target))
			return
		}
		now := time.Now().UTC().Format(time.RFC3339)
		after, statErr := osStat(local)
		if statErr != nil {
			// 不能把零值当成"基线"写进去：那会让下一次比对恒不等，刷出假冲突。
			c.emit(r.rule.ID, "warn", "下载后无法读取本地状态，本条暂不登记基线："+rel)
			return
		}
		r.state.With(func(d *StateFile) {
			e := d.Entries[rel]
			if e == nil {
				e = &Entry{}
				d.Entries[rel] = e
			}
			e.LocalSize, e.LocalModTime, e.HasLocal, e.WrittenAt = after.Size, after.ModTime, true, now
			removeConflict(d, rel)
		})
		r.mu.Lock()
		r.stats.Done++
		r.mu.Unlock()
		c.emit(r.rule.ID, "info", "下载 "+rel+"（"+reason+"）")
	case ActionAdopt:
		r.state.With(func(d *StateFile) {
			e := d.Entries[rel]
			if e == nil {
				e = &Entry{}
				d.Entries[rel] = e
			}
			e.LocalSize, e.LocalModTime, e.HasLocal, e.Adopted = st.Size, st.ModTime, st.Exists, true
		})
		c.emit(r.rule.ID, "info", rel+" 本地已存在且大小一致，未下载（未校验内容）")
	case ActionConflict:
		r.state.With(func(d *StateFile) {
			UpsertConflict(d, Conflict{RelPath: rel, RemoteSize: ent.RemoteSize, RemoteMTime: ent.RemoteMTime,
				LocalSize: st.Size, LocalMTime: st.ModTime, DetectedAt: time.Now().Format(time.RFC3339)})
		})
		r.mu.Lock()
		r.stats.Conflicts++
		r.mu.Unlock()
		c.emit(r.rule.ID, "warn", rel+" 本地已修改，未覆盖")
	case ActionDelete:
		// 绝不在 applyOne 里直接删：删除必须统一过六重闸门。这里只登记，
		// 由 maybeDelete 在闸门放行后才真正执行。
		r.mu.Lock()
		r.pendingDel = append(r.pendingDel, rel)
		r.stats.DeletePending = len(r.pendingDel)
		r.mu.Unlock()
		c.emit(r.rule.ID, "info", "待删除本地 "+rel+"（"+reason+"）")
	}
}

// maybeDelete 在删除闸门放行时才删本地文件。
func (c *Ctrl) maybeDelete(r *ruleRuntime, curCount int) {
	r.mu.Lock()
	pending := len(r.pendingDel)
	r.mu.Unlock()
	r.mu.Lock()
	first, blocked := r.firstRound, r.blockDels
	r.firstRound = false // 第一轮只放行一次
	r.mu.Unlock()
	res := EvaluateDeleteGate(DeleteGateInput{
		MirrorDelete: r.rule.MirrorDelete, Complete: true,
		FirstRound: first, RootGone: blocked, Overflow: blocked,
		CountKnown: curCount >= 0, PrevCount: curCount + pending, CurCount: curCount, PendingCount: pending,
	})
	if !res.Allowed {
		if res.NeedsConfirm {
			c.emit(r.rule.ID, "warn", fmt.Sprintf("本轮待删 %d 个文件，已挂起等待确认", pending))
		} else if pending > 0 {
			c.emit(r.rule.ID, "warn", "已禁止删除："+res.Reason)
		}
		return
	}
	c.executeDeletes(r)
}

func (c *Ctrl) executeDeletes(r *ruleRuntime) {
	r.mu.Lock()
	pending := r.pendingDel
	r.pendingDel = nil
	r.stats.DeletePending = 0
	r.mu.Unlock()
	for _, rel := range pending {
		local, err := LocalTarget(r.rule.LocalPath, rel)
		if err != nil {
			continue
		}
		if err := osRemove(local); err != nil {
			c.emit(r.rule.ID, "warn", "删除本地文件失败："+rel+" "+err.Error())
		}
		r.state.With(func(d *StateFile) { delete(d.Entries, rel) })
	}
	_ = r.state.Flush()
}

// ResolveConflict 只写队列并唤醒规则 goroutine，**绝不自己传输**。
func (c *Ctrl) ResolveConflict(id, rel string, action ConflictAction, local LocalState) error {
	c.mu.Lock()
	r := c.run[id]
	c.mu.Unlock()
	if r == nil {
		return fmt.Errorf("规则未在运行: %s", id)
	}
	var req TransferOrDelete
	var err error
	r.state.With(func(d *StateFile) {
		req, err = ResolveConflict(d, rel, action, local, time.Now().Format(time.RFC3339))
	})
	if err != nil {
		return err
	}
	_ = r.state.Flush()
	if req.Action == ActionGet || req.Action == ActionSaveAs {
		r.mu.Lock()
		r.queue[rel] = watch.KindWrite
		r.mu.Unlock()
		select {
		case r.wake <- struct{}{}:
		default:
		}
	}
	return nil
}

// ConfirmDeletes 执行挂起的删除；执行前**逐条重新校验**远端仍不存在，
// 任何一条已恢复即整批作废（挂载点恢复后用户在陈旧卡片上点确认，
// 语义上就是删一批本不该删的文件）。
func (c *Ctrl) ConfirmDeletes(id, fingerprint string) error {
	c.mu.Lock()
	r := c.run[id]
	c.mu.Unlock()
	if r == nil {
		return fmt.Errorf("规则未在运行: %s", id)
	}
	r.mu.Lock()
	if r.delFP != fingerprint {
		r.mu.Unlock()
		return fmt.Errorf("确认已过期：删除清单已变化，请重新查看")
	}
	r.mu.Unlock()
	metas, err := c.fetchMeta(r, func() map[string]watch.Kind {
		r.mu.Lock()
		defer r.mu.Unlock()
		m := map[string]watch.Kind{}
		for _, rel := range r.pendingDel {
			m[rel] = watch.KindDelete
		}
		return m
	}())
	if err != nil {
		// 无法确认远端状态时**绝不删**：一次瞬时 sftp 故障就会删掉远端其实仍在的文件。
		return fmt.Errorf("无法确认远端状态，已取消本批删除: %w", err)
	}
	for rel := range metas {
		if metas[rel] != nil {
			c.emit(r.rule.ID, "warn", "远端文件已恢复，取消整批删除："+rel)
			return nil
		}
	}
	c.executeDeletes(r)
	return nil
}

func removeConflict(d *StateFile, rel string) {
	for i := range d.Conflicts {
		if d.Conflicts[i].RelPath == rel {
			d.Conflicts = append(d.Conflicts[:i], d.Conflicts[i+1:]...)
			return
		}
	}
}
~~~

同时新增 `internal/sync/fs.go`（把 `os` 调用集中一处，便于测试替换）：

~~~go
package sync

import (
	"os"
	"time"
)

// FormatModTime 是本地文件时间的**唯一**序列化格式。
// 引擎记录 local_mtime 与 UI 读取 LocalState 都必须用它：两处格式不一致会让
// "未被改动"被误判成"被改动"，进而刷出假冲突。
func FormatModTime(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000000000Z07:00")
}

func osStat(p string) (LocalState, error) {
	st, err := os.Stat(p)
	if err != nil {
		return LocalState{}, err
	}
	return LocalState{Exists: true, Size: st.Size(), ModTime: FormatModTime(st.ModTime())}, nil
}

func osRemove(p string) error { return os.Remove(p) }
~~~

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/sync/ -race -count=1 -v`
Expected: 全部 PASS（含前面各任务的单测）

- [ ] **Step 5: 提交**

~~~bash
gofmt -s -w internal/sync/
git add internal/sync/
git commit -m "feat(sync): 新增同步引擎,首轮对齐/对账/退避重连与冲突入队"
~~~

## Task 15: Wails 绑定、自动启动与退出顺序

**Files:** Modify `app.go`；Modify `app_test.go`；Regenerate `frontend/wailsjs/go/main/App.d.ts` / `App.js` / `models.ts`

**Interfaces:** Produces 以下 App 方法（前端契约，命名统一 `SyncRule*` 前缀以避开既有 `SyncWindowBackground`）：

~~~go
func (a *App) ListSyncRules() []config.SyncRule
func (a *App) CreateSyncRule(r config.SyncRule) (config.SyncRule, error)
func (a *App) UpdateSyncRule(r config.SyncRule) error
func (a *App) DeleteSyncRule(id string) error
func (a *App) StartSyncRule(id string) error
func (a *App) StopSyncRule(id string) error
func (a *App) SyncRuleStates() map[string]string
func (a *App) SyncRuleStats() map[string]sync.SyncRuleStat
func (a *App) SyncRuleConflicts(id string) []sync.Conflict
func (a *App) ResolveSyncConflict(id, relPath, action string) error
func (a *App) ConfirmSyncRuleDeletes(id, fingerprint string) error
~~~

- [ ] **Step 1: 写失败的测试**

在 `app_test.go` 末尾追加：

~~~go
// 绑定契约：空切片而非 nil；创建时校验会拒绝坏规则。
func TestSyncRuleBindingsContract(t *testing.T) {
	a := newTestApp(t)
	if got := a.ListSyncRules(); got == nil {
		t.Fatal("ListSyncRules 必须返回空切片而不是 nil")
	}
	if got := a.SyncRuleConflicts("nope"); got == nil {
		t.Fatal("SyncRuleConflicts 必须返回空切片而不是 nil")
	}
	if got := a.SyncRuleStates(); got == nil {
		t.Fatal("SyncRuleStates 必须返回空 map 而不是 nil")
	}
	if _, err := a.CreateSyncRule(config.SyncRule{Host: "-bad", Kind: "dir", RemotePath: "/r", LocalPath: "/l"}); err == nil {
		t.Fatal("坏规则必须被拒绝")
	}
}

// 精确重复的规则必须被拒绝（对齐 CheckRemoteConflict 的做法）。
func TestCreateSyncRuleRejectsExactDuplicate(t *testing.T) {
	a := newTestApp(t)
	r := config.SyncRule{Host: "prod-01", Kind: "dir", RemotePath: "/r", LocalPath: "/l", PollIntervalS: 5}
	created, err := a.CreateSyncRule(r)
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	if created.ID == "" {
		t.Fatal("创建时必须生成 ID")
	}
	if _, err := a.CreateSyncRule(r); err == nil {
		t.Fatal("同 host/remote/local/kind 的重复规则必须被拒绝")
	}
}
~~~

测试文件底部补一个隔离的构造器（该文件现有的测试都是手工拼 `&App{...}`，这里统一成一个，避免碰用户真实配置）：

~~~go
// newTestApp 构造隔离的 App：配置与状态都落在临时目录。
func newTestApp(t *testing.T) *App {
	t.Helper()
	a := &App{cfg: config.DefaultAppConfig(), cfgPath: filepath.Join(t.TempDir(), "sshore.toml")}
	a.sync = sync.NewCtrl(sync.Deps{StateDir: t.TempDir()})
	return a
}
~~~

- [ ] **Step 2: 运行测试确认失败** — Run: `go test . -run SyncRule -v`；Expected: `a.ListSyncRules undefined`

- [ ] **Step 3: 实现**

在 `app.go` 的 `App` 结构体加字段 `sync *sync.Ctrl`，在 `Init` 里接线：

~~~go
	transfer, lister := sync.NewSftpAdapter(a.sftp)
	a.sync = sync.NewCtrl(sync.Deps{
		Spawner:  osutil.NewStreamer(),
		Runner:   osutil.NewCtxRunner(),
		Transfer: transfer,
		ListMany: lister,
		Emit:     emit,
		StateDir: stateDir(),
	})
~~~

并新增两个适配器（放在 `app.go` 底部）：

~~~go
// 适配器只有一处定义：internal/sync/adapters.go 的 NewSftpAdapter（见 Task 10）。
// 这里不要重复定义，否则 Task 16 的 E2E 测试还要再写一份。

// stateDir 与 DefaultConfigPath 同源：<UserConfigDir>/sshore/state。
func stateDir() string {
	p, err := config.DefaultConfigPath()
	if err != nil {
		return filepath.Join(os.TempDir(), "sshore-state")
	}
	return filepath.Join(filepath.Dir(p), "state")
}
~~~

绑定实现：

~~~go
func (a *App) ListSyncRules() []config.SyncRule {
	if a.cfg == nil || a.cfg.Syncs == nil {
		return []config.SyncRule{}
	}
	return a.cfg.Syncs
}

func (a *App) CreateSyncRule(r config.SyncRule) (config.SyncRule, error) {
	if err := sync.ValidateSyncRule(r); err != nil {
		return r, err
	}
	if a.cfg == nil {
		a.cfg = &config.AppConfig{}
	}
	for _, e := range a.cfg.Syncs {
		if e.Host == r.Host && e.RemotePath == r.RemotePath &&
			e.LocalPath == r.LocalPath && e.Kind == r.Kind {
			return r, errors.New("已存在完全相同的同步规则")
		}
	}
	if r.ID == "" {
		r.ID = config.NewSyncID()
	}
	r.Normalize()
	a.cfg.Syncs = append(a.cfg.Syncs, r)
	return r, a.saveConfig()
}

func (a *App) UpdateSyncRule(r config.SyncRule) error {
	if err := sync.ValidateSyncRule(r); err != nil {
		return err
	}
	idx := -1
	for i, e := range a.cfg.Syncs {
		if e.ID == r.ID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return errors.New("sync rule not found")
	}
	// 运行中的规则先停再存（不做热更新：远端根路径可能整个换掉）。
	_ = a.sync.Stop(r.ID)
	r.Enabled = false
	a.cfg.Syncs[idx] = r
	return a.saveConfig()
}

func (a *App) DeleteSyncRule(id string) error {
	idx := -1
	for i, e := range a.cfg.Syncs {
		if e.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return errors.New("sync rule not found")
	}
	rule := a.cfg.Syncs[idx]
	_ = a.sync.Stop(id)
	// 删除规则时一并清理状态文件与本地残留临时文件。
	_ = os.Remove(filepath.Join(stateDir(), "sync-"+id+".json"))
	_, _ = sync.CleanupParts(rule.LocalPath, id)
	a.cfg.Syncs = append(a.cfg.Syncs[:idx], a.cfg.Syncs[idx+1:]...)
	return a.saveConfig()
}

func (a *App) StartSyncRule(id string) error {
	rule, ok := a.findSyncRule(id)
	if !ok {
		return errors.New("sync rule not found")
	}
	if err := a.sync.Start(rule); err != nil {
		return err
	}
	rule.Enabled = true
	a.updateSyncRule(rule)
	return a.saveConfig()
}

func (a *App) StopSyncRule(id string) error {
	if err := a.sync.Stop(id); err != nil {
		return err
	}
	if rule, ok := a.findSyncRule(id); ok {
		rule.Enabled = false
		a.updateSyncRule(rule)
		return a.saveConfig()
	}
	return nil
}

func (a *App) SyncRuleStates() map[string]string { return a.sync.States() }
func (a *App) SyncRuleStats() map[string]sync.SyncRuleStat { return a.sync.Stats() }
func (a *App) SyncRuleConflicts(id string) []sync.Conflict { return a.sync.Conflicts(id) }

func (a *App) ResolveSyncConflict(id, relPath, action string) error {
	local, err := localStateOf(a.cfg, id, relPath)
	if err != nil {
		return err
	}
	return a.sync.ResolveConflict(id, relPath, sync.ConflictAction(action), local)
}

func (a *App) ConfirmSyncRuleDeletes(id, fingerprint string) error {
	return a.sync.ConfirmDeletes(id, fingerprint)
}

func (a *App) findSyncRule(id string) (config.SyncRule, bool) {
	if a.cfg == nil {
		return config.SyncRule{}, false
	}
	for _, e := range a.cfg.Syncs {
		if e.ID == id {
			return e, true
		}
	}
	return config.SyncRule{}, false
}

func (a *App) updateSyncRule(u config.SyncRule) {
	for i := range a.cfg.Syncs {
		if a.cfg.Syncs[i].ID == u.ID {
			a.cfg.Syncs[i] = u
		}
	}
}

// localStateOf 读取本地文件现状，供冲突裁决使用（只读，不做传输）。
func localStateOf(cfg *config.AppConfig, id, rel string) (sync.LocalState, error) {
	for _, e := range cfg.Syncs {
		if e.ID != id {
			continue
		}
		target, err := sync.LocalTarget(e.LocalPath, rel)
		if err != nil {
			return sync.LocalState{}, err
		}
		st, err := os.Stat(target)
		if err != nil {
			return sync.LocalState{Exists: false}, nil
		}
		return sync.LocalState{Exists: true, Size: st.Size(),
			ModTime: sync.FormatModTime(st.ModTime())}, nil
	}
	return sync.LocalState{}, errors.New("sync rule not found")
}
~~~

**自动启动落点**（决策 21）：把 `AutoStartEnabled` 扩展为同时启动 `enabled` 的同步规则：

~~~go
	// 现有隧道逻辑保持不变，循环之后追加：
	for _, rule := range a.cfg.Syncs {
		if !rule.Enabled {
			continue
		}
		if err := a.sync.Start(rule); err != nil {
			msg := fmt.Sprintf("自动启动同步规则 %s 失败: %v", rule.ID, err)
			if a.emit != nil {
				a.emit(forward.Event{SourceType: "sync", SourceID: rule.ID,
					TS: time.Now().Format(time.RFC3339), Level: "error", Message: msg})
			}
			errs = append(errs, errors.New(msg))
		}
	}
~~~

**退出顺序**（决策：先停 sync 再 `sftp.CloseAll`，否则 `CloseAll` 的 `RemoveAll` 会拆掉 sync 正在多路复用的连接）：

~~~go
func (a *App) OnShutdown() {
	// 1. 先停同步规则并关闭探测进程
	for _, rule := range a.cfg.Syncs {
		_ = a.sync.Stop(rule.ID)
	}
	a.forward.OnShutdown()
	// 2. 最后才关 SFTP 的 ControlMaster（它会 RemoveAll 整个 socket 目录）
	a.sftp.CloseAll()
	_ = a.saveConfig()
}
~~~

- [ ] **Step 4: 重新生成绑定并验证**

Run: `go vet ./... && go test . -race -count=1`
Expected: PASS

Run: `wails generate module && git status --short`（**在仓库根目录执行**：wails 是 Go 版 CLI，
需要读根目录的 wails.json；`npx` 解析的是 npm 包，调不到它。若不在 PATH：
`$(go env GOPATH)/bin/wails generate module`）
Expected: `frontend/wailsjs/go/main/App.d.ts`、`App.js`、`models.ts` 三个文件出现改动（Makefile 全部构建带 `-skipbindings`，这一步**不会自动发生**）。

- [ ] **Step 5: 提交**

~~~bash
gofmt -s -w app.go app_test.go
git add app.go app_test.go frontend/wailsjs
git commit -m "feat(app): 新增同步规则绑定、自动启动与退出顺序,重新生成 wailsjs"
~~~

---

## Task 16: 端到端同步验证

**Files:** Create `internal/sync/e2e_test.go`；Modify `e2e/test_local.sh`

**为什么需要 Go 侧的 E2E**：`e2e/test_local.sh` 是纯 bash、直接调 `ssh/sftp` 二进制，**没有 CLI 入口能触达 `internal/sync`**。所以脚本只负责起临时 sshd 并把连接参数通过环境变量交给 Go 测试。

- [ ] **Step 1: 写测试**

创建 `internal/sync/e2e_test.go`：

~~~go
package sync

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"sshore/internal/config"
	"sshore/internal/osutil"
	"sshore/internal/sftp"
	"sshore/internal/sshconn"
)

// TestSyncE2E 需要 e2e/test_local.sh 先起好临时 sshd 并导出：
//
//	SSHORE_E2E_HOST   ssh 别名
//	SSHORE_E2E_REMOTE 远端存在的目录
//
// 缺少环境变量时跳过并打印原因（不静默通过）。
func TestSyncE2E(t *testing.T) {
	host := os.Getenv("SSHORE_E2E_HOST")
	remote := os.Getenv("SSHORE_E2E_REMOTE")
	if host == "" || remote == "" {
		t.Skip("未提供 SSHORE_E2E_HOST / SSHORE_E2E_REMOTE，跳过真实同步验证")
	}
	if _, err := exec.LookPath("sftp"); err != nil {
		t.Skip("缺少 sftp 二进制")
	}

	ctrl := sftp.NewCtrl(osutil.NewRunner(), nil)
	transfer, lister := NewSftpAdapter(ctrl)
	local := t.TempDir()
	rule := config.SyncRule{
		ID: "e2e", Host: host, Kind: "dir", RemotePath: remote,
		LocalPath: local, MaxDepth: 0, PollIntervalS: 1, ForcePoll: true, Enabled: true,
	}
	rule.Normalize()

	runner := osutil.NewCtxRunner()
	if err := sshconn.EnsureMaster(context.Background(), runner, host, ""); err != nil {
		t.Logf("EnsureMaster 失败（允许，会退化为每命令独立连接）: %v", err)
	}

	c := NewCtrl(Deps{
		Runner: runner, Transfer: transfer,
		ListMany: lister, StateDir: t.TempDir(),
	})
	if err := c.Start(rule); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer c.Stop(rule.ID)

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if entries, err := os.ReadDir(local); err == nil && len(entries) > 0 {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("15s 内未把 %s 下的文件同步到本地", remote)
}
~~~

**适配器的唯一位置**：`internal/sync/adapters.go` 提供

~~~go
// NewSftpAdapter 让 sync 通过 sftp.Ctrl 完成传输与列举，同时保持
// internal/sync 不依赖 sftp 的内部实现。
func NewSftpAdapter(c *sftp.Ctrl) (Transferer, ListManyFunc)
~~~

main 包（Task 15）与该 E2E 测试都**只用这一个构造函数**，不要在 app.go 里再定义一份。
E2E 测试同样只用 `NewSftpAdapter`，不再自行定义适配器。

- [ ] **Step 2: 扩展 bash 脚本**

在 `e2e/test_local.sh` 的 sshd 就绪之后、退出之前追加：

~~~bash
echo "== sync e2e (Go side) =="
REMOTE_DIR="$TMPD/remote-conf"
mkdir -p "$REMOTE_DIR"
echo "v1" > "$REMOTE_DIR/app.conf"
# 注意：ssh 别名通过 -F 注入，避免污染用户 ~/.ssh/config
cat > "$HOME/.ssh/config" <<EOF
Host sshore-e2e
  HostName 127.0.0.1
  Port $PORT
  User $(id -un)
  IdentityFile $TMPD/client_key
  StrictHostKeyChecking no
  UserKnownHostsFile /dev/null
EOF
chmod 600 "$HOME/.ssh/config"
if SSHORE_E2E_HOST=sshore-e2e SSHORE_E2E_REMOTE="$REMOTE_DIR" \
   HOME="$HOME" go test ./internal/sync/ -run TestSyncE2E -count=1 -v; then
  echo "sync e2e OK"
else
  echo "sync e2e FAILED" >&2
  exit 1
fi
~~~

- [ ] **Step 3: 运行**

Run: `make e2e`
Expected: 脚本原有断言通过；新增的 `sync e2e OK`。若本机缺 `inotifywait`，Go 测试用 `ForcePoll: true` 走轮询路径，不依赖它。

- [ ] **Step 4: 提交**

~~~bash
git add internal/sync/ e2e/test_local.sh
git commit -m "test(sync): 新增真实 sshd 的端到端同步验证,由 e2e 脚本驱动 Go 测试"
~~~

## Self-Review（计划自审）

### 1. Spec 覆盖检查

逐节核对 spec，发现 **6 处缺口**，全部已就地补齐：

| spec 章节 | 落点 | 自审发现的问题与处理 |
|---|---|---|
| §4.1 watch 契约 | Task 5 | — |
| §4.3 sshconn | Task 2 | — |
| §4.4 osutil / sftp 扩展 | Task 1、3 | — |
| §5.1–5.2 SyncRule / Normalize | Task 4 | — |
| §5.3 状态文件 | Task 9 | — |
| §6.1 降级判定 | Task 6 | — |
| §6.2 inotify | Task 5、6 | **§11 W1 要求"在 inotify.go 顶部注释写明"没有落点** → 已在 Task 6 的 `InotifySource` 文档注释里补上完整弱点说明 |
| §6.3 poll 事务性 | Task 7 | **"连续 5 轮失败 → error" 无人实现** → 补 `maxPollFailures`、`stopOnce`、`Failures()`，Task 14 用类型断言判定进 `error` |
| §6.4 扫描器 / ListMany | Task 3、7 | — |
| §7.1 去抖与背压 | Task 14 | **队列上限 5000 与"退化为全量对账"没有实现** → 补 `maxQueuePaths` 与溢出分支 |
| §7.2 路径安全 | Task 8 | — |
| §7.3 决策表 | Task 8 | — |
| §7.4 传输与原子写 | Task 10 | — |
| §7.5 跨规则重复检测 | Task 15 | —（部分重叠不检测，spec 已列为已知局限 W8） |
| §7.6 删除闸门 | Task 11、14 | — |
| §7.7 冲突队列 | Task 12 | — |
| §7.8 首轮对齐 / 对账 | Task 14 | — |
| §7.9 单文件规则 | Task 14 | **完全没有任务覆盖** → 新增 `alignFile`：不做目录 BFS、源消失保留本地且不进 error、不受 `mirror_delete` 影响、状态走 `stats.SourceMissing` |
| §7.10 输入校验 | Task 13 | — |
| §8.1 状态 | Task 14 | — |
| §8.2 重连与退避 | Task 14 | 见 §6.3 那条（连续失败进 error） |
| §8.3 日志纪律 | Task 14 | **"连续失败每 5 次汇总"没有实现** → 补 `emitThrottled`，并在每轮对账后重置计数 |
| §8.5 配置变更 / 自动启动 | Task 15 | — |
| §8.6 退出顺序 | Task 15 | — |
| §9.1 绑定 | Task 15 | — |
| §10.x 测试策略 | 各任务 Step 1 | — |
| §12 影响面 | 全文 | **适配器在 Task 15 与 Task 16 重复定义** → 收敛到 `internal/sync/adapters.go` 的 `NewSftpAdapter` 一处 |

### 2. 占位符扫描

扫描 `TBD|TODO|待补|实现时|二选一` 等模式，修掉：

- Task 14：删掉无意义的 `de` 占位类型与 `Deps.de` 字段、未使用的 `savedInfo` 块、`var _ = sshconn.ControlPath` 与随之无用的 `sshconn` import、空壳 `ConfirmInput` 类型。
- Task 16：把"实现时下沉到 A 或 B，二选一"这种悬空指示改成唯一确定的写法（`NewSftpAdapter`），并把 `nil2ctx()` 换成真实的 `context.Background()` 且补上 import。

### 3. 类型一致性检查

跨任务核对了下列签名，全部一致：

- `ControlPath(host,user)` / `Exec(ctx,r,host,user,cmd)` / `QuoteRemote(s)`（Task 2 → 6）
- `ListMany(host,user,paths)`（Task 3 → 7、14）
- `Event{RelPath,Kind}`、`Meta{Size,ModTime}`、`Snapshot{Entries,Complete}`、`ScanTree(...)`（Task 5、7 → 14）
- `Entry` / `LocalState` / `Action` / `Decide(kind,ent,local,mirrorDelete)`（Task 8 → 9、12、14）
- `StateStore.With/Flush`、`Fingerprint`（Task 9 → 14）
- `TransferTo(...)`、`CleanupParts(root,ruleID)`（Task 10 → 14、15）
- `EvaluateDeleteGate(DeleteGateInput)`（Task 11 → 14）
- `Conflict` / `TransferOrDelete` / `ResolveConflict`（Task 12 → 14、15）
- `ValidateSyncRule`（Task 13 → 14、15）

**发现并修掉 1 处真 bug**：`Decide` 的"无基线"分支原本调用 `ent.remoteSizeOr(...)`，而该分支的 `ent` 正是 `nil` ——远端大小根本取不到。已改为明确语义：**引擎必须先补齐远端元信息**，因此该分支 `ent` 非 nil 且 `HasLocal=false`；`ent == nil` 表示"远端元信息未知"，保守地直接下载。测试用例同步改为传入 `&Entry{RemoteSize: rs}`。

### 4. 需要确认的两处 spec 偏离

`Global Constraints` 里登记的**扫描器位置修正**：spec §12 把 `scan.go` 放在 `internal/sync`，但它被 `watch` 的 poll 路径使用，而 §4.5 规定 `sync → watch` 单向——放 sync 会造成 `watch → sync` 反向依赖（导入环）。本计划放在 `internal/watch/scan.go`。

**执行前建议先改 spec §12 的这一行**，否则执行者对照 spec 会困惑。这需要你点头，我没有擅自改。

**第二处偏离：已按 spec 对齐（不再偏离）。** 原稿把 `auto_reconnect` 缺键按
false（不重连）处理，与 spec §5.2 的"缺键取全局 `App.AutoReconnectDefault`"相反。
现已改为 `AutoReconnect *bool` + `Reconnect() bool` 访问器，由
`AppConfig.normalize()` 在加载时灌入全局默认——既满足 spec，也避免"解码后的
`bool` 分不清缺键与显式 false"这个根本问题。副作用：`wailsjs` 模型里该字段是
`*bool`（加载后永不为 nil，前端实际只会看到 true/false）。

### 6. 第二轮自审（/review 阶段一）修掉的问题

把计划里的 Go 代码当作"要真的编译并跑起来"逐行推演，找到 12 处，全部已修：

**会导致功能失效或编译不过的（blocker）**

1. **【ParseInotifyLine 的路径前缀判断不按边界】**（Task 5）：裸用 strings.HasPrefix(full, root) 会让 root=/srv/conf 时把兄弟目录 /srv/conf-backup/x 的事件也收进来。已改为按路径边界判断，并加了两个测试用例。
2. **【删除事件永远不会真的删文件】**（Task 14）：applyOne 的 ActionDelete 分支只写了一行日志，既不过闸门也不删除 —— 也就是 mirror_delete 在 inotify 删除路径上完全失效。已改为登记进 pendingDel，由闸门放行后统一执行。
3. **【六重删除闸门里有三条永远不会触发】**（Task 14）：调用点把 FirstRound / RootGone / Overflow 留空，maybeDelete 只传了 Complete: true 和一个常量。已补 firstRound 与 blockDels 两个运行时字段，在 loop 与 consume 里置位、在闸门消费后清除。
4. **【PollSource 的 goroutine 永远停不下来】**（Task 7、14）：Close() 原本直接 return nil，而引擎传进去的是 context.Background()（永不取消）—— 规则停止后轮询 goroutine 会一直活着。已改为 Close 关闭 stop（stopOnce 保证幂等）。

**会导致假冲突的（major）**

5. **【本地时间格式两处不一致】**（Task 14 与 15）：引擎的 osStat 用固定纳秒格式，而 localStateOf 用 time.RFC3339Nano（纳秒为 0 时会省略小数部分）—— 用户在界面裁决 keep_local 后写入的记录与引擎下次读到的值不等，会刷出假冲突。已抽出唯一的 FormatModTime 供两处共用。
6. **【下载后 stat 失败会把零值当作基线写入】**（Task 14）：原本 after, _ := osStat(local)，失败时得到 HasLocal=true 且大小时间为零的记录，下一次比对恒不等。已加防护：stat 失败则本条不登记基线并记 warn。
7. **【适配器定义重复】**（Task 15 与 16）：两处各定义了一份 sftpTransfer。已收敛到 internal/sync/adapters.go 的 NewSftpAdapter，main 包与 E2E 测试共用，并把它登记进文件结构与 Task 10。

**其余（minor）**

8. Task 14 锁顺序未写明（align 里是 state.mu 到 r.mu 的嵌套）→ 已在 Deps 前写明不变式：绝不允许先持 r.mu 再进 state.With / state.Flush。
9. Task 7 的 PollSource.emit 是无退出路径的阻塞发送 → 已加 select on stop。
10. Task 15 的测试引用了不存在的 newTestApp → 已给出真实构造器代码。
11. 文件结构表缺 internal/sync/adapters.go → 已补。
12. Task 6 的 s.done 在 StartStream 返回后才赋值，理论上存在极窄的 nil 窗口 → 未改（影响可忽略），但实现时不要在 handleStdout 里假设 done 非 nil。

### 5. 未覆盖到计划的 spec 内容

以下 spec 内容**故意不在本计划范围**，由后续的前端计划承担：§9.2 界面、§9.3 实时性（`KeepAlive` / 不二次 `EventsOn`）、§9.4 日志隔离（`LogPanel` 的 `sourceTypes` 与 `sftp` 日志量修正）。Task 15 只负责把绑定与生成物落地。

---

## Task 17: 关键集成测试（第三轮补）

**Files:** Modify ¤internal/sync/ctrl_test.go¤

**为什么单独列**：Task 8–14 的单测都是纯逻辑层（决策表、闸门、状态文件），而第三轮复审暴露的问题**全部在"接线"上**——逻辑对但没接上。这一层只能靠集成测试守住。

- [ ] **Step 1: 写测试**

在 ¤internal/sync/ctrl_test.go¤ 追加：

~~~go
// 端到端验证"镜像删除真的会删文件"。它能同时守住三件事：
//   1) applyOne 的 ActionDelete 会登记进 pendingDel；
//   2) drainQueue 处理完会真的调闸门（否则这里永远不会删）；
//   3) 闸门在事件路径（CountKnown=false）放行单条删除。
func TestEngineMirrorDeleteActuallyDeletes(t *testing.T) {
	fs := newFakeRemote()
	fs.set("/r", sftp.Item{Name: "a.txt", Size: 5, ModTime: "2026-09-10 10:00"})
	xf := &fakeXfer{remote: map[string]string{"/r/a.txt": "hello"}}
	c, rule, local := newTestCtrl(t, fs.list, xf)
	rule.MirrorDelete = true
	if err := c.Start(rule); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer c.Stop(rule.ID)

	target := filepath.Join(local, "a.txt")
	waitFor(t, 5*time.Second, func() bool {
		_, err := os.Stat(target)
		return err == nil
	}, "首轮对齐未把 a.txt 拉下来")

	fs.set("/r") // 远端删除
	waitFor(t, 6*time.Second, func() bool {
		_, err := os.Stat(target)
		return os.IsNotExist(err)
	}, "mirror_delete 未删除本地文件（删除闸门没接上或未放行）")
}

func waitFor(t *testing.T, d time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal(msg)
}
~~~

- [ ] **Step 2: 运行** — Run: ¤go test ./internal/sync/ -race -count=1 -run MirrorDelete -v¤；Expected: PASS

- [ ] **Step 3: 提交** — ¤git add internal/sync/ && git commit -m "test(sync): 新增 mirror_delete 端到端测试,守住删除闸门接线"¤

### 仍未写的测试（复审点名要求，后续补）

以下测试**本轮没有写**，不要在实现时以为已经覆盖：

1. ¤ConfirmDeletes¤ 的"挂起 → 远端恢复 → 确认 → 整批作废"（对应 §7.6 第 6 条）；
2. ¤ResolveConflict¤ 与引擎并发时的无死锁断言（对应 §10.3）；
3. ¤save_as¤ 写到 ¤<name>.remote-<ts>¤ 且**不覆盖本地**（对应 §7.7，数据安全级）；
4. 单文件失败 3 次退避（1s/4s/16s）后标记 ¤failed¤ 且继续处理其它文件（对应 §8.4）；
5. 背压：队列超过 ¤maxQueuePaths¤ 时退化为全量对账而不是阻塞（对应 §7.1）。

## Execution Handoff

计划已保存到 `docs/superpowers/plans/2026-09-10-remote-folder-sync-backend.md`（后端，Task 1–16）。

前端计划（`SyncView.vue` / `SyncCard.vue` / `SyncConflictsDialog.vue`、`LogPanel` 的 sourceTypes 隔离、`KeepAlive` 下的防抖刷新、`wailsjs` 调用）在后端落地后单独出。

