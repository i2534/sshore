# 检测更新与自升级实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 给 sshore 加上「检测更新 → 提示 → 应用内下载 + SHA256 校验 → 用户显式点击后由一次性脚本替换二进制并重启」的能力，按阶段 A（检测提示）与阶段 B（下载自升级）两步交付，且不引入 AV/数据损坏风险。

**Architecture:** 新增 Go 包 `internal/update`（版本类判定、GitHub API 源、资产挑选、下载校验、归档解包、替换计划、升级脚本生成、ExeDir 排他锁、状态机），依赖全部注入以便纯单测；`app.go` 在 `startup(ctx)` 构造并调度、在 `OnShutdown`/`SetSettings` 停止与重配置，暴露 9 个绑定；进度与状态经 Wails 事件推送。升级脚本是 `go:embed` 的静态文件，参数在 Linux 走 argv、Windows 走环境变量（经 cmd 转发 argv 无法满足零插值），替换动作永远由用户显式点击触发。

**Tech Stack:** Go 1.26、Wails v2.15.0（`runtime.Quit`/`EventsEmit`/`BrowserOpenURL`）、`golang.org/x/sys/windows`（`CREATE_NO_WINDOW`，已是直接依赖）、Vue 3 `<script setup>` + Pinia、vitest（无 jsdom，纯函数 + SSR 风格）、GitHub Actions（release job + go-windows job）。

**Spec:** `docs/superpowers/specs/2026-09-19-update-check-upgrade-design.md`（v3，748 行）。执行者**必须**同时读 spec：本计划只重复执行所需的最小信息，凡「为什么」的问题以 spec 为准（§4 待复核事实、§7.5 pending 唯一判别、§8 脚本契约、§10.1 信任边界、§14 DoD）。

**前置门禁:** spec §4 的 F3（Windows 允许重命名运行中的 exe）是阶段 B 的**可行性前提**。Task 0 结论为 no-go 时，**阶段 B 的全部任务作废**，只交付阶段 A + 人工替换指引。

## Global Constraints

- Go 1.26；**不新增第三方依赖**（`golang.org/x/sys` 已是直接依赖，见 `go.mod:10`）。
- 提交信息用**中文**；每个 task 结束时 `make ci` 必须全绿：`cd frontend && npm run build` + `go vet ./...` + `go test ./... -race -count=1` + `npx vitest run`（Makefile:93-98）。
- Go 注释与错误文案用中文，风格与 `internal/sftp`、`internal/sync` 一致。
- **只有用户显式点击「重启并升级」才允许替换二进制**；任何自动路径（检查/下载/自检）都不得写 ExeDir 里的正式二进制。
- 校验失败一律拒绝安装：压缩包 SHA256 由 release 的 `checksums.txt` 比对；pending 落盘后写 sidecar `.sha256`，apply 前重算。
- 下载 URL 必须与 `update_source` 同 host（`SameOrigin`）；非 loopback 的自定义源必须 `https://`。
- 脚本内禁止出现 `powershell`、`curl`、`wget`、`certutil`、网络动作与 `eval`（有测试断言）。
- 进度事件节流 **200ms**；状态事件在状态变化时发一次；`UpdateInfo.Seq` 单调递增。
- 备份**只保留最近一份**；pending 与备份的判别只用版本序（spec §7.5），不做名字模式猜测。
- 前端沿用现有风格：无 jsdom、无 `@vue/test-utils`，纯函数单测 + SSR 渲染测试。

## File Structure（先锁定文件边界，再拆任务）

| 文件 | 责任 | 任务 |
|---|---|---|
| `internal/update/version.go` | 版本分类/比较/pending 名解析（纯函数） | 1 |
| `internal/update/checksum.go` | `sha256sum` 解析与文件核对 | 2 |
| `internal/update/extract.go` | `tar.gz`/`zip` 白名单解包（拒绝链接/遍历/超限） | 2 |
| `internal/update/source.go` | GitHub API 客户端与错误哨兵（`Client.Latest`） | 3 |
| `internal/update/asset.go` | 资产挑选与同源校验 | 3 |
| `internal/update/download.go` | 流式下载（进度/超时/空闲超时）+ 空间检查 | 4 |
| `internal/update/plan.go` | 替换计划、备份候选、失败残留恢复 | 5 |
| `internal/update/scripts/update.sh`、`update.cmd` | 升级脚本（embed 资源，纯 sh / 纯 cmd） | 6 |
| `internal/update/script.go` | 脚本写出、参数/环境构造、分离启动 | 6 |
| `internal/update/lock.go` | ExeDir 排他锁（flock / 命名互斥体） | 7 |
| `internal/update/service.go` | 状态机、并发守卫 | 8 |
| `internal/update/service.go` | 下载全流程、残留自检、轮询调度 | 9 |
| `internal/config/store.go` | 4 个新配置字段与 Normalize | 10 |
| `app.go` | 9 个绑定 + startup/OnShutdown/SetSettings 接线 | 11 |
| `frontend/wailsjs/**` | 重新生成的绑定（提交进仓库） | 11（同任务内生成） |
| `internal/update/script_run_test.go` | 脚本真跑（成功/超时/回滚） | 12 |
| `frontend/src/utils/update.js` | 状态→文案、按钮矩阵、格式化（纯函数） | 13 |
| `frontend/src/stores/update.js` | 事件归约（seq）、角标派生、动作封装 | 14 |
| `frontend/src/components/UpdateSection.vue` | 更新区 UI | 15 |
| `frontend/src/components/SettingsDialog.vue`、`stores/settings.js` | 挂载 + 4 个字段 + 跳过双写修复 | 15 |
| `frontend/src/App.vue` | 侧栏角标 | 16 |
| `.gitattributes` | `*.sh` LF / `*.cmd` CRLF | 6 |
| `.github/workflows/ci.yml` | checksums + shellcheck + Windows 真跑脚本 | 17 |
| `README.md` | 「更新与升级」一节 | 18 |
| `e2e/fake_update_source.py` | 端到端假源 | 19 |

---

## 前置门禁

### Task 0: 前置门禁 —— F3（Windows 重命名运行中的 exe）+ F1 复核

**Files:**
- 产出（不入库，.superpowers 已 gitignore）: `.superpowers/update-2026-09-19/f3-spike.md`（结论 + 原始输出）
- 临时探针: `/tmp/winrename/main.go`（**不提交**）

**Interfaces:**
- Consumes: 无
- Produces: 一个 go/no-go 结论，决定 Task 6/8/10/12/17/19 是否执行。

- [ ] **Step 1: 写探针**（验证「同卷 rename 运行中自映像」与「原位置能否写入」两件事）

```go
// /tmp/winrename/main.go
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

func main() {
	dir := os.Args[1]
	self, _ := os.Executable()
	dst := filepath.Join(dir, "probe-old.exe")
	if err := os.Rename(self, dst); err != nil {
		fmt.Println("RENAME_FAIL:", err)
		return
	}
	fmt.Println("RENAME_OK")
	if err := os.WriteFile(self, []byte("x"), 0o755); err != nil {
		fmt.Println("WRITE_ORIGINAL_FAIL:", err)
	} else {
		fmt.Println("WRITE_ORIGINAL_OK")
	}
	time.Sleep(2 * time.Second)
	_ = exec.Command(dst).Start()
}
```

- [ ] **Step 2: 构建并拷到 Windows VM**

```bash
cd /tmp/winrename && GOOS=windows GOARCH=amd64 go build -o probe.exe .
scp -P 2222 probe.exe lan@127.0.0.1:C:/Users/lan/rt/probe.exe
```

Expected: 构建成功；scp 返回 0（VM 与端口约定见 `~/.dsh/skills/real-machine-testing/SKILL.md`）。

- [ ] **Step 3: 在 VM 的交互式会话里运行探针**

```bash
ssh -p 2222 lan@127.0.0.1 "cmd /c mkdir C:\Users\lan\rt\probe 2>nul & C:\Users\lan\rt\probe.exe C:\Users\lan\rt\probe"
```

Expected: 输出包含 `RENAME_OK` 与 `WRITE_ORIGINAL_OK`。

- [ ] **Step 4: 判定并落盘结论**

把「探针输出 + 结论」写进 `.superpowers/update-2026-09-19/f3-spike.md`：

- `RENAME_OK` 且 `WRITE_ORIGINAL_OK` → **go**：阶段 B 按本计划执行（脚本走「先改名备份、再把待安装文件写成正式名」）。
- `RENAME_FAIL` → **no-go**：阶段 B 作废，只交付阶段 A，并把 spec §10.4 的「人工替换」写成设置页文案（Task 18 的子项）。

- [ ] **Step 5: 同时复核 F1（一次即可，写进同一份结论文件）**

```bash
gh api repos/i2534/sshore/releases/latest --jq "{tag: .tag_name, assets: [.assets[].name], keys: (keys|length)}"
```

Expected: tag=v0.6.0，assets 两个（`sshore-v0.6.0-linux-amd64.tar.gz`、`sshore-v0.6.0-windows-amd64.zip`）。

- [ ] **Step 5b: 顺带复核 F2 与 F4（各一次，结论并入同一文件）**

```bash
# F2：未认证配额响应头（记录当时数值，作为「默认 12h 间隔是否够用」的依据）
curl -sSI https://api.github.com/repos/i2534/sshore/releases/latest | grep -i "^x-ratelimit"
```

Expected: 出现 `x-ratelimit-remaining` / `x-ratelimit-reset`。

```powershell
# F4：应用内下载的产物不应带 MOTW（在 Windows VM 上对下载出的 pending 文件检查）
Get-Item <pending 文件路径> -Stream Zone.Identifier -ErrorAction SilentlyContinue
```

Expected: 无输出（不存在 Zone.Identifier），符合 spec §4 F4 的预期。

- [ ] **Step 6: 记录结论到 spec（表格 F3 行补「已实测 + 日期 + 结论」）并提交**

```bash
git add docs/superpowers/specs/2026-09-19-update-check-upgrade-design.md
git commit -m "docs(spec): F3 门禁实测结论（go/no-go）"
```

---

## 阶段 A（可独立交付：检测 + 提示）

### Task 1: version.go —— 版本类判定、比较、pending 名解析

**Files:**
- Create: `internal/update/version.go`
- Test: `internal/update/version_test.go`

**Interfaces:**
- Consumes: 无
- Produces: `type Kind int`（`KindInvalid`/`KindClean`/`KindDescribe`/`KindDev`）；`func Class(v string) Kind`；`func IsRelease(v string) bool`；`func Compare(a, b string) int`；`func Base(v string) string`；`func ParsePendingName(name string) (string, bool)`

- [ ] **Step 1: 写失败测试**

```go
package update

import "testing"

func TestClass(t *testing.T) {
	cases := []struct {
		in   string
		want Kind
	}{
		{"v0.7.0", KindClean},
		{"0.7.0", KindClean},
		{"v0.6.0-80-gc2d2a36", KindDescribe},
		{"dev", KindDev},
		{"", KindDev},
		{"abc", KindInvalid},
		{"1.2", KindInvalid},
	}
	for _, c := range cases {
		if got := Class(c.in); got != c.want {
			t.Errorf("Class(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestCompareIsNumericNotLexical(t *testing.T) {
	if Compare("v0.10.0", "v0.9.0") <= 0 {
		t.Fatal("0.10.0 必须大于 0.9.0（十进制而非字典序）")
	}
	if Compare("v0.7.0", "v0.7.0") != 0 {
		t.Fatal("相同版本必须相等")
	}
}

func TestParsePendingName(t *testing.T) {
	cases := []struct {
		in  string
		ver string
		ok  bool
	}{
		{"sshore.v0.7.0", "v0.7.0", true},
		{"sshore.v0.7.0.exe", "v0.7.0", true},
		{"sshore.dev-20260919-101112", "dev-20260919-101112", true},
		{"sshore.exe", "", false},  // 正式二进制不能是候选（否则版本段会被解析成 "exe"）
		{"sshore", "", false},
		{"sshore-update.sh", "", false},
		{"sshore.v0.7.0.sha256", "", false},
	}
	for _, c := range cases {
		ver, ok := ParsePendingName(c.in)
		if ok != c.ok || ver != c.ver {
			t.Errorf("ParsePendingName(%q) = (%q, %v), want (%q, %v)", c.in, ver, ok, c.ver, c.ok)
		}
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

```bash
mkdir -p internal/update && go test ./internal/update/ -run "TestClass|TestCompareIsNumeric|TestParsePendingName" -v
```

Expected: FAIL —— `undefined: Class`（包内暂无实现）。

- [ ] **Step 3: 实现**

```go
package update

import (
	"regexp"
	"strconv"
	"strings"
)

// Kind 是版本串的类别。分类必须显式区分「干净 tag」与「git describe 串」：
// Makefile 用 git describe --tags，非 tag 构建会产出 v0.6.0-80-gc2d2a36 这类串，
// 只有干净 tag 才能与 Release tag 做有序比较（spec §4 F8）。
type Kind int

const (
	KindInvalid Kind = iota
	KindClean
	KindDescribe
	KindDev
)

var (
	cleanRe    = regexp.MustCompile(`^v?[0-9]+[.][0-9]+[.][0-9]+$`)
	describeRe = regexp.MustCompile(`^v?[0-9]+[.][0-9]+[.][0-9]+-[0-9]+-g[0-9a-f]+$`)
	// pendingRe 只接受「干净 tag / git describe 串 / dev-<时间戳>」三种版本段形态，
	// 因此 sshore.exe、sshore.v0.7.0.sha256、sshore-update.sh 都不会被误判为候选。
	pendingRe = regexp.MustCompile(`^sshore[.]((?:v?[0-9]+[.][0-9]+[.][0-9]+(?:-[0-9]+-g[0-9a-f]+)?|dev-[0-9]{8}-[0-9]{6}))([.]exe)?$`)
)

// Class 判定版本串类别；空串与 dev 都算开发态。
func Class(v string) Kind {
	v = strings.TrimSpace(v)
	switch {
	case v == "" || v == "dev":
		return KindDev
	case cleanRe.MatchString(v):
		return KindClean
	case describeRe.MatchString(v):
		return KindDescribe
	default:
		return KindInvalid
	}
}

// IsRelease 表示这是可直接与 Release tag 比较的干净版本。
func IsRelease(v string) bool { return Class(v) == KindClean }

// Base 去掉 v 前缀，用于「是否已被用户跳过同一版本」的比对。
func Base(v string) string { return strings.TrimPrefix(strings.TrimSpace(v), "v") }

// Compare 比较主三段版本号；非 semver 输入视为不可比（返回 0）。
func Compare(a, b string) int {
	an, aok := semver(a)
	bn, bok := semver(b)
	if !aok || !bok {
		return 0
	}
	for i := 0; i < 3; i++ {
		if an[i] != bn[i] {
			if an[i] < bn[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

func semver(v string) ([3]int, bool) {
	var out [3]int
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// ParsePendingName 判断文件名是否属于「待安装文件 / 备份」候选并取出版本段。
// 候选的版本段必须以数字或 dev- 开头，因此正式二进制 sshore/sshore.exe、
// sidecar（.sha256）与升级脚本都不会被误判。
func ParsePendingName(name string) (string, bool) {
	m := pendingRe.FindStringSubmatch(name)
	if m == nil {
		return "", false
	}
	return m[1], true
}
```

- [ ] **Step 4: 运行测试确认通过**

```bash
go test ./internal/update/ -v
```

Expected: PASS（3 个测试）。

- [ ] **Step 5: 提交**

```bash
git add internal/update/version.go internal/update/version_test.go
git commit -m "feat(update): 版本类判定、比较与待安装文件名解析"
```

---

### Task 2: checksum.go + extract.go —— 校验与安全解包

**Files:**
- Create: `internal/update/checksum.go`、`internal/update/extract.go`
- Test: `internal/update/checksum_test.go`、`internal/update/extract_test.go`

**Interfaces:**
- Consumes: 无
- Produces: `func ParseChecksums(r io.Reader) (map[string]string, error)`；`func FileSHA256(path string) (string, error)`；`func VerifyFile(path, want string) error`；`func ExtractBinary(archive, goos, dest string) error`；包内 `var maxBinarySize int64 = 64 << 20`（测试可临时改小）

- [ ] **Step 1: 写失败测试**

```go
package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseChecksums(t *testing.T) {
	const h1 = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	const h2 = "486ea46224d1bb4fb680f34f7c9ad96a8f24ec88be73ea8e5a6c65260e9cb8a7"
	in := h1 + "  sshore-v0.7.0-linux-amd64.tar.gz\r\n" +
		h2 + " *sshore-v0.7.0-windows-amd64.zip\r\n" +
		"\r\n"
	m, err := ParseChecksums(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if m["sshore-v0.7.0-linux-amd64.tar.gz"] != h1 {
		t.Fatalf("tar.gz 哈希解析错误: %v", m)
	}
	if m["sshore-v0.7.0-windows-amd64.zip"] != h2 {
		t.Fatalf("二进制前缀 * 未处理: %v", m)
	}
}

func TestVerifyFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.txt")
	if err := os.WriteFile(p, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	// echo -n hello | sha256sum
	const want = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	if err := VerifyFile(p, want); err != nil {
		t.Fatalf("期望通过: %v", err)
	}
	if err := VerifyFile(p, strings.Repeat("0", 64)); err == nil {
		t.Fatal("哈希不符必须报错")
	}
}

// makeTarGz 造一个含指定条目的 tar.gz。
func makeTarGz(t *testing.T, entries map[string]struct {
	body []byte
	link byte
	mode int64
}) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for name, e := range entries {
		hdr := &tar.Header{Name: name, Mode: e.mode, Size: int64(len(e.body)), Typeflag: e.link}
		if e.link == tar.TypeSymlink {
			hdr.Linkname = "other"
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(e.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func writeTemp(t *testing.T, data []byte, name string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestExtractBinaryTarGzAcceptsDotSlashPrefix(t *testing.T) {
	// 真实 Release 的 tar.gz 里是 ./sshore（带 ./ 前缀，mode 0755）—— F6 实测。
	data := makeTarGz(t, map[string]struct {
		body []byte
		link byte
		mode int64
	}{
		"./sshore":  {[]byte("NEW"), tar.TypeReg, 0o755},
		"./README.md": {[]byte("doc"), tar.TypeReg, 0o644},
	})
	arc := writeTemp(t, data, "a.tar.gz")
	dest := filepath.Join(t.TempDir(), "out")
	if err := ExtractBinary(arc, "linux", dest); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dest)
	if err != nil || string(got) != "NEW" {
		t.Fatalf("提取内容错误: %q %v", got, err)
	}
}

func TestExtractBinaryZipRootName(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("sshore.exe")
	_, _ = w.Write([]byte("NEWEXE"))
	_, _ = zw.Create("README.md")
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	arc := writeTemp(t, buf.Bytes(), "a.zip")
	dest := filepath.Join(t.TempDir(), "out")
	if err := ExtractBinary(arc, "windows", dest); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(dest); string(got) != "NEWEXE" {
		t.Fatalf("zip 提取错误: %q", got)
	}
}

func TestExtractBinaryRejectsSymlinkTraversalAndMultiMatch(t *testing.T) {
	linkOnly := makeTarGz(t, map[string]struct {
		body []byte
		link byte
		mode int64
	}{"sshore": {nil, tar.TypeSymlink, 0o777}})
	cases := []struct {
		name string
		data []byte
		goos string
	}{
		{"符号链接", linkOnly, "linux"},
		{"路径遍历", makeTarGz(t, map[string]struct {
			body []byte
			link byte
			mode int64
		}{"../sshore": {[]byte("X"), tar.TypeReg, 0o755}}), "linux"},
		{"多份匹配", makeTarGz(t, map[string]struct {
			body []byte
			link byte
			mode int64
		}{"sshore": {[]byte("A"), tar.TypeReg, 0o755}, "./sshore": {[]byte("B"), tar.TypeReg, 0o755}}), "linux"},
		{"没有匹配", makeTarGz(t, map[string]struct {
			body []byte
			link byte
			mode int64
		}{"README.md": {[]byte("doc"), tar.TypeReg, 0o644}}), "linux"},
	}
	for _, c := range cases {
		arc := writeTemp(t, c.data, "x.tar.gz")
		if err := ExtractBinary(arc, c.goos, filepath.Join(t.TempDir(), "out")); err == nil {
			t.Errorf("%s: 必须报错", c.name)
		}
	}
}

func TestExtractBinaryRejectsOversizeEntry(t *testing.T) {
	old := maxBinarySize
	maxBinarySize = 4
	defer func() { maxBinarySize = old }()
	data := makeTarGz(t, map[string]struct {
		body []byte
		link byte
		mode int64
	}{"sshore": {[]byte("1234567890"), tar.TypeReg, 0o755}})
	arc := writeTemp(t, data, "big.tar.gz")
	if err := ExtractBinary(arc, "linux", filepath.Join(t.TempDir(), "out")); err == nil {
		t.Fatal("超过大小上限必须报错")
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

```bash
go test ./internal/update/ -run "TestParseChecksums|TestVerifyFile|TestExtractBinary" -v
```

Expected: FAIL —— `undefined: ParseChecksums`。

- [ ] **Step 3: 实现 checksum.go**

```go
package update

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"
)

// ParseChecksums 解析 sha256sum 输出（每行「<hash>  <file>」；二进制模式带 * 前缀）。
// 空行忽略；字段不足或哈希非法则整份拒绝 —— 校验文件不可信时宁可失败。
func ParseChecksums(r io.Reader) (map[string]string, error) {
	out := map[string]string{}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 2 {
			return nil, fmt.Errorf("校验文件格式非法: %q", line)
		}
		hash := strings.ToLower(parts[0])
		if len(hash) != 64 {
			return nil, fmt.Errorf("校验文件哈希长度非法: %q", hash)
		}
		name := strings.TrimPrefix(parts[1], "*")
		if name == "" {
			return nil, fmt.Errorf("校验文件缺少文件名: %q", line)
		}
		out[name] = hash
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("校验文件为空")
	}
	return out, nil
}

// FileSHA256 流式计算文件哈希（下载与 sidecar 都用它）。
func FileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// VerifyFile 重算并与 want 比对（大小写不敏感）。
func VerifyFile(path, want string) error {
	got, err := FileSHA256(path)
	if err != nil {
		return err
	}
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("哈希不符：期望 %.12s… 实际 %.12s…", want, got)
	}
	return nil
}
```

- [ ] **Step 4: 实现 extract.go**

```go
package update

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// maxBinarySize 是单个归档条目的解压上限；测试可临时改小以覆盖越界路径。
var maxBinarySize int64 = 64 << 20

// binaryNames 返回该平台归档里允许提取的条目 basename。
func binaryNames(goos string) []string {
	if goos == "windows" {
		return []string{"sshore.exe"}
	}
	return []string{"sshore"}
}

// ExtractBinary 从 tar.gz / zip 中取出唯一的目标二进制写到 dest，并置 0755。
// 安全约束：只接受常规文件条目、拒绝路径遍历与符号/硬链接、拒绝多份匹配、拒绝超限条目。
func ExtractBinary(archive, goos, dest string) error {
	if strings.HasSuffix(archive, ".zip") {
		return extractZip(archive, goos, dest)
	}
	return extractTarGz(archive, goos, dest)
}

func wantName(goos string) string {
	names := binaryNames(goos)
	return names[0]
}

// cleanEntry 把归档条目归一成 basename；带 ./ 前缀也会被 Clean 掉（F6 实测）。
func cleanEntry(name string) string {
	return path.Base(path.Clean(strings.ReplaceAll(name, "\\", "/")))
}

// unsafeEntry 判断条目名是否含路径遍历或绝对路径。
// 必须在归一之前判断：path.Base(path.Clean("../sshore")) 会得到 "sshore"，
// 只靠归一化会把遍历条目「洗白」成合法条目（两阶段评审实测）。
func unsafeEntry(name string) bool {
	n := strings.ReplaceAll(name, "\\", "/")
	if strings.HasPrefix(n, "/") || strings.Contains(n, ":") {
		return true
	}
	for _, seg := range strings.Split(n, "/") {
		if seg == ".." {
			return true
		}
	}
	return false
}

func extractTarGz(archive, goos, dest string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	found := 0
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if unsafeEntry(hdr.Name) {
			return fmt.Errorf("归档条目 %q 含路径遍历，拒绝", hdr.Name)
		}
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeRegA {
			if cleanEntry(hdr.Name) == wantName(goos) {
				return fmt.Errorf("归档条目 %q 不是常规文件（拒绝链接/设备）", hdr.Name)
			}
			continue
		}
		if cleanEntry(hdr.Name) != wantName(goos) {
			continue
		}
		found++
		if found > 1 {
			return fmt.Errorf("归档里有多份 %s，拒绝安装", wantName(goos))
		}
		if hdr.Size > maxBinarySize {
			return fmt.Errorf("归档条目过大（%d > %d）", hdr.Size, maxBinarySize)
		}
		if err := writeExact(dest, tr, hdr.Size); err != nil {
			return err
		}
	}
	if found == 0 {
		return fmt.Errorf("归档里没有找到 %s", wantName(goos))
	}
	return os.Chmod(dest, 0o755)
}

func extractZip(archive, goos, dest string) error {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer zr.Close()
	found := 0
	for _, zf := range zr.File {
		if unsafeEntry(zf.Name) {
			return fmt.Errorf("归档条目 %q 含路径遍历，拒绝", zf.Name)
		}
		if cleanEntry(zf.Name) != wantName(goos) {
			continue
		}
		if zf.FileInfo().Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("归档条目 %q 是符号链接，拒绝", zf.Name)
		}
		if zf.FileInfo().IsDir() {
			continue
		}
		found++
		if found > 1 {
			return fmt.Errorf("归档里有多份 %s，拒绝安装", wantName(goos))
		}
		if int64(zf.UncompressedSize64) > maxBinarySize {
			return fmt.Errorf("归档条目过大（%d > %d）", zf.UncompressedSize64, maxBinarySize)
		}
		rc, err := zf.Open()
		if err != nil {
			return err
		}
		err = writeExact(dest, rc, int64(zf.UncompressedSize64))
		_ = rc.Close()
		if err != nil {
			return err
		}
	}
	if found == 0 {
		return fmt.Errorf("归档里没有找到 %s", wantName(goos))
	}
	return os.Chmod(dest, 0o755)
}

// writeExact 以 0600 写临时目标并核对字节数，写完再由调用方 chmod。
func writeExact(dest string, r io.Reader, size int64) error {
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	n, err := io.Copy(out, io.LimitReader(r, size+1))
	closeErr := out.Close()
	if err != nil {
		_ = os.Remove(dest)
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if n != size {
		_ = os.Remove(dest)
		return fmt.Errorf("解包字节数不符：期望 %d 实际 %d", size, n)
	}
	return nil
}
```

注意：`extract.go` 里用到的 `path`/`filepath` 只保留实际需要的导入（`filepath` 若未使用必须删掉，否则 `go vet` 会失败）。

- [ ] **Step 5: 运行测试确认通过**

```bash
go test ./internal/update/ -v
```

Expected: PASS（含 `TestExtractBinaryRejectsSymlinkTraversalAndMultiMatch` 的 4 个子用例）。

- [ ] **Step 6: 提交**

```bash
git add internal/update/checksum.go internal/update/extract.go internal/update/checksum_test.go internal/update/extract_test.go
git commit -m "feat(update): sha256 校验工具与白名单归档解包"
```

---

---

### Task 3: source.go + asset.go —— GitHub API 客户端、错误哨兵、资产挑选

**Files:**
- Create: `internal/update/source.go`、`internal/update/asset.go`
- Test: `internal/update/source_test.go`、`internal/update/asset_test.go`

**Interfaces:**
- Consumes: 无
- Produces: `type Doer interface { Do(*http.Request) (*http.Response, error) }`；`type Asset struct{ Name, URL string }`；`type Release struct{ Tag, Notes, PublishedAt string; Assets []Asset }`；`const DefaultSource = "https://api.github.com/repos/i2534/sshore"`；`type Client struct{ HTTP Doer; UserAgent string }`；`func (c *Client) Latest(ctx context.Context, source string) (Release, error)`；`func PickArchive(rel Release, goos, goarch string) (Asset, error)`；`func PickChecksums(rel Release) (Asset, error)`；`func SameOrigin(source, rawURL string) bool`；哨兵 `ErrRateLimited`/`ErrCheckFailed`/`ErrNoAsset`/`ErrNoChecksum`

- [ ] **Step 1: 写失败测试**

```go
package update

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newTestClient() *Client {
	return &Client{HTTP: &http.Client{}, UserAgent: "sshore/test"}
}

const latestOKBody = `{"tag_name":"v0.7.0","body":"note","published_at":"2026-09-19T00:00:00Z","assets":[{"name":"sshore-v0.7.0-linux-amd64.tar.gz","browser_download_url":"https://github.com/x/y/releases/download/v0.7.0/a.tar.gz"}]}`

func TestLatestStatusMapping(t *testing.T) {
	cases := []struct {
		name    string
		code    int
		header  map[string]string
		body    string
		wantErr error
	}{
		{"200", http.StatusOK, nil, latestOKBody, nil},
		{"403 限流", http.StatusForbidden, map[string]string{"X-RateLimit-Remaining": "0"}, "", ErrRateLimited},
		{"403 非限流", http.StatusForbidden, nil, "", ErrCheckFailed},
		{"429", http.StatusTooManyRequests, nil, "", ErrRateLimited},
		{"500", http.StatusInternalServerError, nil, "", ErrCheckFailed},
		{"坏 JSON", http.StatusOK, nil, "{oops", ErrCheckFailed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/releases/latest" {
					t.Errorf("路径错误: %s", r.URL.Path)
				}
				if got := r.Header.Get("User-Agent"); got != "sshore/test" {
					t.Errorf("UA 未设置: %q", got)
				}
				for k, v := range c.header {
					w.Header().Set(k, v)
				}
				w.WriteHeader(c.code)
				_, _ = w.Write([]byte(c.body))
			}))
			defer srv.Close()
			rel, err := newTestClient().Latest(context.Background(), srv.URL)
			if !errors.Is(err, c.wantErr) {
				t.Fatalf("err = %v, want %v", err, c.wantErr)
			}
			if c.wantErr == nil && (rel.Tag != "v0.7.0" || len(rel.Assets) != 1) {
				t.Fatalf("解析结果错误: %+v", rel)
			}
		})
	}
}

func TestLatestEmptyAssets(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"v0.7.0","assets":[]}`))
	}))
	defer srv.Close()
	rel, err := newTestClient().Latest(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := PickArchive(rel, "linux", "amd64"); !errors.Is(err, ErrNoAsset) {
		t.Fatalf("空 assets 必须 ErrNoAsset, got %v", err)
	}
}

func TestPickArchiveAndChecksums(t *testing.T) {
	rel := Release{Tag: "v0.7.0", Assets: []Asset{
		{Name: "sshore-v0.7.0-linux-amd64.tar.gz", URL: "https://github.com/a/b.tar.gz"},
		{Name: "sshore-v0.7.0-windows-amd64.zip", URL: "https://github.com/a/b.zip"},
		{Name: "checksums.txt", URL: "https://github.com/a/checksums.txt"},
	}}
	lin, err := PickArchive(rel, "linux", "amd64")
	if err != nil || lin.Name != "sshore-v0.7.0-linux-amd64.tar.gz" {
		t.Fatalf("linux 挑选失败: %v %+v", err, lin)
	}
	win, err := PickArchive(rel, "windows", "amd64")
	if err != nil || win.Name != "sshore-v0.7.0-windows-amd64.zip" {
		t.Fatalf("windows 挑选失败: %v %+v", err, win)
	}
	if _, err := PickArchive(rel, "windows", "386"); !errors.Is(err, ErrNoAsset) {
		t.Fatalf("386 必须 ErrNoAsset, got %v", err)
	}
	cs, err := PickChecksums(rel)
	if err != nil || cs.Name != "checksums.txt" {
		t.Fatalf("校验文件挑选失败: %v %+v", err, cs)
	}
	if _, err := PickChecksums(Release{Tag: "v0.7.0"}); !errors.Is(err, ErrNoChecksum) {
		t.Fatalf("缺校验文件必须 ErrNoChecksum, got %v", err)
	}
}

func TestSameOrigin(t *testing.T) {
	cases := []struct {
		src, raw string
		want     bool
	}{
		{"https://api.github.com/repos/i2534/sshore", "https://github.com/i2534/sshore/releases/download/v0.7.0/a.tar.gz", true},
		{"https://api.github.com/repos/i2534/sshore", "https://objects.githubusercontent.com/github-production-release-asset/x", true},
		{"https://api.github.com/repos/i2534/sshore", "https://evil.example.com/a.tar.gz", false},
		{"https://mirror.corp:8443/api/repos/sshore", "https://mirror.corp:8443/files/a.tar.gz", true},
		{"https://mirror.corp:8443/api/repos/sshore", "https://other.corp/a.tar.gz", false},
	}
	for _, c := range cases {
		if got := SameOrigin(c.src, c.raw); got != c.want {
			t.Errorf("SameOrigin(%q, %q) = %v, want %v", c.src, c.raw, got, c.want)
		}
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

```bash
go test ./internal/update/ -run "TestLatest|TestPickArchive|TestSameOrigin" -v
```

Expected: FAIL —— `undefined: Client`。

- [ ] **Step 3: 实现 source.go**

```go
package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// DefaultSource 是内置更新源（GitHub API）。
const DefaultSource = "https://api.github.com/repos/i2534/sshore"

var (
	// ErrRateLimited 表示更新源限流（403 + X-RateLimit-Remaining: 0，或 429）。
	ErrRateLimited = errors.New("更新源限流")
	// ErrCheckFailed 表示检查失败（网络、非 200、坏 JSON 等）。
	ErrCheckFailed = errors.New("检查更新失败")
	// ErrNoAsset 表示该 Release 没有本平台产物。
	ErrNoAsset = errors.New("本平台暂无可用包")
	// ErrNoChecksum 表示该 Release 没有校验文件。
	ErrNoChecksum = errors.New("该版本未提供校验文件")
)

// Doer 让 HTTP 依赖可注入（生产用 *http.Client，测试用 httptest）。
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// Asset 是 Release 里的一个产物。
type Asset struct {
	Name string
	URL  string
}

// Release 是 /releases/latest 的最小投影。
type Release struct {
	Tag         string
	Notes       string
	PublishedAt string
	Assets      []Asset
}

// Client 是更新源客户端。
type Client struct {
	HTTP      Doer
	UserAgent string
}

type latestJSON struct {
	TagName     string `json:"tag_name"`
	Body        string `json:"body"`
	PublishedAt string `json:"published_at"`
	Assets      []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

// Latest 读取 <source>/releases/latest 并投影成 Release。
func (c *Client) Latest(ctx context.Context, source string) (Release, error) {
	if c.HTTP == nil {
		return Release{}, fmt.Errorf("%w: 未配置 HTTP 客户端", ErrCheckFailed)
	}
	if source == "" {
		source = DefaultSource
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(source, "/")+"/releases/latest", nil)
	if err != nil {
		return Release{}, fmt.Errorf("%w: %v", ErrCheckFailed, err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return Release{}, fmt.Errorf("%w: %v", ErrCheckFailed, err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusOK:
	case resp.StatusCode == http.StatusTooManyRequests:
		return Release{}, ErrRateLimited
	case resp.StatusCode == http.StatusForbidden && resp.Header.Get("X-RateLimit-Remaining") == "0":
		return Release{}, ErrRateLimited
	default:
		return Release{}, fmt.Errorf("%w: HTTP %d", ErrCheckFailed, resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Release{}, fmt.Errorf("%w: %v", ErrCheckFailed, err)
	}
	var parsed latestJSON
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return Release{}, fmt.Errorf("%w: %v", ErrCheckFailed, err)
	}
	if parsed.TagName == "" {
		return Release{}, fmt.Errorf("%w: 缺少 tag_name", ErrCheckFailed)
	}
	out := Release{Tag: parsed.TagName, Notes: parsed.Body, PublishedAt: parsed.PublishedAt}
	for _, a := range parsed.Assets {
		if a.Name == "" || a.URL == "" {
			continue
		}
		out.Assets = append(out.Assets, Asset{Name: a.Name, URL: a.URL})
	}
	return out, nil
}
```

- [ ] **Step 4: 实现 asset.go**

```go
package update

import (
	"fmt"
	"net/url"
	"strings"
)

// PickArchive 挑选本平台产物：名字需同时含 goos 与 goarch，并以平台后缀结尾。
func PickArchive(rel Release, goos, goarch string) (Asset, error) {
	suffix := ".tar.gz"
	if goos == "windows" {
		suffix = ".zip"
	}
	for _, a := range rel.Assets {
		if !strings.HasSuffix(a.Name, suffix) {
			continue
		}
		if !strings.Contains(a.Name, goos) || !strings.Contains(a.Name, goarch) {
			continue
		}
		return a, nil
	}
	return Asset{}, fmt.Errorf("%w: 需要 %s-%s 的 %s", ErrNoAsset, goos, goarch, suffix)
}

// PickChecksums 挑选校验文件（按文件名精确匹配）。
func PickChecksums(rel Release) (Asset, error) {
	for _, a := range rel.Assets {
		if a.Name == "checksums.txt" {
			return a, nil
		}
	}
	return Asset{}, ErrNoChecksum
}

// SameOrigin 判断下载 URL 是否在受信主机集合内。
// 默认源是 api.github.com，而资产实际在 github.com / *.githubusercontent.com 上，
// 所以不能做字面 host 相等（spec §10.1）。
func SameOrigin(source, rawURL string) bool {
	su, err := url.Parse(strings.TrimSpace(source))
	if err != nil || su.Host == "" {
		return false
	}
	du, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || du.Host == "" {
		return false
	}
	if du.Scheme != "https" && !(du.Scheme == "http" && isLoopbackHost(du.Hostname())) {
		return false
	}
	if strings.EqualFold(du.Host, su.Host) {
		return true
	}
	if strings.EqualFold(su.Hostname(), "api.github.com") {
		host := strings.ToLower(du.Hostname())
		return host == "github.com" || host == "codeload.github.com" || strings.HasSuffix(host, ".githubusercontent.com")
	}
	return false
}

func isLoopbackHost(h string) bool {
	h = strings.ToLower(h)
	return h == "127.0.0.1" || h == "localhost" || h == "::1"
}
```

- [ ] **Step 5: 运行测试确认通过**

```bash
go test ./internal/update/ -run "TestLatest|TestPickArchive|TestSameOrigin" -v
```

Expected: PASS（6 个状态映射子用例 + 5 个同源子用例）。

- [ ] **Step 6: 提交**

```bash
git add internal/update/source.go internal/update/asset.go internal/update/source_test.go internal/update/asset_test.go
git commit -m "feat(update): GitHub API 客户端、错误哨兵、资产挑选与受信下载主机"
```

---

---

### Task 4: download.go —— 流式下载、进度节流、超时、空间检查

**Files:**
- Create: `internal/update/download.go`
- Test: `internal/update/download_test.go`

**Interfaces:**
- Consumes: `Client`、`Doer`（Task 3）
- Produces: `type DownloadOpt struct{ IdleTimeout, Throttle time.Duration; Progress func(done, total int64) }`；`func (c *Client) Download(ctx context.Context, url, dest string, opt DownloadOpt) error`；`func FreeSpace(dir string) (int64, error)`

- [ ] **Step 1: 写失败测试**

```go
package update

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestDownloadProgressIsMonotonicAndThrottled(t *testing.T) {
	const total = 3 * 1024 * 1024
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(total))
		chunk := make([]byte, 256*1024)
		for i := 0; i < 12; i++ {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	var mu sync.Mutex
	var seen []int64
	client := &Client{HTTP: srv.Client()}
	dest := filepath.Join(t.TempDir(), "a.part")
	err := client.Download(context.Background(), srv.URL, dest, DownloadOpt{
		IdleTimeout: 5 * time.Second,
		Throttle:    time.Millisecond,
		Progress: func(done, got int64) {
			mu.Lock()
			defer mu.Unlock()
			if got != total {
				t.Errorf("total = %d, want %d", got, total)
			}
			seen = append(seen, done)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) == 0 || seen[len(seen)-1] != total {
		t.Fatalf("最后一帧必须是完成量: %v", seen)
	}
	for i := 1; i < len(seen); i++ {
		if seen[i] < seen[i-1] {
			t.Fatalf("进度必须单调: %v", seen)
		}
	}
}

func TestDownloadCancelAndIdleTimeoutRemovePartial(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "1024")
		_, _ = w.Write([]byte("start"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		time.Sleep(3 * time.Second)
	}))
	defer slow.Close()

	// ① 空闲超时：IdleTimeout 200ms 远小于服务端 3s 静默
	dest := filepath.Join(t.TempDir(), "idle.part")
	err := (&Client{HTTP: slow.Client()}).Download(context.Background(), slow.URL, dest,
		DownloadOpt{IdleTimeout: 200 * time.Millisecond})
	if err == nil {
		t.Fatal("空闲超时必须报错")
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Fatal("失败后必须删除半截文件")
	}

	// ② ctx 取消
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	dest2 := filepath.Join(t.TempDir(), "cancel.part")
	err = (&Client{HTTP: slow.Client()}).Download(ctx, slow.URL, dest2, DownloadOpt{IdleTimeout: 5 * time.Second})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("取消应返回 context.Canceled, got %v", err)
	}
	if _, statErr := os.Stat(dest2); !os.IsNotExist(statErr) {
		t.Fatal("取消后必须删除半截文件")
	}
}

func TestFreeSpace(t *testing.T) {
	n, err := FreeSpace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if n <= 0 {
		t.Fatalf("可用空间应大于 0: %d", n)
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

```bash
go test ./internal/update/ -run "TestDownload|TestFreeSpace" -v
```

Expected: FAIL —— `undefined: DownloadOpt`。

- [ ] **Step 3: 实现（要点：边下边算哈希由调用方用 `FileSHA256` 复核；这里保证进度/超时/清理语义）**

```go
package update

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync/atomic"
	"time"
)

// DownloadOpt 控制下载行为；IdleTimeout 是「连续无字节」上限，Throttle 是进度最小间隔。
type DownloadOpt struct {
	IdleTimeout time.Duration
	Throttle    time.Duration
	Progress    func(done, total int64)
}

// Download 把 url 流式写到 dest（失败/取消会删除 dest）。
func (c *Client) Download(ctx context.Context, url, dest string, opt DownloadOpt) error {
	if c.HTTP == nil {
		return errors.New("未配置 HTTP 客户端")
	}
	if opt.IdleTimeout <= 0 {
		opt.IdleTimeout = 30 * time.Second
	}
	if opt.Throttle <= 0 {
		opt.Throttle = 200 * time.Millisecond
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载失败：HTTP %d", resp.StatusCode)
	}
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	fail := func(e error) error {
		_ = out.Close()
		_ = os.Remove(dest)
		return e
	}
	// 空闲看门狗：每次成功读到字节就重置；触发即标记原因并取消 ctx。
	var idleHit atomic.Bool
	watchdog := time.AfterFunc(opt.IdleTimeout, func() { idleHit.Store(true); cancel() })
	defer watchdog.Stop()
	total := resp.ContentLength
	var done int64
	last := time.Now()
	buf := make([]byte, 256*1024)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
				return fail(werr)
			}
			done += int64(n)
			watchdog.Reset(opt.IdleTimeout)
			if opt.Progress != nil && time.Since(last) >= opt.Throttle {
				last = time.Now()
				opt.Progress(done, total)
			}
		}
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				break
			}
			if errors.Is(rerr, context.Canceled) && ctx.Err() != nil {
				if idleHit.Load() {
					return fail(fmt.Errorf("下载空闲超时（%s 无数据）", opt.IdleTimeout))
				}
				return fail(ctx.Err())
			}
			return fail(rerr)
		}
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(dest)
		return err
	}
	if opt.Progress != nil {
		opt.Progress(done, total)
	}
	return nil
}

// FreeSpace 的实现按平台拆到 freespace_unix.go / freespace_windows.go（见下），
// 这样 download.go 不引入平台专有符号，GOOS=windows 与 linux 都能编译。
```

**两平台实现**（`download.go` 不再内联 FreeSpace）：

```go
// freespace_unix.go
//go:build !windows

package update

import "golang.org/x/sys/unix"

// FreeSpace 返回 dir 所在文件系统的可用字节数（Linux/BSD 用 Statfs）。
func FreeSpace(dir string) (int64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return 0, err
	}
	return int64(st.Bavail) * int64(st.Bsize), nil
}
```

```go
// freespace_windows.go
//go:build windows

package update

import "golang.org/x/sys/windows"

func FreeSpace(dir string) (int64, error) {
	var free, total, avail uint64
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return 0, err
	}
	if err := windows.GetDiskFreeSpaceEx(p, &free, &total, &avail); err != nil {
		return 0, err
	}
	return int64(free), nil
}
```

- [ ] **Step 4: 运行测试确认通过（含 `GOOS=windows go vet ./internal/update/`）**

```bash
go test ./internal/update/ -run "TestDownload|TestFreeSpace" -v
GOOS=windows go vet ./internal/update/
```

Expected: 测试 PASS；`go vet` 无输出（证明 Windows 侧文件能编译）。

- [ ] **Step 5: 提交**

```bash
git add internal/update/download.go internal/update/download_test.go internal/update/freespace_unix.go internal/update/freespace_windows.go
git commit -m "feat(update): 流式下载（进度节流/空闲超时/半截清理）与磁盘空间检查"
```

---

---

### Task 5: plan.go —— 替换计划、备份候选、失败残留恢复

**Files:**
- Create: `internal/update/plan.go`
- Test: `internal/update/plan_test.go`

**Interfaces:**
- Consumes: `Class`/`Compare`/`ParsePendingName`（Task 1）
- Produces: `type Plan struct{ Target, Pending, Backup, Sidecar, LogPath, ExeDir string; Size int64; Wait time.Duration }`；`const DefaultWait = 60 * time.Second`；`func PlanFor(goos, exePath, fromVer, toVer string, size int64, wait time.Duration) Plan`；`func ResumePending(exeDir, current string) (Plan, bool)`；`func IsPendingName(name, current string) bool`

- [ ] **Step 1: 写失败测试**

```go
package update

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPlanForNaming(t *testing.T) {
	exe := filepath.Join("/opt/sshore", "sshore")
	p := PlanFor("linux", exe, "v0.6.0", "v0.7.0", 123, DefaultWait)
	if p.Pending != filepath.Join("/opt/sshore", "sshore.v0.7.0") {
		t.Fatalf("pending 名错误: %s", p.Pending)
	}
	if p.Backup != filepath.Join("/opt/sshore", "sshore.v0.6.0") {
		t.Fatalf("backup 名错误: %s", p.Backup)
	}
	if p.Sidecar != p.Pending+".sha256" {
		t.Fatalf("sidecar 名错误: %s", p.Sidecar)
	}
	if p.LogPath != filepath.Join("/opt/sshore", "sshore-update.log") {
		t.Fatalf("日志路径错误: %s", p.LogPath)
	}
	win := PlanFor("windows", "C:/app/sshore.exe", "v0.6.0", "v0.7.0", 1, DefaultWait)
	if filepath.Base(win.Pending) != "sshore.v0.7.0.exe" {
		t.Fatalf("windows pending 名错误: %s", win.Pending)
	}
}

func TestPlanForDescribeAndDevBackupNames(t *testing.T) {
	exe := "/opt/sshore/sshore"
	describe := PlanFor("linux", exe, "v0.6.0-80-gc2d2a36", "v0.7.0", 1, 5*time.Second)
	if describe.Backup != "/opt/sshore/sshore.v0.6.0-80-gc2d2a36" {
		t.Fatalf("describe 备份名错误: %s", describe.Backup)
	}
	if describe.Wait != 5*time.Second {
		t.Fatalf("wait 未透传: %v", describe.Wait)
	}
	dev := PlanFor("linux", exe, "dev", "v0.7.0", 1, DefaultWait)
	if !hasDevTimestamp(filepath.Base(dev.Backup)) {
		t.Fatalf("dev 备份名必须带时间戳: %s", dev.Backup)
	}
}

func hasDevTimestamp(name string) bool {
	const prefix = "sshore.dev-"
	if len(name) < len(prefix)+15 {
		return false
	}
	for _, r := range name[len(prefix):] {
		if r != '-' && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

func TestIsPendingNameUsesVersionOrder(t *testing.T) {
	cases := []struct {
		name, current string
		want          bool
	}{
		{"sshore.v0.7.0", "v0.6.0", true},
		{"sshore.v0.6.0", "v0.7.0", false},
		{"sshore.v0.6.0", "v0.6.0", false},
		{"sshore.v0.6.0-80-gc2d2a36", "v0.6.0", false},
		{"sshore.dev-20260919-101112", "v0.6.0", false},
		{"sshore.v0.7.0", "v0.6.0-80-gc2d2a36", true},
		{"sshore.exe", "v0.6.0", false},
		{"sshore-update.sh", "v0.6.0", false},
		{"sshore.v0.7.0.sha256", "v0.6.0", false},
	}
	for _, c := range cases {
		if got := IsPendingName(c.name, c.current); got != c.want {
			t.Errorf("IsPendingName(%q, %q) = %v, want %v", c.name, c.current, got, c.want)
		}
	}
}

func TestResumePending(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"sshore", "sshore.v0.6.0", "sshore.v0.7.0", "sshore.v0.7.0.sha256", "sshore-update.sh"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	p, ok := ResumePending(dir, "v0.6.0")
	if !ok {
		t.Fatal("应识别出待安装文件 sshore.v0.7.0")
	}
	if filepath.Base(p.Pending) != "sshore.v0.7.0" || p.Size != 0 {
		t.Fatalf("恢复计划错误: %+v", p)
	}
	if filepath.Base(p.Target) != "sshore" || p.ExeDir != dir {
		t.Fatalf("目标/目录错误: %+v", p)
	}
	dir2 := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir2, "sshore"), []byte("x"), 0o755)
	_ = os.WriteFile(filepath.Join(dir2, "sshore.v0.6.0"), []byte("x"), 0o755)
	if _, ok := ResumePending(dir2, "v0.7.0"); ok {
		t.Fatal("备份不能被当成待安装文件")
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

```bash
go test ./internal/update/ -run "TestPlanFor|TestIsPendingName|TestResumePending" -v
```

Expected: FAIL —— `undefined: PlanFor`。

- [ ] **Step 3: 实现**

```go
package update

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DefaultWait 是脚本等待旧进程退出的默认上限。
const DefaultWait = 60 * time.Second

// Plan 是一次升级要用到的全部路径与参数。
type Plan struct {
	Target  string
	Pending string
	Backup  string
	Sidecar string
	LogPath string
	ExeDir  string
	Size    int64
	Wait    time.Duration
}

// PlanFor 按平台与版本算出命名；非 release 的当前版本用时间戳备份名。
func PlanFor(goos, exePath, fromVer, toVer string, size int64, wait time.Duration) Plan {
	if wait <= 0 {
		wait = DefaultWait
	}
	dir := filepath.Dir(exePath)
	ext := ""
	if goos == "windows" {
		ext = ".exe"
	}
	pending := filepath.Join(dir, "sshore."+Base(toVer)+ext)
	var backup string
	switch Class(fromVer) {
	case KindClean, KindDescribe:
		// 干净 tag 与 git describe 串都能唯一标识被替换的版本（spec §8.4）
		backup = filepath.Join(dir, "sshore."+Base(fromVer)+ext)
	default:
		// dev / 未知版本没有唯一标识，用时间戳
		backup = filepath.Join(dir, "sshore.dev-"+time.Now().Format("20060102-150405")+ext)
	}
	return Plan{
		Target:  exePath,
		Pending: pending,
		Backup:  backup,
		Sidecar: pending + ".sha256",
		LogPath: filepath.Join(dir, "sshore-update.log"),
		ExeDir:  dir,
		Size:    size,
		Wait:    wait,
	}
}

// IsPendingName 是 pending 的唯一判别（spec §7.5）：干净 tag 且版本序大于当前版本。
func IsPendingName(name, current string) bool {
	ver, ok := ParsePendingName(name)
	if !ok || Class(ver) != KindClean {
		return false
	}
	switch Class(current) {
	case KindClean, KindDescribe:
	default:
		return false
	}
	return Compare(ver, current) > 0
}

// ResumePending 在启动自检时找出上次未完成的待安装文件（Size 置 0，由磁盘决定）。
func ResumePending(exeDir, current string) (Plan, bool) {
	entries, err := os.ReadDir(exeDir)
	if err != nil {
		return Plan{}, false
	}
	best := ""
	for _, e := range entries {
		if e.IsDir() || !IsPendingName(e.Name(), current) {
			continue
		}
		if best == "" {
			best = e.Name()
			continue
		}
		bv, _ := ParsePendingName(best)
		v, _ := ParsePendingName(e.Name())
		if Compare(v, bv) > 0 {
			best = e.Name()
		}
	}
	if best == "" {
		return Plan{}, false
	}
	ext := ""
	if strings.HasSuffix(best, ".exe") {
		ext = ".exe"
	}
	toVer, _ := ParsePendingName(best)
	goos := "linux"
	if ext == ".exe" {
		goos = "windows"
	}
	return PlanFor(goos, filepath.Join(exeDir, "sshore"+ext), current, toVer, 0, 0), true
}
```

- [ ] **Step 4: 运行测试确认通过**

```bash
go test ./internal/update/ -run "TestPlanFor|TestIsPendingName|TestResumePending" -v
```

Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/update/plan.go internal/update/plan_test.go
git commit -m "feat(update): 替换计划、pending 版本序判别与残留恢复"
```

---

---

### Task 6: 升级脚本 + script.go + .gitattributes

**Files:**
- Create: `internal/update/scripts/update.sh`、`internal/update/scripts/update.cmd`、`internal/update/script.go`、`.gitattributes`
- Test: `internal/update/script_test.go`

**Interfaces:**
- Consumes: `Plan`（Task 5）
- Produces: `func ScriptName(goos string) string`；`func ScriptBytes(goos string) ([]byte, error)`；`func ScriptArgs(p Plan, pid int) []string`（Linux）；`func ScriptEnv(p Plan, pid int) []string`（Windows）；`func StartDetached(goos, scriptPath string, args, env []string) error`

- [ ] **Step 1: 写脚本（Linux，纯 sh；参数走 argv）**

```sh
#!/bin/sh
# sshore 升级脚本：由主程序按本次升级写出并传参，不要手工修改。
# 参数：--pid <旧PID> --target <正式二进制> --pending <待安装文件> --backup <备份路径>
#       --size <字节> --log <日志> --wait <秒>
# 日志协议（末行由主程序残留自检读取）：RESULT=ok 或 RESULT=fail:<step>
set -u

# 解析自身绝对路径：脚本会在替换前 cd 到目标目录，$0 若是相对路径就再也删不掉自己（已实测）。
SELF=$(cd "$(dirname "$0")" 2>/dev/null && pwd)/$(basename "$0")

PID=""; TARGET=""; PENDING=""; BACKUP=""; SIZE=""; LOG=""; WAIT=60
while [ $# -gt 0 ]; do
  case "$1" in
    --pid) PID="$2"; shift 2 ;;
    --target) TARGET="$2"; shift 2 ;;
    --pending) PENDING="$2"; shift 2 ;;
    --backup) BACKUP="$2"; shift 2 ;;
    --size) SIZE="$2"; shift 2 ;;
    --log) LOG="$2"; shift 2 ;;
    --wait) WAIT="$2"; shift 2 ;;
    *) shift ;;
  esac
done

fail() {
  printf "STEP=%s ERR=%s\nRESULT=fail:%s\n" "$1" "$2" "$1" >> "$LOG"
  # 参数类失败按 spec §8.2 退 2，其余（0/3/5/6/launch/wait）退 3
  case "$1" in
    args) exit 2 ;;
    *) exit 3 ;;
  esac
}

[ -n "$LOG" ] || LOG=/dev/null
: > "$LOG" 2>/dev/null || true

case "$PID" in ""|*[!0-9]*) fail args "pid 非法" ;; esac
case "$SIZE" in ""|*[!0-9]*) fail args "size 非法" ;; esac
case "$WAIT" in ""|*[!0-9]*) fail args "wait 非法" ;; esac
[ -n "$TARGET" ] || fail args "target 缺失"
[ -n "$PENDING" ] || fail args "pending 缺失"
[ -f "$TARGET" ] || fail args "target 不存在"
[ -f "$PENDING" ] || fail args "pending 不存在"

DIR=$(dirname "$TARGET")
cd "$DIR" || fail 0 "无法进入目标目录"
PB=$(basename "$PENDING")
BB=$(basename "$BACKUP")

# 2) 等旧进程退出（上限 WAIT 秒）
waited=0
while kill -0 "$PID" 2>/dev/null; do
  [ "$waited" -lt "$WAIT" ] || fail wait "等待旧进程退出超时"
  sleep 1
  waited=$((waited+1))
done

# 3) 自检 pending 存在且大小一致
[ -f "$PENDING" ] || fail 3 "pending 已消失"
actual=$(wc -c < "$PENDING" | tr -d " ")
[ "$actual" = "$SIZE" ] || fail 3 "pending 大小不符：$actual != $SIZE"

# 4) 清理更早备份（只留最近一份；绝不删 pending、sidecar、脚本、日志）
for f in "$DIR"/sshore.v* "$DIR"/sshore.dev-*; do
  [ -e "$f" ] || continue
  b=$(basename "$f")
  [ "$b" = "$PB" ] && continue
  [ "$b" = "$BB" ] && continue
  case "$b" in *.sha256|*.log|*-update.sh|*-update.cmd) continue ;; esac
  rm -f "$f" || true
done

# 5) 旧二进制改名备份
mv -f "$TARGET" "$BACKUP" || fail 5 "备份旧二进制失败"
# 6) 待安装文件改名正式名（失败则把备份改回去，避免应用消失）
if ! mv -f "$PENDING" "$TARGET"; then
  mv -f "$BACKUP" "$TARGET" 2>/dev/null || true
  fail 6 "替换正式名失败"
fi
chmod +x "$TARGET" 2>/dev/null || true

# 8) 启动新版并做 3 秒存活探测；失败则回滚
if command -v setsid >/dev/null 2>&1; then
  setsid "$TARGET" >/dev/null 2>&1 &
else
  nohup "$TARGET" >/dev/null 2>&1 &
fi
NEWPID=$!
sleep 3
if ! kill -0 "$NEWPID" 2>/dev/null; then
  mv -f "$TARGET" "$PENDING" 2>/dev/null || true
  mv -f "$BACKUP" "$TARGET" 2>/dev/null || true
  if command -v setsid >/dev/null 2>&1; then
    setsid "$TARGET" >/dev/null 2>&1 &
  else
    nohup "$TARGET" >/dev/null 2>&1 &
  fi
  fail launch "新版本启动失败，已回滚到旧版本"
fi

# 9) 成功：写结果、清日志、自删
printf "RESULT=ok\n" >> "$LOG"
rm -f "$LOG" "$SELF"
exit 0
```

- [ ] **Step 2: 写脚本（Windows，纯 cmd；参数走环境变量，见 spec §8.5）**

```bat
@echo off
rem sshore 升级脚本：由主程序按本次升级写出并用环境变量传参，不要手工修改。
rem 变量：SSHORE_PID/SSHORE_TARGET/SSHORE_PENDING/SSHORE_BACKUP/SSHORE_SIZE/SSHORE_LOG/SSHORE_WAIT
rem 日志协议：末行 RESULT=ok 或 RESULT=fail:<step>
setlocal enabledelayedexpansion
set "PID=%SSHORE_PID%"
set "TARGET=%SSHORE_TARGET%"
set "PENDING=%SSHORE_PENDING%"
set "BACKUP=%SSHORE_BACKUP%"
set "SIZE=%SSHORE_SIZE%"
set "LOG=%SSHORE_LOG%"
set "WAIT=%SSHORE_WAIT%"
if "%WAIT%"=="" set "WAIT=60"
if "%LOG%"=="" set "LOG=%TEMP%\sshore-update.log"
type nul > "%LOG%" 2>nul

if "%PID%"=="" goto :args
if "%SIZE%"=="" goto :args
if "%WAIT%"=="" goto :args
rem PID/SIZE/WAIT 必须是正整数（spec §8.2）：拼起来的字符串只含数字才算通过
for %%A in (%PID%) do if "%%A"=="" goto :args
echo %PID%%SIZE%%WAIT%| findstr /r "^[0-9][0-9]*$" >nul || goto :args
if "%TARGET%"=="" goto :args
if "%PENDING%"=="" goto :args
if not exist "%TARGET%" goto :args
if not exist "%PENDING%" goto :args

cd /d "%~dp0" || goto :step0
for %%A in ("%PENDING%") do set "PB=%%~nxA"
for %%A in ("%BACKUP%") do set "BB=%%~nxA"

rem 2) 等旧进程退出
set /a WAITED=0
:wait
tasklist /FI "PID eq %PID%" 2>nul | find "%PID%" >nul
if errorlevel 1 goto :waited
if %WAITED% GEQ %WAIT% goto :waitfail
ping -n 2 127.0.0.1 >nul
set /a WAITED+=1
goto :wait
:waited

rem 3) 自检大小（必须用延迟展开读在同一块里刚 set 的变量）
for %%A in ("%PENDING%") do set "PEND_SIZE=%%~zA"
if not "!PEND_SIZE!"=="%SIZE%" goto :step3

rem 4) 清理更早备份（保留 pending 与本次 backup，且跳过 sidecar/脚本/日志）
for %%f in ("%CD%\sshore.v*") do call :maybe_del "%%~nxf"
for %%f in ("%CD%\sshore.dev-*") do call :maybe_del "%%~nxf"

rem 5) 备份旧二进制
move /y "%TARGET%" "%BACKUP%" >nul || goto :step5
rem 6) 换成新版本（失败则回滚）
move /y "%PENDING%" "%TARGET%" >nul || goto :rollback_replace

rem 8) 启动并做 3 秒存活探测
start "" "%TARGET%"
ping -n 4 127.0.0.1 >nul
for %%A in ("%TARGET%") do set "EXENAME=%%~nxA"
tasklist /FI "IMAGENAME eq !EXENAME!" 2>nul | find /i "!EXENAME!" >nul
if errorlevel 1 goto :rollback_launch

rem 9) 成功
>> "%LOG%" echo RESULT=ok
del "%LOG%" >nul 2>nul
del "%~f0" >nul 2>nul
exit /b 0

:maybe_del
set "N=%~1"
if /i "%N%"=="%PB%" exit /b 0
if /i "%N%"=="%BB%" exit /b 0
if /i "%N:~-7%"==".sha256" exit /b 0
if /i "%N%"=="sshore-update.cmd" exit /b 0
if /i "%N%"=="sshore-update.log" exit /b 0
del "%CD%\%N%" >nul 2>nul
exit /b 0

:rollback_replace
move /y "%BACKUP%" "%TARGET%" >nul 2>nul
goto :fail6

:rollback_launch
move /y "%TARGET%" "%PENDING%" >nul 2>nul
move /y "%BACKUP%" "%TARGET%" >nul 2>nul
start "" "%TARGET%"
>> "%LOG%" echo RESULT=fail:launch
exit /b 3

:args
>> "%LOG%" echo RESULT=fail:args
exit /b 2
:step0
>> "%LOG%" echo RESULT=fail:0
exit /b 3
:waitfail
>> "%LOG%" echo RESULT=fail:wait
exit /b 3
:step3
>> "%LOG%" echo RESULT=fail:3
exit /b 3
:step5
>> "%LOG%" echo RESULT=fail:5
exit /b 3
:fail6
>> "%LOG%" echo RESULT=fail:6
exit /b 3
```

- [ ] **Step 3: 加 .gitattributes（换行符钉死）**

```gitattributes
*.sh  text eol=lf
*.cmd text eol=crlf
*.bat text eol=crlf
```

- [ ] **Step 4: 写失败测试**

```go
package update

import (
	"strings"
	"testing"
	"time"
)

func TestScriptName(t *testing.T) {
	if ScriptName("windows") != "sshore-update.cmd" {
		t.Fatalf("windows 脚本名错误: %s", ScriptName("windows"))
	}
	if ScriptName("linux") != "sshore-update.sh" {
		t.Fatalf("linux 脚本名错误: %s", ScriptName("linux"))
	}
}

func TestScriptBytesAreSafeAndLFOnly(t *testing.T) {
	sh, err := ScriptBytes("linux")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(sh), "\r\n") {
		t.Fatal("update.sh 不能含 CRLF（spec §8.1）")
	}
	for _, banned := range []string{"powershell", "curl", "wget", "certutil", "eval"} {
		if strings.Contains(string(sh), banned) {
			t.Errorf("update.sh 不得出现 %q", banned)
		}
	}
	cmd, err := ScriptBytes("windows")
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{"powershell", "curl", "wget", "certutil"} {
		if strings.Contains(strings.ToLower(string(cmd)), banned) {
			t.Errorf("update.cmd 不得出现 %q", banned)
		}
	}
	for _, want := range []string{"SSHORE_PENDING", "RESULT=ok", "enabledelayedexpansion"} {
		if !strings.Contains(string(cmd), want) {
			t.Errorf("update.cmd 缺少 %q", want)
		}
	}
}

func TestScriptArgsAndEnv(t *testing.T) {
	p := PlanFor("linux", "/opt/sshore/sshore", "v0.6.0", "v0.7.0", 42, 30*time.Second)
	args := ScriptArgs(p, 4242)
	joined := strings.Join(args, " ")
	for _, want := range []string{"--pid 4242", "--size 42", "--wait 30", p.Pending, p.Backup, p.LogPath} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv 缺少 %q: %v", want, args)
		}
	}
	env := ScriptEnv(p, 4242)
	want := map[string]string{
		"SSHORE_PID":     "4242",
		"SSHORE_TARGET":  p.Target,
		"SSHORE_PENDING": p.Pending,
		"SSHORE_BACKUP":  p.Backup,
		"SSHORE_SIZE":    "42",
		"SSHORE_LOG":     p.LogPath,
		"SSHORE_WAIT":    "30",
	}
	got := map[string]string{}
	for _, kv := range env {
		i := strings.Index(kv, "=")
		got[kv[:i]] = kv[i+1:]
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("env[%s] = %q, want %q", k, got[k], v)
		}
	}
}
```

- [ ] **Step 5: 运行测试确认失败**

```bash
go test ./internal/update/ -run "TestScript" -v
```

Expected: FAIL —— `undefined: ScriptName`。

- [ ] **Step 6: 实现 script.go**

```go
package update

import (
	_ "embed"
	"fmt"
	"os/exec"
	"strconv"
	"syscall"
)

//go:embed scripts/update.sh
var scriptSH []byte

//go:embed scripts/update.cmd
var scriptCMD []byte

// ScriptName 返回该平台的脚本文件名。
func ScriptName(goos string) string {
	if goos == "windows" {
		return "sshore-update.cmd"
	}
	return "sshore-update.sh"
}

// ScriptBytes 返回嵌入的脚本内容（按 GOOS 选择）。
func ScriptBytes(goos string) ([]byte, error) {
	if goos == "windows" {
		if len(scriptCMD) == 0 {
			return nil, fmt.Errorf("未嵌入 update.cmd")
		}
		return scriptCMD, nil
	}
	if len(scriptSH) == 0 {
		return nil, fmt.Errorf("未嵌入 update.sh")
	}
	return scriptSH, nil
}

// ScriptArgs 生成 Linux 侧 argv（execve 不经 shell，天然零插值）。
func ScriptArgs(p Plan, pid int) []string {
	return []string{
		"--pid", strconv.Itoa(pid),
		"--target", p.Target,
		"--pending", p.Pending,
		"--backup", p.Backup,
		"--size", strconv.FormatInt(p.Size, 10),
		"--log", p.LogPath,
		"--wait", strconv.Itoa(int(p.Wait / 1e9)),
	}
}

// ScriptEnv 生成 Windows 侧环境变量（argv 经 cmd 转发不满足零插值，见 spec §8.5）。
func ScriptEnv(p Plan, pid int) []string {
	return []string{
		"SSHORE_PID=" + strconv.Itoa(pid),
		"SSHORE_TARGET=" + p.Target,
		"SSHORE_PENDING=" + p.Pending,
		"SSHORE_BACKUP=" + p.Backup,
		"SSHORE_SIZE=" + strconv.FormatInt(p.Size, 10),
		"SSHORE_LOG=" + p.LogPath,
		"SSHORE_WAIT=" + strconv.Itoa(int(p.Wait/1e9)),
	}
}

// StartDetached 分离启动脚本：两侧都不依赖父进程存活。
func StartDetached(goos, scriptPath string, args, env []string) error {
	var cmd *exec.Cmd
	if goos == "windows" {
		cmd = exec.Command("cmd", "/d", "/c", scriptPath)
		cmd.Env = append(cmd.Environ(), env...)
	} else {
		cmd = exec.Command(scriptPath, args...)
	}
	setDetached(cmd) // 平台差异（新会话 / 隐藏窗口）见 start_unix.go 与 start_windows.go
	if err := cmd.Start(); err != nil {
		return err
	}
	// 不 Wait：脚本要在本进程退出后继续跑。
	return cmd.Process.Release()
}
```

**`SysProcAttr` 必须按平台拆文件**（Linux 的 `syscall.SysProcAttr` 没有 `HideWindow`/`CreationFlags` 字段，写在同一个文件里 `make ci` 的 Linux 编译会直接失败 —— 两阶段评审实测）：

```go
// start_unix.go
//go:build !windows

package update

import (
	"os/exec"
	"syscall"
)

// setDetached 让脚本进入新会话，主进程退出后不受影响。
func setDetached(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
```

```go
// start_windows.go
//go:build windows

package update

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// setDetached 隐藏控制台窗口；子进程不随父进程退出（见 spec §8.5）。
// 常量取自 golang.org/x/sys/windows —— syscall 包没有 CREATE_NO_WINDOW（spec §4 F7）。
func setDetached(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
}
```

- [ ] **Step 7: 运行测试确认通过，并检查换行符**

```bash
go test ./internal/update/ -run "TestScript" -v
git add -A && git check-attr text eol -- internal/update/scripts/update.sh internal/update/scripts/update.cmd
```

Expected: 测试 PASS；`git check-attr` 显示 `update.sh: text: set`、`eol: lf`，`update.cmd: eol: crlf`。

- [ ] **Step 8: 提交**

```bash
git add .gitattributes internal/update/scripts internal/update/script.go internal/update/script_test.go internal/update/start_unix.go internal/update/start_windows.go
git commit -m "feat(update): 嵌入式升级脚本（sh/cmd）+ 参数/环境构造 + 分离启动"
```

---

---

### Task 7: lock.go —— ExeDir 排他锁（防两实例互相 rename）

**Files:**
- Create: `internal/update/lock.go`、`internal/update/lock_unix.go`、`internal/update/lock_windows.go`
- Test: `internal/update/lock_test.go`

**Interfaces:**
- Consumes: 无
- Produces: `func Acquire(exeDir string) (release func(), err error)`

- [ ] **Step 1: 写失败测试**

```go
//go:build !windows

package update

import "testing"

func TestAcquireIsExclusiveWithinProcess(t *testing.T) {
	dir := t.TempDir()
	release, err := Acquire(dir)
	if err != nil {
		t.Fatalf("首次加锁失败: %v", err)
	}
	// 第二次必须失败：两个实例同时 apply 会互相 rename，可能弄丢二进制（spec §7.6）
	if _, err := Acquire(dir); err == nil {
		t.Fatal("同一目录重复加锁必须失败")
	}
	release()
	release2, err := Acquire(dir)
	if err != nil {
		t.Fatalf("释放后应能再次加锁: %v", err)
	}
	release2()
}
```

- [ ] **Step 2: 运行测试确认失败**

```bash
go test ./internal/update/ -run TestAcquire -v
```

Expected: FAIL —— `undefined: Acquire`。

- [ ] **Step 3: 实现（Linux 用 flock，Windows 用命名互斥体；锁随进程退出自动释放）**

```go
// lock_unix.go
//go:build !windows

package update

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// Acquire 在 ExeDir 上取排他锁（LOCK_EX|LOCK_NB）。
func Acquire(exeDir string) (func(), error) {
	path := filepath.Join(exeDir, ".sshore-update.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("另一个实例正在升级：%w", err)
	}
	return func() {
		_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
		_ = f.Close()
	}, nil
}
```

```go
// lock_windows.go
//go:build windows

package update

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"strings"

	"golang.org/x/sys/windows"
)

// Acquire 用命名互斥体加锁：句柄随进程关闭，脚本不需要参与。
func Acquire(exeDir string) (func(), error) {
	sum := sha1.Sum([]byte(strings.ToLower(exeDir)))
	name, err := windows.UTF16PtrFromString("Global\\sshore-update-" + hex.EncodeToString(sum[:8]))
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateMutex(nil, false, name)
	if err != nil {
		return nil, fmt.Errorf("创建互斥体失败：%w", err)
	}
	if windows.GetLastError() == windows.ERROR_ALREADY_EXISTS {
		_ = windows.CloseHandle(h)
		return nil, fmt.Errorf("另一个实例正在升级")
	}
	return func() { _ = windows.CloseHandle(h) }, nil
}
```

- [ ] **Step 4: 运行测试确认通过 + Windows 侧编译**

```bash
go test ./internal/update/ -run TestAcquire -v
GOOS=windows go vet ./internal/update/
```

Expected: PASS；vet 无输出。

- [ ] **Step 5: 提交**

```bash
git add internal/update/lock*.go
git commit -m "feat(update): ExeDir 排他锁（flock / 命名互斥体）"
```

---

### Task 8: service.go（一）—— 状态机、守卫、检查与下载

**Files:**
- Create: `internal/update/service.go`
- Test: `internal/update/service_test.go`

**Interfaces:**
- Consumes: Tasks 1–7 的全部导出符号
- Produces: `type Settings struct{ Auto bool; Interval time.Duration; Source, Skipped string }`；`type Options struct{ Goos, Goarch, Version, ExePath string; Doer Doer; Launch func(goos, path string, args, env []string) error; Acquire func(string) (func(), error); Emit func(string, any); Config func() Settings; Save func(Settings) error; Now func() time.Time }`；`type UpdateInfo struct{...}`（字段见 spec §5.2）；状态常量 `StateIdle`…`StateDisabled`；`func New(Options) *Service`；`func (s *Service) Info() UpdateInfo`；`Check`/`StartDownload`/`CancelDownload`/`ApplyAndRestart`/`DiscardPending`/`SkipVersion`/`ClearSkipped`

- [ ] **Step 1: 写失败测试（守卫与门卫是本任务的核心价值）**

```go
package update

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newSvc(t *testing.T, version string, opts ...func(*Options)) (*Service, *Options) {
	t.Helper()
	o := Options{
		Goos: "linux", Goarch: "amd64", Version: version,
		ExePath: "/tmp/sshore/sshore",
		Doer:    &http.Client{},
		Launch:  func(string, string, []string, []string) error { return nil },
		Acquire: func(string) (func(), error) { return func() {}, nil },
		Emit:    func(string, any) {},
		Config:  func() Settings { return Settings{Auto: true, Interval: time.Hour, Source: ""} },
		Save:    func(Settings) error { return nil },
		Now:     time.Now,
	}
	for _, f := range opts {
		f(&o)
	}
	return New(o), &o
}

func TestDisabledGateMakesNoRequest(t *testing.T) {
	var calls int32
	svc, o := newSvc(t, "dev", func(o *Options) {
		o.Doer = doerFunc(func(*http.Request) (*http.Response, error) {
			atomic.AddInt32(&calls, 1)
			return nil, errors.New("不该被调用")
		})
	})
	_ = o
	info, err := svc.Check(context.Background(), false)
	if err != nil {
		t.Fatalf("门卫不应报错: %v", err)
	}
	if info.State != StateDisabled {
		t.Fatalf("非 release 构建自动检查应 disabled, got %s", info.State)
	}
	if n := atomic.LoadInt32(&calls); n != 0 {
		t.Fatalf("门卫拦截时不得发请求, calls=%d", n)
	}
}

func TestCheckRejectedWhileDownloadingOrReady(t *testing.T) {
	svc, _ := newSvc(t, "v0.6.0")
	svc.mu.Lock()
	svc.info.State = StateDownloading
	svc.mu.Unlock()
	if _, err := svc.Check(context.Background(), true); !errors.Is(err, ErrBusy) {
		t.Fatalf("下载中必须拒绝检查, got %v", err)
	}
	svc.mu.Lock()
	svc.info.State = StateReady
	svc.info.ReadyPath = "/tmp/sshore/sshore.v0.7.0"
	svc.mu.Unlock()
	if _, err := svc.Check(context.Background(), true); !errors.Is(err, ErrBusy) {
		t.Fatalf("ready 期间必须拒绝检查（否则会抹掉 ReadyPath）")
	}
	// applying 是终态；DoD#10 变异 ④ 就是删掉这条守卫，必须有测试抓住
	svc.mu.Lock()
	svc.info.State = StateApplying
	svc.mu.Unlock()
	if _, err := svc.Check(context.Background(), true); !errors.Is(err, ErrBusy) {
		t.Fatalf("applying 期间必须拒绝检查")
	}
}

func TestApplyRequiresReadyAndIsNotReentrant(t *testing.T) {
	svc, o := newSvc(t, "v0.6.0")
	if err := svc.ApplyAndRestart(context.Background()); err == nil {
		t.Fatal("非 ready 状态必须拒绝 apply")
	}
	_ = o
	svc.mu.Lock()
	svc.info.State = StateApplying
	svc.mu.Unlock()
	if err := svc.ApplyAndRestart(context.Background()); !errors.Is(err, ErrBusy) {
		t.Fatalf("applying 必须不可重入, got %v", err)
	}
}

func TestSkipAndClearSkippedTransitions(t *testing.T) {
	var saved Settings
	svc, _ := newSvc(t, "v0.6.0", func(o *Options) {
		o.Save = func(s Settings) error { saved = s; return nil }
	})
	svc.mu.Lock()
	svc.info.State = StateAvailable
	svc.info.Latest = "v0.7.0"
	svc.mu.Unlock()
	if err := svc.SkipVersion("v0.7.0"); err != nil {
		t.Fatal(err)
	}
	if saved.Skipped != "0.7.0" {
		t.Fatalf("跳过版本必须存 Base 形式（与 Check 的 Base(tag)==Base(Skipped) 对齐）: %+v", saved)
	}
	if got := svc.Info().State; got != StateSkipped {
		t.Fatalf("跳过后的状态应为 skipped, got %s", got)
	}
	if err := svc.ClearSkipped(); err != nil {
		t.Fatal(err)
	}
	if saved.Skipped != "" {
		t.Fatalf("取消跳过未清字段: %+v", saved)
	}
}

// doerFunc 让测试用函数替代 http.Client。
type doerFunc func(*http.Request) (*http.Response, error)

func (f doerFunc) Do(r *http.Request) (*http.Response, error) { return f(r) }
```

（测试文件顶部需 `import "net/http"`。）

- [ ] **Step 2: 运行测试确认失败**

```bash
go test ./internal/update/ -run "TestDisabled|TestCheckRejected|TestApplyRequires|TestSkipAndClear" -v
```

Expected: FAIL —— `undefined: New`。

- [ ] **Step 3: 实现（状态机骨架 + 守卫 + 检查/下载/应用；调度与自检在 Task 9）**

```go
package update

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// 状态常量（spec §7.1）。
const (
	StateIdle         = "idle"
	StateChecking     = "checking"
	StateUpToDate     = "up-to-date"
	StateAvailable    = "available"
	StateSkipped      = "skipped"
	StateRateLimited  = "rate-limited"
	StateCheckFailed  = "check-failed"
	StateNoAsset      = "no-asset"
	StateNoChecksum   = "no-checksum"
	StateDownloading  = "downloading"
	StateVerifyFailed = "verify-failed"
	StateIOFailed     = "io-failed"
	StateReady        = "ready"
	StateApplying     = "applying"
	StateDisabled     = "disabled"
	HintManualUpgrade = "manual-upgrade"
)

// ErrBusy 表示当前状态不允许该操作（并发守卫）。
var ErrBusy = errors.New("当前状态不允许该操作")

// Settings 是服务需要的配置子集。
type Settings struct {
	Auto     bool
	Interval time.Duration
	Source   string
	Skipped  string
}

// Options 注入全部外部依赖（测试可零网络、零进程）。
type Options struct {
	Goos, Goarch, Version, ExePath string
	Doer                           Doer
	Launch                         func(goos, path string, args, env []string) error
	Acquire                        func(exeDir string) (func(), error)
	Emit                           func(event string, payload any)
	Log                            func(msg string) // 诊断日志（门卫原因 / X-RateLimit / 自动检查失败）→ 前端日志面板
	Config                         func() Settings
	Save                           func(Settings) error
	Now                            func() time.Time
}

// UpdateInfo 是前后端唯一契约（JSON 字段见 spec §5.2）。
type UpdateInfo struct {
	Seq         int64  `json:"seq"`
	State       string `json:"state"`
	Current     string `json:"current"`
	Latest      string `json:"latest"`
	Notes       string `json:"notes"`
	PublishedAt string `json:"published_at"`
	Source      string `json:"source"`
	Progress    int    `json:"progress"`
	ReadyPath   string `json:"ready_path"`
	Skipped     bool   `json:"skipped"`
	Manual      bool   `json:"manual"`
	Hint        string `json:"hint"`
	Error       string `json:"error"`
	PendingLog  string `json:"pending_log"`
}

// Service 是更新状态机。
type Service struct {
	opts Options

	mu        sync.Mutex
	info      UpdateInfo
	rel       Release
	checksums map[string]string
	cancelDL  context.CancelFunc
	pending   Plan
	stopCh    chan struct{}
	stopped   bool
}

// New 构造服务；零值 App 场景下也不会 panic（Info 返回 disabled 快照）。
func New(o Options) *Service {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Acquire == nil {
		o.Acquire = Acquire
	}
	if o.Config == nil {
		o.Config = func() Settings { return Settings{} }
	}
	s := &Service{opts: o, stopCh: make(chan struct{})}
	s.info = UpdateInfo{State: StateIdle, Current: o.Version, Source: DefaultSource}
	return s
}

// Info 返回快照（含 seq，前端据此丢弃迟到载荷）。
func (s *Service) Info() UpdateInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.info
}

// emitLocked 在持锁状态更新快照并发事件；调用方保证 s.mu 已加锁。
func (s *Service) setLocked(state, errMsg string) {
	s.info.Seq++
	s.info.State = state
	s.info.Error = errMsg
	payload := s.info
	if s.opts.Emit != nil {
		go s.opts.Emit("update:state", payload)
	}
}

// resolveSource 按配置与合法性挑源（非法/非 loopback 的 http 源回退默认并记一行日志）。
func (s *Service) resolveSource() (string, string) {
	src := strings.TrimSpace(s.opts.Config().Source)
	if src == "" {
		return DefaultSource, ""
	}
	if !strings.HasPrefix(src, "https://") && !strings.HasPrefix(src, "http://") {
		return DefaultSource, "更新源非法，已回退默认源"
	}
	if strings.HasPrefix(src, "http://") && !strings.Contains(src, "127.0.0.1") && !strings.Contains(src, "localhost") {
		return DefaultSource, "非 loopback 的更新源必须使用 https，已回退默认源"
	}
	return src, ""
}

// Check 执行一次检查；manual=false 时受门卫与轮询约束。
func (s *Service) Check(ctx context.Context, manual bool) (UpdateInfo, error) {
	s.mu.Lock()
	switch s.info.State {
	case StateDownloading, StateReady, StateApplying:
		s.mu.Unlock()
		return s.Info(), fmt.Errorf("%w: 当前状态 %s", ErrBusy, s.Info().State)
	case StateChecking:
		s.mu.Unlock()
		return s.Info(), nil
	}
	s.mu.Unlock()

	cfg := s.opts.Config()
	if !manual && (!cfg.Auto || !IsRelease(s.opts.Version)) {
		if s.opts.Log != nil {
			s.opts.Log("未自动检查更新（非 release 构建或已关闭自动检查）")
		}
		s.mu.Lock()
		s.info.State = StateDisabled
		s.info.Manual = false
		s.setLocked(StateDisabled, "")
		s.mu.Unlock()
		return s.Info(), nil
	}
	src, warn := s.resolveSource()

	s.mu.Lock()
	s.info.Manual = manual
	s.info.Source = src
	if warn != "" {
		s.info.Error = warn
	}
	s.setLocked(StateChecking, warn)
	s.mu.Unlock()

	// 检查请求总超时 10s（spec §7.2.2）
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	client := &Client{HTTP: s.opts.Doer, UserAgent: "sshore/" + s.opts.Version}
	rel, err := client.Latest(ctx, src)
	if err != nil {
		state, msg := StateCheckFailed, err.Error()
		if errors.Is(err, ErrRateLimited) {
			state = StateRateLimited
		}
		// 自动检查失败/限流都只在日志面板留一行（spec §7.2.3、§9）
		if s.opts.Log != nil {
			s.opts.Log("检查更新失败：" + msg)
		}
		s.mu.Lock()
		s.setLocked(state, msg)
		s.mu.Unlock()
		return s.Info(), nil
	}

	if IsRelease(s.opts.Version) && Compare(rel.Tag, s.opts.Version) <= 0 {
		s.mu.Lock()
		s.info.Latest = rel.Tag
		s.setLocked(StateUpToDate, "")
		s.mu.Unlock()
		return s.Info(), nil
	}

	if cfg.Skipped != "" && Base(rel.Tag) == Base(cfg.Skipped) {
		s.mu.Lock()
		s.info.Latest = rel.Tag
		s.info.Skipped = true
		s.info.Notes = rel.Notes
		s.setLocked(StateSkipped, "")
		s.mu.Unlock()
		return s.Info(), nil
	}

	archive, err := PickArchive(rel, s.opts.Goos, s.opts.Goarch)
	if err != nil {
		s.mu.Lock()
		s.info.Latest = rel.Tag
		s.setLocked(StateNoAsset, err.Error())
		s.mu.Unlock()
		return s.Info(), nil
	}
	csAsset, err := PickChecksums(rel)
	if err != nil {
		s.mu.Lock()
		s.info.Latest = rel.Tag
		s.setLocked(StateNoChecksum, err.Error())
		s.mu.Unlock()
		return s.Info(), nil
	}
	if !SameOrigin(src, archive.URL) || !SameOrigin(src, csAsset.URL) {
		s.mu.Lock()
		s.info.Latest = rel.Tag
		s.setLocked(StateCheckFailed, "下载地址不在受信主机集合内")
		s.mu.Unlock()
		return s.Info(), nil
	}

	s.mu.Lock()
	s.rel = rel
	s.info.Latest = rel.Tag
	s.info.Notes = rel.Notes
	s.info.PublishedAt = rel.PublishedAt
	s.info.Skipped = false
	s.setLocked(StateAvailable, "")
	s.mu.Unlock()
	return s.Info(), nil
}

// StartDownload 在 available / verify-failed / io-failed 上启动下载（重试路径）。
func (s *Service) StartDownload(ctx context.Context) error {
	s.mu.Lock()
	switch s.info.State {
	case StateAvailable, StateVerifyFailed, StateIOFailed:
	case StateDownloading:
		s.mu.Unlock()
		return fmt.Errorf("%w: 已在下载", ErrBusy)
	default:
		state := s.info.State
		s.mu.Unlock()
		return fmt.Errorf("%w: 当前状态 %s", ErrBusy, state)
	}
	s.mu.Unlock()
	go s.download(ctx)
	return nil
}

// CancelDownload 取消在飞下载（幂等）。
func (s *Service) CancelDownload() bool {
	s.mu.Lock()
	cancel := s.cancelDL
	s.mu.Unlock()
	if cancel == nil {
		return false
	}
	cancel()
	return true
}

// SkipVersion 记录「跳过此版本」；只经 Save 写配置（避免前端全量回写把它清掉）。
func (s *Service) SkipVersion(v string) error {
	s.mu.Lock()
	if s.info.State == StateApplying {
		s.mu.Unlock()
		return ErrBusy
	}
	s.mu.Unlock()
	cfg := s.opts.Config()
	cfg.Skipped = Base(v)
	if s.opts.Save != nil {
		if err := s.opts.Save(cfg); err != nil {
			return err
		}
	}
	s.mu.Lock()
	s.info.Skipped = true
	s.setLocked(StateSkipped, "")
	s.mu.Unlock()
	return nil
}

// ClearSkipped 清掉跳过并立刻重查（状态落 checking，保持与 UI 文案一致）。
func (s *Service) ClearSkipped() error {
	cfg := s.opts.Config()
	cfg.Skipped = ""
	if s.opts.Save != nil {
		if err := s.opts.Save(cfg); err != nil {
			return err
		}
	}
	s.mu.Lock()
	s.info.Skipped = false
	s.setLocked(StateChecking, "")
	s.mu.Unlock()
	_, err := s.Check(context.Background(), true)
	return err
}

// DiscardPending 删除待安装文件与 sidecar（applying 时拒绝）。
func (s *Service) DiscardPending() error {
	s.mu.Lock()
	if s.info.State == StateApplying {
		s.mu.Unlock()
		return ErrBusy
	}
	plan := s.pending
	s.mu.Unlock()
	if plan.Pending != "" {
		_ = os.Remove(plan.Pending)
		_ = os.Remove(plan.Sidecar)
	}
	s.mu.Lock()
	s.pending = Plan{}
	s.info.ReadyPath = ""
	s.info.Progress = 0
	if s.info.Latest != "" {
		s.setLocked(StateAvailable, "")
	} else {
		s.setLocked(StateIdle, "")
	}
	s.mu.Unlock()
	return nil
}

// ApplyAndRestart 是唯一允许替换二进制的入口（必须显式调用）。
func (s *Service) ApplyAndRestart(ctx context.Context) error {
	s.mu.Lock()
	if s.info.State == StateApplying {
		s.mu.Unlock()
		return ErrBusy
	}
	if s.info.State != StateReady {
		state := s.info.State
		s.mu.Unlock()
		return fmt.Errorf("%w: 需 ready，当前 %s", ErrBusy, state)
	}
	plan := s.pending
	s.mu.Unlock()

	// ① size 守卫：Size>0 时校验，Size==0（重启后由自检进入 ready）以磁盘为准
	st, err := os.Stat(plan.Pending)
	if err != nil {
		return s.failIO(fmt.Errorf("待安装文件不存在：%w", err), "")
	}
	if plan.Size > 0 && st.Size() != plan.Size {
		return s.failVerify(fmt.Errorf("待安装文件大小已变化：%d != %d", st.Size(), plan.Size))
	}
	plan.Size = st.Size()

	// ② sidecar 重算（补上「落盘后被替换」的窗口，spec §10.1）
	want, err := readSidecar(plan.Sidecar)
	if err != nil {
		return s.failVerify(fmt.Errorf("缺少或无法读取哈希 sidecar：%w", err))
	}
	if err := VerifyFile(plan.Pending, want); err != nil {
		return s.failVerify(err)
	}

	// ③ 与当前版本比较：绝不允许降级安装
	toVer, ok := ParsePendingName(filepath.Base(plan.Pending))
	if !ok || Compare(toVer, s.opts.Version) <= 0 {
		return s.failVerify(fmt.Errorf("待安装版本 %q 不高于当前版本 %q，拒绝安装", toVer, s.opts.Version))
	}

	// ④ 排他锁
	release, err := s.opts.Acquire(plan.ExeDir)
	if err != nil {
		return s.failIO(err, "")
	}
	defer release()

	// ⑤ 写脚本 + 分离启动
	body, err := ScriptBytes(s.opts.Goos)
	if err != nil {
		return s.failIO(err, "")
	}
	scriptPath := filepath.Join(plan.ExeDir, ScriptName(s.opts.Goos))
	mode := os.FileMode(0o755)
	if s.opts.Goos == "windows" {
		mode = 0o644
	}
	if err := os.WriteFile(scriptPath, body, mode); err != nil {
		return s.failIO(err, "")
	}
	launch := s.opts.Launch
	if launch == nil {
		launch = StartDetached
	}
	pid := os.Getpid()
	var args, env []string
	if s.opts.Goos == "windows" {
		env = ScriptEnv(plan, pid)
	} else {
		args = ScriptArgs(plan, pid)
	}
	if err := launch(s.opts.Goos, scriptPath, args, env); err != nil {
		return s.failIO(err, "")
	}

	s.mu.Lock()
	s.setLocked(StateApplying, "")
	s.mu.Unlock()
	return nil
}

// failIO / failVerify 收敛错误状态（IO 与校验失败要区分，前者可重试）。
func (s *Service) failIO(err error, hint string) error {
	s.mu.Lock()
	s.info.Hint = hint
	s.setLocked(StateIOFailed, err.Error())
	s.mu.Unlock()
	return err
}

func (s *Service) failVerify(err error) error {
	s.mu.Lock()
	s.setLocked(StateVerifyFailed, err.Error())
	s.mu.Unlock()
	return err
}

// readSidecar 读单行 sidecar（<hash> 两个空格 <文件名>）。
func readSidecar(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	m, err := ParseChecksums(strings.NewReader(string(raw)))
	if err != nil {
		return "", err
	}
	for _, h := range m {
		return h, nil
	}
	return "", fmt.Errorf("sidecar 为空")
}
```

（`download`/`Init`/`Reconfigure`/`Shutdown` 在 Task 9 实现；本任务先让上述方法编译通过，`download` 可以先是一个最小骨架，但**必须**在 Task 9 的测试里变成完整实现。）

- [ ] **Step 4: 运行测试确认通过**

```bash
go test ./internal/update/ -run "TestDisabled|TestCheckRejected|TestApplyRequires|TestSkipAndClear" -v
GOOS=windows go vet ./internal/update/
```

Expected: PASS；vet 无输出。

- [ ] **Step 5: 提交**

```bash
git add internal/update/service.go internal/update/service_test.go
git commit -m "feat(update): 状态机、并发守卫与显式触发的应用入口"
```

---

---

### Task 9: service.go（二）—— 下载实现、残留自检、轮询调度

**Files:**
- Modify: `internal/update/service.go`
- Test: `internal/update/service_selfcheck_test.go`

**Interfaces:**
- Consumes: Task 8 的 `Service`
- Produces: `func (s *Service) Init(ctx context.Context)`；`func (s *Service) Shutdown()`；`func (s *Service) Reconfigure()`；内部 `func (s *Service) download(ctx context.Context)`；`func lastResult(logPath string) string`（返回 "ok" / "fail:<step>" / ""）

- [ ] **Step 1: 写失败测试（自检判别 + 日志末行协议）**

```go
package update

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestInitSelfCheckClassifiesPendingVersusBackup(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"sshore", "sshore.v0.6.0", "sshore.v0.6.0-80-gc2d2a36", "sshore.v0.7.0"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	svc := New(Options{
		Goos: "linux", Goarch: "amd64", Version: "v0.6.0",
		ExePath: filepath.Join(dir, "sshore"),
		Config:  func() Settings { return Settings{} },
		Emit:    func(string, any) {},
	})
	svc.Init(context.Background())
	defer svc.Shutdown()
	info := svc.Info()
	if info.State != StateReady {
		t.Fatalf("state = %s, want ready", info.State)
	}
	if filepath.Base(info.ReadyPath) != "sshore.v0.7.0" {
		t.Fatalf("ready_path = %s", info.ReadyPath)
	}
}

func TestInitSelfCheckTreatsStaleOKLogAsStale(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "sshore"), []byte("x"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "sshore-update.log"), []byte("RESULT=ok\n"), 0o644)
	svc := New(Options{
		Goos: "linux", Goarch: "amd64", Version: "v0.7.0",
		ExePath: filepath.Join(dir, "sshore"),
		Config:  func() Settings { return Settings{} },
		Emit:    func(string, any) {},
	})
	svc.Init(context.Background())
	defer svc.Shutdown()
	if _, err := os.Stat(filepath.Join(dir, "sshore-update.log")); !os.IsNotExist(err) {
		t.Fatal("RESULT=ok 的陈旧日志必须被清理")
	}
	if got := svc.Info().PendingLog; got != "" {
		t.Fatalf("陈旧日志不应触发「上次升级未完成」: %s", got)
	}
}

func TestInitSelfCheckSurfacesFailedLog(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "sshore"), []byte("x"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "sshore-update.log"), []byte("STEP=8 ERR=x\nRESULT=fail:launch\n"), 0o644)
	svc := New(Options{
		Goos: "linux", Goarch: "amd64", Version: "v0.7.0",
		ExePath: filepath.Join(dir, "sshore"),
		Config:  func() Settings { return Settings{} },
		Emit:    func(string, any) {},
	})
	svc.Init(context.Background())
	defer svc.Shutdown()
	if svc.Info().PendingLog == "" {
		t.Fatal("失败日志必须暴露给界面（pending_log）")
	}
}

func TestReconfigureRebuildsTickerWithNewInterval(t *testing.T) {
	svc := New(Options{
		Goos: "linux", Goarch: "amd64", Version: "v0.6.0",
		ExePath: filepath.Join(t.TempDir(), "sshore"),
		Config:  func() Settings { return Settings{Auto: true, Interval: time.Hour} },
		Emit:    func(string, any) {},
	})
	svc.Init(context.Background())
	svc.Reconfigure()  // 只断言不 panic；调度本身由人工/端到端覆盖
	svc.Shutdown()
}

func TestApplyRejectsTamperedPendingSidecar(t *testing.T) {
	// spec §10.1 的第二道校验，也是 DoD#10 变异 ③ 的对应测试：
	// 只要把 ApplyAndRestart 里的 VerifyFile 去掉，本测试就必须失败。
	dir := t.TempDir()
	exe := filepath.Join(dir, "sshore")
	if err := os.WriteFile(exe, []byte("OLD\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	plan := PlanFor("linux", exe, "v0.6.0", "v0.7.0", 0, DefaultWait)
	if err := os.WriteFile(plan.Pending, []byte("NEW\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(plan.Pending)
	plan.Size = st.Size()
	launched := false
	svc := New(Options{
		Goos: "linux", Goarch: "amd64", Version: "v0.6.0", ExePath: exe,
		Acquire: func(string) (func(), error) { return func() {}, nil },
		Launch:  func(string, string, []string, []string) error { launched = true; return nil },
		Config:  func() Settings { return Settings{} },
		Emit:    func(string, any) {},
	})
	svc.mu.Lock()
	svc.info.State = StateReady
	svc.info.ReadyPath = plan.Pending
	svc.pending = plan
	svc.mu.Unlock()

	if err := svc.ApplyAndRestart(context.Background()); err == nil {
		t.Fatal("sidecar 缺失必须拒绝 apply")
	}
	if launched {
		t.Fatal("被拒时不得启动脚本")
	}
	sidecar := strings.Repeat("0", 64) + "  " + filepath.Base(plan.Pending) + "\n"
	if err := os.WriteFile(plan.Sidecar, []byte(sidecar), 0o600); err != nil {
		t.Fatal(err)
	}
	svc.mu.Lock()
	svc.info.State = StateReady
	svc.mu.Unlock()
	if err := svc.ApplyAndRestart(context.Background()); err == nil {
		t.Fatal("哈希不符必须拒绝 apply")
	}
	if got := svc.Info().State; got != StateVerifyFailed {
		t.Fatalf("state = %s, want verify-failed", got)
	}
	if launched {
		t.Fatal("校验失败时不得启动脚本")
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

```bash
go test ./internal/update/ -run "TestInitSelfCheck|TestReconfigure" -v
```

Expected: FAIL —— `svc.Init undefined`。

- [ ] **Step 3: 实现（追加到 service.go：自检只用版本序判别 + 日志末行协议）**

```go
// lastResult 读日志末行：返回 "ok"、"fail:<step>" 或 ""（无日志/空文件）。
func lastResult(logPath string) string {
	raw, err := os.ReadFile(logPath)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) == 0 {
		return ""
	}
	last := strings.TrimSpace(lines[len(lines)-1])
	if strings.HasPrefix(last, "RESULT=") {
		return strings.TrimPrefix(last, "RESULT=")
	}
	return ""
}

// Init 做残留自检并按配置调度（ctx 来自 startup）。
func (s *Service) Init(ctx context.Context) {
	dir := filepath.Dir(s.opts.ExePath)
	if plan, ok := ResumePending(dir, s.opts.Version); ok {
		ver, _ := ParsePendingName(filepath.Base(plan.Pending))
		s.mu.Lock()
		s.pending = plan
		s.info.ReadyPath = plan.Pending
		s.info.Latest = ver
		s.setLocked(StateReady, "")
		s.mu.Unlock()
	}
	logPath := filepath.Join(dir, "sshore-update.log")
	switch res := lastResult(logPath); {
	case res == "ok":
		_ = os.Remove(logPath)
	case strings.HasPrefix(res, "fail:"):
		s.mu.Lock()
		s.info.PendingLog = logPath
		s.info.Error = "上次升级未完成（" + res + "）"
		s.mu.Unlock()
	}
	go s.loop(ctx)
}

// loop 负责首次延迟 5s 检查与后续轮询。
// 注意：stopCh 会被 Reconfigure 替换，循环里必须每次持锁读一次本地副本，避免与替换竞争。
func (s *Service) loop(ctx context.Context) {
	cfg := s.opts.Config()
	if !cfg.Auto || !IsRelease(s.opts.Version) {
		return
	}
	stopCh := s.currentStop() // 持锁读：Reconfigure 会替换该字段，直接读会与写竞争（-race 会报）
	select {
	case <-time.After(5 * time.Second):
	case <-stopCh:
		return
	}
	_, _ = s.Check(ctx, false)
	if cfg.Interval <= 0 {
		return
	}
	ticker := time.NewTicker(cfg.Interval)
	defer ticker.Stop()
	for {
		stopCh := s.currentStop()
		select {
		case <-ticker.C:
			_, _ = s.Check(ctx, false)
		case <-stopCh:
			return
		case <-ctx.Done():
			return
		}
	}
}

// Reconfigure 在设置保存后重建调度。
func (s *Service) Reconfigure() {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	close(s.stopCh)
	s.stopCh = make(chan struct{})
	s.mu.Unlock()
	go s.loop(context.Background())
}

// Shutdown 停掉调度与在飞下载。
func (s *Service) Shutdown() {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	s.stopped = true
	if s.cancelDL != nil {
		s.cancelDL()
	}
	close(s.stopCh)
	s.mu.Unlock()
}

// currentStop 在锁内读 stopCh：Reconfigure 会替换该字段，直接读会与写竞争（-race 会报）。
func (s *Service) currentStop() chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stopCh
}

// download 执行下载 → 校验 → 解包 → sidecar → ready。
func (s *Service) download(ctx context.Context) {
	s.mu.Lock()
	rel := s.rel
	plan := PlanFor(s.opts.Goos, s.opts.ExePath, s.opts.Version, rel.Tag, 0, DefaultWait)
	s.info.Progress = 0
	s.setLocked(StateDownloading, "")
	ctx, cancel := context.WithCancel(ctx)
	s.cancelDL = cancel
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.cancelDL = nil
		s.mu.Unlock()
	}()

	// 错误收敛必须在释放 s.mu 之后：failIO/failVerify 内部会再次加锁，持锁调用会自死锁。
	archiveAsset, err := PickArchive(rel, s.opts.Goos, s.opts.Goarch)
	if err != nil {
		_ = s.failIO(err, "")
		return
	}
	csAsset, err := PickChecksums(rel)
	if err != nil {
		_ = s.failVerify(err)
		return
	}

	probe := filepath.Join(plan.ExeDir, ".sshore-write-test")
	if err := os.WriteFile(probe, []byte("x"), 0o600); err != nil {
		_ = s.failIO(fmt.Errorf("安装目录不可写：%w", err), HintManualUpgrade)
		return
	}
	_ = os.Remove(probe)
	free, err := FreeSpace(plan.ExeDir)
	if err != nil {
		_ = s.failIO(err, "")
		return
	}
	if free < 64<<20 {
		_ = s.failIO(fmt.Errorf("磁盘可用空间不足：%d 字节", free), "")
		return
	}

	client := &Client{HTTP: s.opts.Doer, UserAgent: "sshore/" + s.opts.Version}
	part := filepath.Join(os.TempDir(), fmt.Sprintf("sshore-update-%d.part", os.Getpid()))
	err = client.Download(ctx, archiveAsset.URL, part, DownloadOpt{
		IdleTimeout: 30 * time.Second,
		Throttle:    200 * time.Millisecond,
		Progress: func(done, total int64) {
			percent := -1
			if total > 0 {
				percent = int(done * 100 / total)
			}
			s.mu.Lock()
			s.info.Progress = percent
			s.mu.Unlock()
			if s.opts.Emit != nil {
				s.opts.Emit("update:progress", map[string]any{"done": done, "total": total, "percent": percent})
			}
		},
	})
	if err != nil {
		if errors.Is(err, context.Canceled) {
			// 用户取消：删半截文件、回 available（spec §7.3.6），不算失败
			s.mu.Lock()
			s.info.Progress = 0
			s.setLocked(StateAvailable, "")
			s.mu.Unlock()
			return
		}
		_ = s.failIO(err, "")
		return
	}
	defer os.Remove(part)

	csBytes, err := fetchBytes(ctx, s.opts.Doer, csAsset.URL, 1<<20)
	if err != nil {
		_ = s.failIO(err, "")
		return
	}
	all, err := ParseChecksums(strings.NewReader(string(csBytes)))
	if err != nil {
		_ = s.failVerify(err)
		return
	}
	hash, ok := all[archiveAsset.Name]
	if !ok {
		_ = s.failVerify(fmt.Errorf("校验文件里没有 %s", archiveAsset.Name))
		return
	}
	if err := VerifyFile(part, hash); err != nil {
		_ = s.failVerify(err)
		return
	}
	if err := ExtractBinary(part, s.opts.Goos, plan.Pending); err != nil {
		_ = s.failIO(err, "")
		return
	}
	st, err := os.Stat(plan.Pending)
	if err != nil {
		_ = s.failIO(err, "")
		return
	}
	plan.Size = st.Size()
	fileHash, err := FileSHA256(plan.Pending)
	if err != nil {
		_ = s.failIO(err, "")
		return
	}
	line := fileHash + "  " + filepath.Base(plan.Pending) + "\n"
	if err := os.WriteFile(plan.Sidecar, []byte(line), 0o600); err != nil {
		_ = s.failIO(err, "")
		return
	}
	s.mu.Lock()
	s.pending = plan
	s.info.ReadyPath = plan.Pending
	s.info.Progress = 100
	s.setLocked(StateReady, "")
	s.mu.Unlock()
}

// fetchBytes 取小文件（校验文件），带大小上限。
func fetchBytes(ctx context.Context, d Doer, rawURL string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := d.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("拉取 %s 失败：HTTP %d", rawURL, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}
```

- [ ] **Step 4: 运行测试确认通过 + 全包测试**

```bash
go test ./internal/update/ -v
GOOS=windows go vet ./internal/update/
```

Expected: PASS（Task 1–9 全部测试）。

- [ ] **Step 5: 提交**

```bash
git add internal/update/service.go internal/update/service_selfcheck_test.go
git commit -m "feat(update): 下载全流程、残留自检（版本序 + 日志末行）与轮询调度"
```

---

### Task 10: 配置字段（含「跳过版本」双写修复）

**Files:**
- Modify: `internal/config/store.go`（字段 + `Normalize` + `DefaultAppConfig`）
- Test: `internal/config/update_settings_test.go`（新建）

**Interfaces:**
- Consumes: 无
- Produces: `AppSettings.UpdateCheckAuto`、`UpdateCheckIntervalHours`、`UpdateSkippedVersion`、`UpdateSource`（TOML/JSON 键：`update_check_auto`、`update_check_interval_hours`、`update_skipped_version`、`update_source`）

- [ ] **Step 1: 写失败测试**

```go
package config

import "testing"

// 只比较本任务新增的四个字段：既有 Normalize 还会把 Theme 置 system、FontScale 置 1，
// 对完整结构体做 == 会让这些用例全部失败（两阶段评审实测）。
// 区间语义：Normalize 只把 <0 归一为 12；「缺键」的默认值由 DefaultAppConfig/LoadConfig 给，
// 因此 0 表示用户显式关闭轮询（配置文件写了 update_check_interval_hours = 0）。
func TestUpdateSettingsNormalize(t *testing.T) {
	cases := []struct {
		name                       string
		in                         AppSettings
		wantInterval               int
		wantSource, wantSkipVer    string
	}{
		{"负间隔回 12", AppSettings{UpdateCheckIntervalHours: -3}, 12, "", ""},
		{"上限 168", AppSettings{UpdateCheckIntervalHours: 999}, 168, "", ""},
		{"0 表显式关闭轮询", AppSettings{UpdateCheckIntervalHours: 0}, 0, "", ""},
		{"非法源清空", AppSettings{UpdateSource: " ftp://x ", UpdateCheckIntervalHours: 12}, 12, "", ""},
		{"合法源去空白保留", AppSettings{UpdateSource: " https://mirror.corp/api ", UpdateCheckIntervalHours: 12}, 12, "https://mirror.corp/api", ""},
		{"跳过版本去 v 与空白", AppSettings{UpdateSkippedVersion: " v0.7.0 ", UpdateCheckIntervalHours: 12}, 12, "", "0.7.0"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := c.in
			s.Normalize()
			if s.UpdateCheckIntervalHours != c.wantInterval || s.UpdateSource != c.wantSource || s.UpdateSkippedVersion != c.wantSkipVer {
				t.Fatalf("新字段归一错误: interval=%d source=%q skipped=%q", s.UpdateCheckIntervalHours, s.UpdateSource, s.UpdateSkippedVersion)
			}
			if s.Theme != "system" || s.FontScale != 1 {
				t.Fatalf("既有字段归一被破坏: theme=%q fontScale=%v", s.Theme, s.FontScale)
			}
		})
	}
}

func TestDefaultConfigEnablesUpdateCheck(t *testing.T) {
	if !DefaultAppConfig().App.UpdateCheckAuto {
		t.Fatal("默认必须开启自动检查（否则老配置升级后静默不再检查）")
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

```bash
go test ./internal/config/ -run "TestUpdateSettings|TestDefaultConfigEnables" -v
```

Expected: FAIL —— 字段不存在（编译错误）。

- [ ] **Step 3: 实现（Normalize 保持纯函数、**不写日志**；非法源只清空，回退与告警在 Service 侧）**

```go
// ① 追加到 AppSettings 结构体：
	UpdateCheckAuto          bool   `toml:"update_check_auto" json:"update_check_auto"`
	UpdateCheckIntervalHours int    `toml:"update_check_interval_hours" json:"update_check_interval_hours"`
	UpdateSkippedVersion     string `toml:"update_skipped_version" json:"update_skipped_version"`
	UpdateSource             string `toml:"update_source" json:"update_source"`

// ② 追加到 (*AppSettings).Normalize()：
	if s.UpdateCheckIntervalHours < 0 {
		s.UpdateCheckIntervalHours = 12
	}
	if s.UpdateCheckIntervalHours > 168 {
		s.UpdateCheckIntervalHours = 168
	}
	s.UpdateSource = strings.TrimSpace(s.UpdateSource)
	if s.UpdateSource != "" && !strings.HasPrefix(s.UpdateSource, "http://") && !strings.HasPrefix(s.UpdateSource, "https://") {
		s.UpdateSource = ""
	}
	s.UpdateSkippedVersion = strings.TrimPrefix(strings.TrimSpace(s.UpdateSkippedVersion), "v")

// ③ 追加到 DefaultAppConfig().App：
			UpdateCheckAuto:          true,
			UpdateCheckIntervalHours: 12,
```

- [ ] **Step 4: 后端把新字段接到 Service（保证「跳过版本」只由后端读写）**

`Service` 的 `Config`/`Save` 必须**直接读写 `a.cfg.App`、不走前端**（前端 `save()` 是挂载时的字段快照，用它回写会把「跳过版本」清掉）。
具体实现是 `app.go` 的两个方法 `a.updateSettings` / `a.saveUpdateSettings`，代码在 **Task 11 Step 3**（那里同时是绑定的接线处）。
本任务只负责配置字段与 `Normalize`。

- [ ] **Step 5: 运行测试确认通过**

```bash
go test ./internal/config/ -v && go test ./... -count=1
```

Expected: PASS，现有 config 测试不回归。

- [ ] **Step 6: 提交**

```bash
git add internal/config/store.go internal/config/update_settings_test.go
git commit -m "feat(config): 更新检查开关/间隔/跳过版本/更新源字段，并让跳过版本只由后端读写"
```

---

### Task 11: app.go 接线 + 绑定 + 重新生成 wailsjs

**Files:**
- Modify: `app.go`（字段、`startup`、`OnShutdown`、`SetSettings`、9 个绑定）
- Modify: `frontend/wailsjs/go/main/App.js`、`frontend/wailsjs/go/main/App.d.ts`、`frontend/wailsjs/go/models.ts`（**用 wails 重新生成，不要手改**；`models.ts` 在 `go/` 下，不在 `go/main/` 下 —— 两阶段评审核对过生成物布局）
- Test: `app_update_test.go`（新建）

**Interfaces:**
- Consumes: `update.Service`（Task 8/9）、4 个配置字段（Task 10）
- Produces: `func (a *App) GetUpdateInfo() update.UpdateInfo`、`CheckUpdate(manual bool) (update.UpdateInfo, error)`、`StartUpdateDownload() error`、`CancelUpdateDownload() bool`、`ApplyUpdateAndRestart() error`、`DiscardUpdateDownload() error`、`SkipUpdateVersion(version string) error`、`ClearSkippedUpdate() error`、`OpenReleasePage() error`

- [ ] **Step 1: 写失败测试（零值 App 不得 panic；门卫与快照语义）**

```go
package main

import "testing"

func TestUpdateBindingsAreSafeWithoutStartup(t *testing.T) {
	// 测试里直接构造的 App 没走 startup，updater 为 nil；绑定必须返回 disabled 快照而不是 panic。
	a := &App{}
	info := a.GetUpdateInfo()
	if info.State != "disabled" {
		t.Fatalf("state = %s, want disabled", info.State)
	}
	if _, err := a.CheckUpdate(true); err == nil {
		t.Fatal("未初始化时必须返回错误而不是 panic")
	}
	if err := a.StartUpdateDownload(); err == nil {
		t.Fatal("未初始化时必须返回错误")
	}
	if err := a.ApplyUpdateAndRestart(); err == nil {
		t.Fatal("未初始化时必须返回错误")
	}
	if a.CancelUpdateDownload() {
		t.Fatal("未初始化时取消应返回 false")
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

```bash
go test . -run TestUpdateBindingsAreSafeWithoutStartup -v
```

Expected: FAIL —— `a.GetUpdateInfo undefined`。

- [ ] **Step 3: 实现接线（**关键：构造与调度放 startup(ctx)，不放 App.Init**）**

```go
// ① App 结构体新增字段
	updater *update.Service

// ② startup(ctx) 末尾（app.go:90，a.ctx 赋值之后）
	a.updater = update.New(update.Options{
		Goos: gosruntime.GOOS, Goarch: gosruntime.GOARCH,
		Version: Version, ExePath: exePath(), // exePath() = os.Executable() 包一层，失败返回 ""
		Doer: &http.Client{Transport: &http.Transport{
			ResponseHeaderTimeout: 15 * time.Second,
			Proxy:                 http.ProxyFromEnvironment,
		}},
		Launch:  update.StartDetached,
		Acquire: update.Acquire,
		Emit:    func(event string, payload any) { runtime.EventsEmit(a.ctx, event, payload) },
		Log:     a.logUpdate,
		Config:  a.updateSettings,
		Save:    a.saveUpdateSettings,
	})
	a.updater.Init(ctx)

// ③ App.OnShutdown()（app.go:1044）里，与其他控制器一起停
	if a.updater != nil {
		a.updater.Shutdown()
	}

// ④ SetSettings（app.go:260）保存成功后
	if a.updater != nil {
		a.updater.Reconfigure()
	}

// ⑤ 配置读写、诊断日志与可执行文件路径（必须直读 a.cfg，避免前端快照回写）

// logUpdate 把更新服务的诊断信息转发到前端日志面板（复用既有 log 事件）。
func (a *App) logUpdate(msg string) {
	if a.emit == nil {
		return
	}
	a.emit(forward.Event{
		SourceType: "system",
		SourceID:   "update",
		TS:         time.Now().Format(time.RFC3339),
		Level:      "info",
		Message:    msg,
	})
}
func (a *App) updateSettings() update.Settings {
	if a.cfg == nil {
		return update.Settings{Auto: true, Interval: 12 * time.Hour}
	}
	return update.Settings{
		Auto:     a.cfg.App.UpdateCheckAuto,
		Interval: time.Duration(a.cfg.App.UpdateCheckIntervalHours) * time.Hour,
		Source:   a.cfg.App.UpdateSource,
		Skipped:  a.cfg.App.UpdateSkippedVersion,
	}
}

func (a *App) saveUpdateSettings(s update.Settings) error {
	if a.cfg == nil {
		a.cfg = config.DefaultAppConfig()
	}
	a.cfg.App.UpdateSkippedVersion = s.Skipped
	return a.saveConfig()
}

// exePath 返回当前可执行文件绝对路径；失败返回空串（调用方按 nil 路径处理）。
func exePath() string {
	p, err := os.Executable()
	if err != nil {
		return ""
	}
	return p
}

// ⑥ 9 个绑定（nil 安全 + 中文注释）
func (a *App) GetUpdateInfo() update.UpdateInfo {
	if a.updater == nil {
		return update.UpdateInfo{State: update.StateDisabled, Current: Version, Source: update.DefaultSource}
	}
	return a.updater.Info()
}

func (a *App) CheckUpdate(manual bool) (update.UpdateInfo, error) {
	if a.updater == nil {
		return a.GetUpdateInfo(), errors.New("更新服务未初始化")
	}
	return a.updater.Check(a.ctx, manual)
}

func (a *App) StartUpdateDownload() error {
	if a.updater == nil {
		return errors.New("更新服务未初始化")
	}
	return a.updater.StartDownload(a.ctx)
}

func (a *App) CancelUpdateDownload() bool {
	if a.updater == nil {
		return false
	}
	return a.updater.CancelDownload()
}

func (a *App) ApplyUpdateAndRestart() error {
	if a.updater == nil {
		return errors.New("更新服务未初始化")
	}
	if err := a.updater.ApplyAndRestart(a.ctx); err != nil {
		return err
	}
	runtime.Quit(a.ctx)
	return nil
}

func (a *App) DiscardUpdateDownload() error {
	if a.updater == nil {
		return errors.New("更新服务未初始化")
	}
	return a.updater.DiscardPending()
}

func (a *App) SkipUpdateVersion(version string) error {
	if a.updater == nil {
		return errors.New("更新服务未初始化")
	}
	return a.updater.SkipVersion(version)
}

func (a *App) ClearSkippedUpdate() error {
	if a.updater == nil {
		return errors.New("更新服务未初始化")
	}
	return a.updater.ClearSkipped()
}

func (a *App) OpenReleasePage() error {
	runtime.BrowserOpenURL(a.ctx, Repo+"/releases")
	return nil
}
```

**导入与守卫（否则编译不过）**：

- 只新增三个导入：`gosruntime "runtime"`（**必须起别名**，因为 app.go 已导入 Wails 的 `runtime`）、`net/http`、`"sshore/internal/update"`。**`os`、`time`、`errors`、`config` 在 app.go 里已经导入**，再加会编译失败（两阶段评审指出）。
- `a.ctx` 在测试里可能为 nil：`Check`/`StartDownload` 内部只用 ctx 做取消，nil 会 panic —— 所有绑定统一用 `if a.updater == nil || a.ctx == nil { return ... }` 双守卫。

- [ ] **Step 4: 重新生成并提交 wailsjs 绑定**

```bash
wails generate module
git status --short frontend/wailsjs
for f in GetUpdateInfo CheckUpdate StartUpdateDownload CancelUpdateDownload ApplyUpdateAndRestart DiscardUpdateDownload SkipUpdateVersion ClearSkippedUpdate OpenReleasePage; do
  grep -q "$f" frontend/wailsjs/go/main/App.d.ts || { echo "绑定缺失: $f"; exit 1; }
done
grep -q "UpdateInfo" frontend/wailsjs/go/models.ts && echo "models.ts 已含 UpdateInfo"
```

Expected: `wails generate module` 成功；上面的循环对 9 个函数逐一校验（缺任一就退出 1），并确认 `frontend/wailsjs/go/models.ts` 里出现 `UpdateInfo`。

- [ ] **Step 5: 运行测试确认通过**

```bash
go test . -run TestUpdateBindingsAreSafeWithoutStartup -v && go vet ./...
cd frontend && npm run build
```

Expected: PASS；`go vet` 无输出；前端构建成功（若绑定没生成，这一步会因 import 缺导出而失败）。

- [ ] **Step 6: 提交**

```bash
git add app.go app_update_test.go frontend/wailsjs
git commit -m "feat(app): 更新检查绑定与 startup/OnShutdown/SetSettings 接线，重新生成 wailsjs"
```

---

## 阶段 B（下载自升级；依赖 Task 0 的 go 结论）

### Task 12: 脚本真跑（Linux）—— 成功/超时/参数非法/回滚四条路径

**Files:**
- Test: `internal/update/script_run_test.go`（`//go:build !windows`）

**Interfaces:**
- Consumes: `ScriptBytes`/`ScriptArgs`（Task 6）、`PlanFor`（Task 5）
- Produces: 对脚本行为的回归保护（spec §13.2 的 1–5 条）

- [ ] **Step 1: 写失败测试（真执行脚本，断言文件系统结果）**

```go
//go:build !windows

package update

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// runScript 在临时目录里跑生成的 update.sh，返回日志内容与错误。
func runScript(t *testing.T, plan Plan, pid int) (string, error) {
	t.Helper()
	body, err := ScriptBytes("linux")
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(plan.ExeDir, "sshore-update.sh")
	if err := os.WriteFile(script, body, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(script, ScriptArgs(plan, pid)...)
	cmd.Dir = plan.ExeDir
	err = cmd.Run()
	log, _ := os.ReadFile(plan.LogPath)
	return string(log), err
}

// newFixture 造出旧二进制 + 待安装文件 + 一份更早的备份。
// fakeBinary 是「可执行且能存活 >3s」的假二进制：脚本第 8 步会做 3 秒存活探测，
// 用不可执行的字面量会让脚本走回滚分支（两阶段评审实测）。它把 PID 写进 fake.pid，
// 由 t.Cleanup 杀掉，避免测试留下孤儿进程。
const fakeBinary = "#!/bin/sh\necho $$ > \"$(dirname \"$0\")/fake.pid\"\nsleep 30\n"

func newFixture(t *testing.T) Plan {
	t.Helper()
	dir := t.TempDir()
	exe := filepath.Join(dir, "sshore")
	if err := os.WriteFile(exe, []byte("OLD\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := PlanFor("linux", exe, "v0.6.0", "v0.7.0", 0, DefaultWait)
	if err := os.WriteFile(p.Pending, []byte(fakeBinary), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sshore.v0.5.0"), []byte("OLDER\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		raw, err := os.ReadFile(filepath.Join(dir, "fake.pid"))
		if err != nil {
			return
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
		if err != nil {
			return
		}
		if proc, err := os.FindProcess(pid); err == nil {
			_ = proc.Kill()
		}
	})
	return p
}

// exitedChild 起一个立刻退出的子进程并 reap，返回其 PID。
func exitedChild(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	return cmd.Process.Pid
}

func TestScriptSuccessPath(t *testing.T) {
	p := newFixture(t)
	st, _ := os.Stat(p.Pending)
	p.Size = st.Size()
	if _, err := runScript(t, p, exitedChild(t)); err != nil {
		t.Fatalf("脚本应成功: %v", err)
	}
	got, readErr := os.ReadFile(p.Target)
	if readErr != nil || !strings.HasPrefix(string(got), "#!/bin/sh") {
		t.Fatalf("正式二进制内容错误: %q %v", got, readErr)
	}
	if _, err := os.Stat(p.Pending); !os.IsNotExist(err) {
		t.Fatal("成功路径 pending 必须已被消费")
	}
	if _, err := os.Stat(filepath.Join(p.ExeDir, "sshore.v0.6.0")); err != nil {
		t.Fatalf("备份缺失: %v", err)
	}
	if _, err := os.Stat(filepath.Join(p.ExeDir, "sshore.v0.5.0")); !os.IsNotExist(err) {
		t.Fatal("更早的备份必须被清理（只留最近一份）")
	}
	if _, err := os.Stat(filepath.Join(p.ExeDir, "sshore-update.sh")); !os.IsNotExist(err) {
		t.Fatal("成功路径脚本必须自删")
	}
	// 成功路径会删除日志（spec §8.3 第 9 步），所以只能断言它不存在，不能断言内容
	if _, err := os.Stat(p.LogPath); !os.IsNotExist(err) {
		t.Fatal("成功路径日志必须被删除")
	}
}

func TestScriptCleanupKeepsPending(t *testing.T) {
	// 这条专门挡住「比较绝对路径与 glob 裸名恒为假 → 删掉 pending」的缺陷（spec §16.2 B2）。
	p := newFixture(t)
	// 做法：让第 4 步清理照常执行，但第 5 步（备份）必然失败 —— backup 指向不存在的目录。
	st, _ := os.Stat(p.Pending)
	p.Size = st.Size()
	p.Backup = filepath.Join(t.TempDir(), "missing-dir", "sshore.v0.6.0")
	log, err := runScript(t, p, exitedChild(t))
	if err == nil {
		t.Fatalf("备份失败必须报错, log=%s", log)
	}
	if _, err := os.Stat(p.Pending); err != nil {
		t.Fatalf("pending 不能被清理掉: %v", err)
	}
	if got, _ := os.ReadFile(p.Target); string(got) != "OLD" {
		t.Fatalf("失败时正式二进制必须保持旧内容: %q", got)
	}
}

func TestScriptWaitTimeoutKeepsEverything(t *testing.T) {
	p := newFixture(t)
	st, _ := os.Stat(p.Pending)
	p.Size = st.Size()
	p.Wait = time.Second
	// 用一个长存活子进程当「旧进程」，并把它 reap 掉以避免僵尸导致 kill -0 恒成功。
	sleeper := exec.Command("/bin/sh", "-c", "sleep 5")
	if err := sleeper.Start(); err != nil {
		t.Fatal(err)
	}
	pid := sleeper.Process.Pid
	t.Cleanup(func() { _ = sleeper.Process.Kill(); _ = sleeper.Wait() })
	log, err := runScript(t, p, pid)
	if err == nil {
		t.Fatal("等待超时必须失败")
	}
	if !strings.Contains(log, "RESULT=fail:wait") {
		t.Fatalf("日志应含 RESULT=fail:wait: %s", log)
	}
	if _, err := os.Stat(p.Pending); err != nil {
		t.Fatalf("超时必须保留 pending: %v", err)
	}
	if _, err := os.Stat(filepath.Join(p.ExeDir, "sshore-update.sh")); err != nil {
		t.Fatalf("超时必须保留脚本（供重试）: %v", err)
	}
}

func TestScriptLaunchFailureRollsBack(t *testing.T) {
	// 新版起不来（用立刻退出的假二进制模拟）：脚本必须回滚并保留 pending（spec §8.3）。
	p := newFixture(t)
	if err := os.WriteFile(p.Pending, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(p.Pending)
	p.Size = st.Size()
	log, err := runScript(t, p, exitedChild(t))
	if err == nil {
		t.Fatalf("新版起不来必须失败, log=%s", log)
	}
	if !strings.Contains(log, "RESULT=fail:launch") {
		t.Fatalf("日志应含 RESULT=fail:launch: %s", log)
	}
	if got, _ := os.ReadFile(p.Target); string(got) != "OLD" {
		t.Fatalf("回滚后正式二进制必须是旧内容: %q", got)
	}
	if _, err := os.Stat(p.Pending); err != nil {
		t.Fatalf("回滚后 pending 必须保留（供重试）: %v", err)
	}
	if _, err := os.Stat(filepath.Join(p.ExeDir, "sshore.v0.6.0")); !os.IsNotExist(err) {
		t.Fatal("回滚后备份必须已改回正式名")
	}
}

func TestScriptSelfDeletesWhenInvokedByRelativePath(t *testing.T) {
	// 计划里的脚本会在替换前 cd 到目标目录；若 $0 是相对路径，朴素的 rm -f "$0" 会静默失败，
	// 留下一个陈旧的 sshore-update.sh（本计划实测过这个 bug），所以脚本必须用 $SELF 绝对路径自删。
	p := newFixture(t)
	st, _ := os.Stat(p.Pending)
	p.Size = st.Size()
	body, err := ScriptBytes("linux")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p.ExeDir, "sshore-update.sh"), body, 0o755); err != nil {
		t.Fatal(err)
	}
	// 用相对文件名 + cmd.Dir 调用，模拟「相对路径」场景
	cmd := exec.Command("sh", "sshore-update.sh", ScriptArgs(p, exitedChild(t))...)
	cmd.Dir = p.ExeDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("脚本应成功: %v", err)
	}
	if _, err := os.Stat(filepath.Join(p.ExeDir, "sshore-update.sh")); !os.IsNotExist(err) {
		t.Fatal("相对路径调用时脚本也必须自删（用 $SELF）")
	}
}

func TestScriptArgsValidation(t *testing.T) {
	p := newFixture(t)
	// --target 不存在 → 参数校验失败：必须写 RESULT=fail:args 并按 spec §8.2 退 2
	p.Target = filepath.Join(p.ExeDir, "not-there")
	st, _ := os.Stat(p.Pending)
	p.Size = st.Size()
	log, err := runScript(t, p, exitedChild(t))
	if err == nil {
		t.Fatalf("target 不存在必须失败, log=%s", log)
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 2 {
		t.Fatalf("参数类失败必须退 2，实际 %v（log=%s）", err, log)
	}
	if !strings.Contains(log, "RESULT=fail:args") {
		t.Fatalf("参数类失败必须写 RESULT=fail:args: %s", log)
	}
}

```

- [ ] **Step 2: 运行测试，逐条确认行为**

```bash
go test ./internal/update/ -run TestScript -v
```

Expected: 6 个测试 PASS（成功 / 清理保留 pending / 相对路径自删 / 等待超时 / 参数非法 / 新版起不来回滚）。**若 `TestScriptCleanupKeepsPending` 失败，说明脚本第 4 步在删 pending —— 回到 Task 6 第 4 步修 cd + basename 比较。**

- [ ] **Step 3: 提交**

```bash
git add internal/update/script_run_test.go
git commit -m "test(update): 脚本真跑 —— 成功/超时/参数非法/清理保留 pending 四条路径"
```

---

### Task 13: 前端 utils/update.js —— 状态文案与按钮矩阵（纯函数）

**Files:**
- Create: `frontend/src/utils/update.js`
- Test: `frontend/src/utils/update.test.js`

**Interfaces:**
- Consumes: 后端 `UpdateInfo` JSON（`state`/`latest`/`hint`/`pending_log`/`skipped`/`progress`/`manual`/`current`）
- Produces: `export const STATES`（常量表）；`export function stateLabel(info)`；`export function statusMessage(info)`；`export function actions(info)`（返回 `{check, download, cancel, apply, discard, skip, clearSkip, openPage}` 布尔）; `export function formatBytes(n)`；`export function progressPercent(info)`

- [ ] **Step 1: 写失败测试（先钉死按钮矩阵——这是评审发现的缺边所在）**

```js
import { describe, it, expect } from "vitest";
import { actions, stateLabel, statusMessage, progressPercent, formatBytes } from "./update";

const base = { state: "idle", current: "v0.6.0", latest: "", hint: "", pending_log: "", skipped: false, progress: 0, manual: false };

describe("actions", () => {
  it("available 才允许下载与跳过", () => {
    const a = actions({ ...base, state: "available", latest: "v0.7.0" });
    expect(a.download).toBe(true);
    expect(a.skip).toBe(true);
    expect(a.apply).toBe(false);
    expect(a.cancel).toBe(false);
  });

  it("verify-failed 与 io-failed 都能重试下载（补齐状态机缺边）", () => {
    for (const state of ["verify-failed", "io-failed"]) {
      const a = actions({ ...base, state, latest: "v0.7.0" });
      expect(a.download, state).toBe(true);
    }
  });

  it("downloading 只给取消", () => {
    const a = actions({ ...base, state: "downloading" });
    expect(a.cancel).toBe(true);
    expect(Object.entries(a).filter(([k, v]) => v && k !== "cancel" && k !== "openPage")).toHaveLength(0);
  });

  it("ready 才出现重启并升级，且可删除已下载", () => {
    const a = actions({ ...base, state: "ready" });
    expect(a.apply).toBe(true);
    expect(a.discard).toBe(true);
    expect(a.download).toBe(false);
  });

  it("disabled / skipped 不允许下载，skipped 允许取消跳过", () => {
    expect(actions({ ...base, state: "disabled" }).download).toBe(false);
    const s = actions({ ...base, state: "skipped", latest: "v0.7.0", skipped: true });
    expect(s.download).toBe(false);
    expect(s.clearSkip).toBe(true);
  });

  it("applying 时全部禁用", () => {
    const a = actions({ ...base, state: "applying" });
    expect(Object.values(a).every((v) => v === false)).toBe(true);
  });
});

describe("labels", () => {
  it("非 release 构建说明无法判定是否最新", () => {
    const info = { ...base, current: "v0.6.0-80-gc2d2a36", state: "available", latest: "v0.7.0" };
    expect(stateLabel(info)).toContain("本地构建");
    expect(statusMessage(info)).toContain("无法判定");
  });

  it("io-failed + hint 给人工升级指引", () => {
    const info = { ...base, state: "io-failed", hint: "manual-upgrade" };
    expect(statusMessage(info)).toContain("手动");
  });

  it("pending_log 非空时提示上次升级未完成", () => {
    const info = { ...base, state: "idle", pending_log: "/opt/sshore/sshore-update.log" };
    expect(statusMessage(info)).toContain("上次升级未完成");
  });
});

describe("progress", () => {
  it("未知总大小返回 null（UI 用 indeterminate）", () => {
    expect(progressPercent({ ...base, state: "downloading", progress: -1 })).toBeNull();
  });
  it("已知进度返回 0-100", () => {
    expect(progressPercent({ ...base, state: "downloading", progress: 42 })).toBe(42);
  });
  it("formatBytes 走二进制单位", () => {
    expect(formatBytes(0)).toBe("0 B");
    expect(formatBytes(2048)).toBe("2.0 KB");
  });
});
```

- [ ] **Step 2: 运行测试确认失败**

```bash
cd frontend && npx vitest run src/utils/update.test.js
```

Expected: FAIL —— 模块不存在。

- [ ] **Step 3: 实现**

```js
// 更新区的展示逻辑全部是纯函数：组件只做绑定，测试不碰 DOM（沿用仓库无 jsdom 的风格）。
export const STATES = ["idle", "checking", "up-to-date", "available", "skipped", "rate-limited",
  "check-failed", "no-asset", "no-checksum", "downloading", "verify-failed", "io-failed",
  "ready", "applying", "disabled"];

const isLocalBuild = (info) => /-[0-9]+-g[0-9a-f]+$/.test(info.current || "") || !info.current || info.current === "dev";

export function stateLabel(info) {
  if (isLocalBuild(info)) return `本地构建 ${info.current || "unknown"}（dev）`;
  return `当前版本 ${info.current || "unknown"}`;
}

export function statusMessage(info) {
  const st = info.state;
  if (info.pending_log) return "上次升级未完成，可重试升级或查看日志";
  switch (st) {
    case "checking": return "正在检查更新…";
    case "up-to-date": return "已是最新版本";
    case "disabled": return "已关闭自动检查（可手动检查）";
    case "skipped": return `已跳过 ${info.latest || "该版本"}`;
    case "rate-limited": return "更新源限流，请稍后再试或手动下载";
    case "check-failed": return info.error ? `检查失败：${info.error}` : "检查失败";
    case "no-asset": return "发现新版本，但本平台暂无可用包";
    case "no-checksum": return "该版本未提供校验文件，已拒绝自动升级";
    case "available": return isLocalBuild(info)
      ? `发现 ${info.latest}（本地构建无法判定是否最新）`
      : `发现新版本 ${info.latest}`;
    case "downloading": return "正在下载并校验…";
    case "verify-failed": return "下载内容校验失败，已丢弃，可重试";
    case "io-failed": return info.hint === "manual-upgrade" ? "安装目录不可写，请按手动步骤升级" : "写入失败，可重试";
    case "ready": return `${info.latest || "新版本"} 已就绪，点击「重启并升级」生效`;
    case "applying": return "正在重启…";
    default: return "尚未检查更新";
  }
}

export function actions(info) {
  const st = info.state;
  const none = { check: false, download: false, cancel: false, apply: false, discard: false, skip: false, clearSkip: false, openPage: false };
  if (st === "applying") return none;
  const a = { ...none, openPage: true };
  if (st === "downloading") {
    a.cancel = true;
    a.openPage = false;
    return a;
  }
  if (st === "ready") {
    a.apply = true;
    a.discard = true;
    return a;
  }
  if (["available", "verify-failed", "io-failed"].includes(st)) {
    a.download = true;
    a.check = true;
    if (st === "available") a.skip = true;
    return a;
  }
  if (st === "skipped") {
    a.clearSkip = true;
    a.check = true;
    return a;
  }
  a.check = true;
  return a;
}

export function progressPercent(info) {
  if (info.progress == null || info.progress < 0) return null;
  return Math.max(0, Math.min(100, info.progress));
}

export function formatBytes(n) {
  if (!n || n <= 0) return "0 B";
  const units = ["B", "KB", "MB", "GB"];
  let v = n;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i += 1;
  }
  return i === 0 ? `${v} B` : `${v.toFixed(1)} ${units[i]}`;
}
```

- [ ] **Step 4: 运行测试确认通过**

```bash
cd frontend && npx vitest run src/utils/update.test.js
```

Expected: PASS（12 个用例）。

- [ ] **Step 5: 提交**

```bash
git add frontend/src/utils/update.js frontend/src/utils/update.test.js
git commit -m "feat(web): 更新区状态文案与按钮矩阵（纯函数 + 单测）"
```

---

### Task 14: 前端 stores/update.js —— 事件归约（seq）、角标派生、动作封装

**Files:**
- Create: `frontend/src/stores/update.js`、`frontend/src/stores/update.test.js`

**Interfaces:**
- Consumes: `GetUpdateInfo`/`CheckUpdate`/`StartUpdateDownload`/`CancelUpdateDownload`/`ApplyUpdateAndRestart`/`DiscardUpdateDownload`/`SkipUpdateVersion`/`ClearSkippedUpdate`/`OpenReleasePage`（Task 11 生成的绑定）；事件 `update:state`/`update:progress`
- Produces: `useUpdateStore()`（Pinia）：`state.info`、`getters.badgeVisible`、`actions.hydrate()`、`actions.applyState(payload)`、`actions.applyProgress(payload)`、`actions.acknowledge()`、`actions.check/download/cancel/apply/discard/skip/clearSkip/openPage`

- [ ] **Step 1: 写失败测试**

```js
import { describe, it, expect, beforeEach } from "vitest";
import { createPinia, setActivePinia } from "pinia";
import { useUpdateStore } from "./update";

const info = (over = {}) => ({ seq: 1, state: "idle", current: "v0.6.0", latest: "", progress: 0, skipped: false, hint: "", pending_log: "", ...over });

describe("update store", () => {
  beforeEach(() => setActivePinia(createPinia()));

  it("按 seq 丢弃迟到的旧载荷", () => {
    const s = useUpdateStore();
    s.applyState(info({ seq: 5, state: "available", latest: "v0.7.0" }));
    s.applyState(info({ seq: 3, state: "idle" }));
    expect(s.info.state).toBe("available");
  });

  it("progress 只更新进度字段", () => {
    const s = useUpdateStore();
    s.applyState(info({ seq: 1, state: "downloading" }));
    s.applyProgress({ done: 10, total: 100, percent: 10 });
    expect(s.info.progress).toBe(10);
    expect(s.info.state).toBe("downloading");
  });

  it("available 时角标可见，acknowledge 后消失；换版本后重新可见", () => {
    const s = useUpdateStore();
    s.applyState(info({ seq: 1, state: "available", latest: "v0.7.0" }));
    expect(s.badgeVisible).toBe(true);
    s.acknowledge();
    expect(s.badgeVisible).toBe(false);
    s.applyState(info({ seq: 2, state: "available", latest: "v0.8.0" }));
    expect(s.badgeVisible).toBe(true);
  });

  it("available 已确认后进入 ready 会重新亮（spec §12.2）", () => {
    const s = useUpdateStore();
    s.applyState(info({ seq: 1, state: "available", latest: "v0.7.0" }));
    s.acknowledge();
    expect(s.badgeVisible).toBe(false);
    s.applyState(info({ seq: 2, state: "ready", latest: "v0.7.0" }));
    expect(s.badgeVisible).toBe(true);
  });

  it("skipped / disabled 不亮角标", () => {
    const s = useUpdateStore();
    s.applyState(info({ seq: 1, state: "skipped", latest: "v0.7.0", skipped: true }));
    expect(s.badgeVisible).toBe(false);
    s.applyState(info({ seq: 2, state: "disabled" }));
    expect(s.badgeVisible).toBe(false);
  });

  it("ready 状态也亮角标", () => {
    const s = useUpdateStore();
    s.applyState(info({ seq: 1, state: "ready", latest: "v0.7.0" }));
    expect(s.badgeVisible).toBe(true);
  });
});
```

- [ ] **Step 2: 运行测试确认失败**

```bash
cd frontend && npx vitest run src/stores/update.test.js
```

Expected: FAIL —— 模块不存在。

- [ ] **Step 3: 实现（先订阅再取快照；seq 单调）**

```js
import { defineStore } from "pinia";
import { EventsOn } from "../../wailsjs/runtime/runtime";
import {
  GetUpdateInfo, CheckUpdate, StartUpdateDownload, CancelUpdateDownload,
  ApplyUpdateAndRestart, DiscardUpdateDownload, SkipUpdateVersion, ClearSkippedUpdate, OpenReleasePage,
} from "../../wailsjs/go/main/App";

const emptyInfo = { seq: 0, state: "idle", current: "", latest: "", notes: "", published_at: "", source: "", progress: 0, ready_path: "", skipped: false, manual: false, hint: "", error: "", pending_log: "" };

export const useUpdateStore = defineStore("update", {
  state: () => ({
    info: { ...emptyInfo },
    ackedVersion: "",
    listening: false,
    err: "",
  }),
  getters: {
    badgeVisible: (s) => ["available", "ready"].includes(s.info.state) && s.info.latest !== s.ackedVersion,
    progress: (s) => s.info.progress,
  },
  actions: {
    ensureListening() {
      if (this.listening) return;
      this.listening = true;
      // 先订阅，再取快照：否则「快照晚于事件」会用旧值覆盖新状态；seq 是第二道保险。
      EventsOn("update:state", (payload) => this.applyState(payload));
      EventsOn("update:progress", (payload) => this.applyProgress(payload));
    },
    async hydrate() {
      this.ensureListening();
      try {
        this.applyState(await GetUpdateInfo());
      } catch (e) {
        this.err = String(e);
      }
    },
    applyState(payload) {
      if (!payload) return;
      if (Number(payload.seq || 0) < Number(this.info.seq || 0)) return;
      this.info = { ...this.info, ...payload };
    },
    applyProgress(payload) {
      if (!payload) return;
      this.info = { ...this.info, progress: payload.percent };
    },
    acknowledge() {
      this.ackedVersion = this.info.latest || "";
    },
    async check() { this.applyState(await CheckUpdate(true)); },
    async download() { await StartUpdateDownload(); },
    async cancel() { await CancelUpdateDownload(); },
    async apply() { await ApplyUpdateAndRestart(); },
    async discard() { await DiscardUpdateDownload(); this.applyState(await GetUpdateInfo()); },
    async skip(version) { await SkipUpdateVersion(version); this.applyState(await GetUpdateInfo()); },
    async clearSkip() { await ClearSkippedUpdate(); this.applyState(await GetUpdateInfo()); },
    async openPage() { await OpenReleasePage(); },
  },
});
```

- [ ] **Step 4: 运行测试确认通过**

```bash
cd frontend && npx vitest run src/stores/update.test.js
```

Expected: PASS（5 个用例）。

- [ ] **Step 5: 提交**

```bash
git add frontend/src/stores/update.js frontend/src/stores/update.test.js
git commit -m "feat(web): 更新状态 store（seq 归约、角标派生、动作封装）"
```

---

### Task 15: 设置页「更新」区 + settings store 新字段（含跳过双写修复）

**Files:**
- Create: `frontend/src/components/UpdateSection.vue`
- Modify: `frontend/src/components/SettingsDialog.vue`、`frontend/src/stores/settings.js`
- Test: `frontend/src/stores/settings.test.js`（追加）与 `frontend/src/components/updateSection.test.js`（SSR 渲染）

**Interfaces:**
- Consumes: `utils/update.js`（Task 13）、`stores/update.js`（Task 14）、`store.theme` 等既有 settings store
- Produces: `<UpdateSection />` 组件；settings store 新增 `updateCheckAuto`/`updateCheckIntervalHours`/`updateSkippedVersion`/`updateSource`（save payload 用 snake_case 键）

- [ ] **Step 1: 先修 settings store（跳过版本不能被前端全量回写清掉）**

```js
// state 追加
    updateCheckAuto: true,
    updateCheckIntervalHours: 12,
    updateSkippedVersion: "",
    updateSource: "",

// load() 追加（缺失键按默认）
      this.updateCheckAuto = s.update_check_auto !== false;
      this.updateCheckIntervalHours = Number.isFinite(Number(s.update_check_interval_hours)) ? Number(s.update_check_interval_hours) : 12;
      this.updateSkippedVersion = s.update_skipped_version || "";
      this.updateSource = s.update_source || "";

// save() 追加（显式字段清单）
        update_check_auto: this.updateCheckAuto,
        update_check_interval_hours: this.updateCheckIntervalHours,
        update_skipped_version: this.updateSkippedVersion,
        update_source: this.updateSource,
```

- [ ] **Step 2: 写测试（含「跳过 → 改主题 → 保存 → 跳过仍在」）**

沿用仓库既有做法（`stores/settings.test.js` 的 `vi.hoisted` + `vi.mock("../../wailsjs/go/main/App")`，把绑定挡在测试之外）。**注意：下面两个用例要放进文件里已有的 `describe`（其 `beforeEach` 调用 `setActivePinia(createPinia())`），否则 `useSettingsStore()` 会因没有活动 pinia 而报错**：

```js
// stores/settings.test.js：扩展 backend 假后端，并在测试里模拟「后端先写跳过版本、前端再重读」
it("跳过版本不会被后续的主题保存清掉（评审发现的真实缺陷）", async () => {
  backend.settings = {
    theme: "system", font_scale: 1,
    update_check_auto: true, update_check_interval_hours: 12,
    update_skipped_version: "0.7.0", update_source: "",
  };
  const s = useSettingsStore();
  await s.load();          // 前端拿到含 0.7.0 的快照
  s.theme = "dark";
  await s.save();          // 全量回写
  expect(backend.saved.at(-1).update_skipped_version).toBe("0.7.0");
});

it("跳过版本会在动作后被重读进前端快照", async () => {
  backend.settings = { update_skipped_version: "" };
  const s = useSettingsStore();
  await s.load();
  backend.settings = { update_skipped_version: "0.8.0" };  // 后端被 SkipeVersion 直接改了
  await s.load();
  expect(s.updateSkippedVersion).toBe("0.8.0");
});
```

- [ ] **Step 3: 写 UpdateSection.vue（元素与按钮矩阵由 utils 决定，模板只做绑定）**

```vue
<script setup>
import { computed, onMounted, ref } from "vue";
import { useUpdateStore } from "../stores/update";
import { useSettingsStore } from "../stores/settings";
import { stateLabel, statusMessage, actions, progressPercent, formatBytes } from "../utils/update";

const update = useUpdateStore();
const settings = useSettingsStore();
const confirmCustomSource = ref(false);
const customSource = computed(() => Boolean(settings.updateSource));

const acts = computed(() => actions(update.info));
const label = computed(() => stateLabel(update.info));
const message = computed(() => statusMessage(update.info));
const percent = computed(() => progressPercent(update.info));
// 更新说明可能很长：截断到 2000 字符（spec §12.1）
const notesExcerpt = computed(() => {
  const n = update.info.notes || "";
  return n.length > 2000 ? n.slice(0, 2000) + "…" : n;
});

onMounted(() => { update.hydrate(); });

// 跳过/取消跳过后必须重读设置：后端直接改了 TOML，前端快照会过期（spec §6）。
async function skip() { await update.skip(update.info.latest); await settings.load(); }
async function clearSkip() { await update.clearSkip(); await settings.load(); }
async function applyNow() {
  if (customSource.value && !globalThis.confirm("更新源为自定义地址，确认继续？")) return;
  await update.apply();
}
</script>

<template>
  <section class="group update">
    <h3>更新</h3>
    <p class="meta">{{ label }}</p>
    <p class="status">{{ message }}</p>
    <p v-if="update.info.pending_log" class="warn">日志：{{ update.info.pending_log }}</p>
    <div v-if="update.info.state === 'downloading'" class="bar">
      <div class="fill" :style="{ width: (percent ?? 5) + '%' }" />
      <span v-if="percent !== null">{{ percent }}%</span>
    </div>
    <details v-if="notesExcerpt"><summary>更新说明</summary><pre>{{ notesExcerpt }}</pre></details>
    <div class="btns">
      <button v-if="acts.check" :disabled="update.info.state === 'checking'" @click="update.check()">检查更新</button>
      <button v-if="acts.download" class="primary" @click="update.download()">下载更新</button>
      <button v-if="acts.cancel" @click="update.cancel()">取消下载</button>
      <button v-if="acts.apply" class="primary" @click="applyNow()">重启并升级</button>
      <button v-if="acts.discard" @click="update.discard()">删除已下载的更新</button>
      <button v-if="acts.skip" @click="skip()">跳过此版本</button>
      <button v-if="acts.clearSkip" @click="clearSkip()">取消跳过</button>
      <button v-if="acts.openPage" @click="update.openPage()">打开发布页</button>
    </div>
    <div class="field"><label><input v-model="settings.updateCheckAuto" type="checkbox" /> 自动检查更新</label></div>
    <div class="field">
      <label for="update-interval">检查间隔</label>
      <select id="update-interval" v-model.number="settings.updateCheckIntervalHours">
        <option :value="6">6 小时</option>
        <option :value="12">12 小时</option>
        <option :value="24">24 小时</option>
        <option :value="0">关闭轮询</option>
      </select>
    </div>
    <div class="field">
      <label for="update-source">更新源</label>
      <input id="update-source" v-model="settings.updateSource" placeholder="默认 GitHub（可填镜像 API 地址）" />
      <button class="link" @click="settings.updateSource = ''">恢复默认</button>
    </div>
    <p v-if="customSource && !settings.updateSource.startsWith('https://')" class="warn">非本机地址必须使用 https。</p>
  </section>
</template>
```

- [ ] **Step 4: 挂到 SettingsDialog（**帮助段下方**，见 spec §5.4）并加 SSR 渲染测试**

```vue
<!-- SettingsDialog.vue：import UpdateSection from "./UpdateSection.vue"，在 <section class="group help"> 之后插入： -->
<UpdateSection />
```

```js
import { describe, it, expect } from "vitest";
import { createSSRApp } from "vue";
import { renderToString } from "@vue/server-renderer";
import { createPinia } from "pinia";
import UpdateSection from "./UpdateSection.vue";

describe("UpdateSection SSR", () => {
  it("渲染标题与检查按钮", async () => {
    const app = createSSRApp(UpdateSection);
    app.use(createPinia());
    const html = await renderToString(app);
    expect(html).toContain("更新");
    expect(html).toContain("检查更新");
  });
});
```

- [ ] **Step 5: 运行前端测试确认通过**

```bash
cd frontend && npx vitest run src/stores/settings.test.js src/components/updateSection.test.js
```

Expected: PASS。

- [ ] **Step 6: 提交**

```bash
git add frontend/src/components/UpdateSection.vue frontend/src/components/updateSection.test.js frontend/src/components/SettingsDialog.vue frontend/src/stores/settings.js frontend/src/stores/settings.test.js
git commit -m "feat(web): 设置页更新区 + 四个设置字段 + 跳过版本重读修复"
```

---

### Task 16: 侧栏角标 + 结构性不变量测试

**Files:**
- Modify: `frontend/src/App.vue`
- Test: `frontend/src/views/updateWiring.test.js`（新建，仿 `sftpDropWiring.test.js` 的结构性断言）

**Interfaces:**
- Consumes: `useUpdateStore`（Task 14）
- Produces: 侧栏「⚙ 设置」按钮上的角标；不变量测试

- [ ] **Step 1: App.vue 加角标（单一来源：store 派生值）**

```vue
// script 追加
import { useUpdateStore } from "./stores/update";
const updateStore = useUpdateStore();
function openSettings() { settingsVisible.value = true; updateStore.acknowledge(); }

// 模板：设置按钮改为 @click="openSettings"，并加角标
<button class="settings" @click="openSettings">
  ⚙ 设置
  <span v-if="updateStore.badgeVisible" class="badge" aria-label="有可用更新" />
</button>

// 样式追加
.sidebar .settings { position: relative; }
.sidebar .badge { position: absolute; top: 10px; right: 14px; width: 8px; height: 8px; border-radius: 50%; background: var(--accent); }

// onMounted 里补一次快照（前端不调度检查，只取后端快照）
  updateStore.hydrate()
```

- [ ] **Step 2: 写结构性不变量测试**

```js
import { describe, it, expect } from "vitest";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, resolve } from "node:path";

const here = dirname(fileURLToPath(import.meta.url));
const read = (p) => readFileSync(resolve(here, p), "utf8");

describe("更新功能的结构性不变量", () => {
  it("角标只有一个来源（store 派生值），不额外拉取", () => {
    const app = read("../App.vue");
    expect(app).toContain("updateStore.badgeVisible");
    expect(app).not.toContain("CheckUpdate(");
  });

  it("打开设置时确认角标（acknowledge）", () => {
    const app = read("../App.vue");
    expect(app).toContain("updateStore.acknowledge()");
  });

  it("自动检查的默认值来自后端，不在前端硬编码开关默认", () => {
    const settings = read("../stores/settings.js");
    expect(settings).toContain("s.update_check_auto !== false");
  });

  it("跳过与取消跳过之后必须重读设置（否则前端快照会把它清掉）", () => {
    const sec = read("../components/UpdateSection.vue");
    expect(sec).toMatch(/skip\(\)[\s\S]*settings\.load\(\)/);
    expect(sec).toMatch(/clearSkip\(\)[\s\S]*settings\.load\(\)/);
  });

  it("重启并升级只在 ready 时出现（由 utils 的按钮矩阵决定）", () => {
    const sec = read("../components/UpdateSection.vue");
    expect(sec).toContain("acts.apply");
  });
});
```

- [ ] **Step 3: 运行前端全部测试确认通过**

```bash
cd frontend && npx vitest run
```

Expected: 全部 PASS（新增用例 + 既有用例不回归；**以 `npx vitest` 实际输出为准**，spec §14.1 明确不写死数字）。

- [ ] **Step 4: 提交**

```bash
git add frontend/src/App.vue frontend/src/views/updateWiring.test.js
git commit -m "feat(web): 设置按钮更新角标 + 结构性不变量测试"
```

---

### Task 17: CI —— checksums、shellcheck、Windows 真跑脚本

**Files:**
- Modify: `.github/workflows/ci.yml`

**Interfaces:**
- Consumes: 打包产物命名（`sshore-<v>-<os>-amd64.{tar.gz,zip}`）、`internal/update/scripts/update.sh|cmd`
- Produces: Release 附带 `checksums.txt`；Linux job 跑 shellcheck；go-windows job 用真实 `cmd.exe` 跑脚本

- [ ] **Step 1: 在既有 Linux 测试步骤加 shellcheck**

```yaml
      - name: shellcheck 升级脚本
        run: |
          if ! command -v shellcheck >/dev/null; then sudo apt-get update && sudo apt-get install -y shellcheck; fi
          shellcheck internal/update/scripts/update.sh
```

- [ ] **Step 2: 在 go-windows job 加「真实 cmd.exe 跑脚本」**

```yaml
      # 用真实 PE 覆盖「成功」与「回滚」两条路径（spec §11.3/§13.2、DoD #4）：
      #   · 成功：把一个长期存活的真 exe（cmd.exe）当「新版本」，存活探测通过 → RESULT=ok
      #   · 回滚：把一个立刻退出的真 exe（whoami.exe）当「新版本」，探测失败 → RESULT=fail:launch
      - name: 真实 cmd.exe 跑 update.cmd（成功路径）
        shell: pwsh
        run: |
          $dir = Join-Path $env:RUNNER_TEMP "upd-ok"
          New-Item -ItemType Directory -Force $dir | Out-Null
          Copy-Item "$env:SystemRoot\System32\whoami.exe" (Join-Path $dir "sshore.exe")
          Copy-Item "$env:SystemRoot\System32\cmd.exe" (Join-Path $dir "sshore.v0.7.0.exe")
          Copy-Item internal/update/scripts/update.cmd (Join-Path $dir "sshore-update.cmd")
          $size = (Get-Item (Join-Path $dir "sshore.v0.7.0.exe")).Length
          $env:SSHORE_PID = "1"
          $env:SSHORE_TARGET = Join-Path $dir "sshore.exe"
          $env:SSHORE_PENDING = Join-Path $dir "sshore.v0.7.0.exe"
          $env:SSHORE_BACKUP = Join-Path $dir "sshore.v0.6.0.exe"
          $env:SSHORE_SIZE = "$size"
          $env:SSHORE_LOG = Join-Path $dir "sshore-update.log"
          $env:SSHORE_WAIT = "10"
          Push-Location $dir; cmd /d /c "sshore-update.cmd"; $code = $LASTEXITCODE; Pop-Location
          if ($code -ne 0) { throw "成功路径应退 0，实际 $code" }
          if (-not (Test-Path (Join-Path $dir "sshore.v0.6.0.exe"))) { throw "缺少备份" }
          if ((Get-Item (Join-Path $dir "sshore.exe")).Length -ne $size) { throw "未替换为新版本" }
          if (Test-Path (Join-Path $dir "sshore.v0.7.0.exe")) { throw "pending 未被消费" }
          if (Test-Path (Join-Path $dir "sshore-update.cmd")) { throw "脚本未自删" }
          if (Test-Path (Join-Path $dir "sshore-update.log")) { throw "日志未删除" }
          taskkill /IM sshore.exe /F 2>$null | Out-Null   # 收掉存活探测留下的 cmd.exe

      - name: 真实 cmd.exe 跑 update.cmd（存活探测失败 → 回滚）
        shell: pwsh
        run: |
          $dir = Join-Path $env:RUNNER_TEMP "upd-fail"
          New-Item -ItemType Directory -Force $dir | Out-Null
          Copy-Item "$env:SystemRoot\System32\cmd.exe" (Join-Path $dir "sshore.exe")
          $oldSize = (Get-Item (Join-Path $dir "sshore.exe")).Length
          Copy-Item "$env:SystemRoot\System32\whoami.exe" (Join-Path $dir "sshore.v0.7.0.exe")
          Copy-Item internal/update/scripts/update.cmd (Join-Path $dir "sshore-update.cmd")
          $size = (Get-Item (Join-Path $dir "sshore.v0.7.0.exe")).Length
          $env:SSHORE_PID = "1"
          $env:SSHORE_TARGET = Join-Path $dir "sshore.exe"
          $env:SSHORE_PENDING = Join-Path $dir "sshore.v0.7.0.exe"
          $env:SSHORE_BACKUP = Join-Path $dir "sshore.v0.6.0.exe"
          $env:SSHORE_SIZE = "$size"
          $env:SSHORE_LOG = Join-Path $dir "sshore-update.log"
          $env:SSHORE_WAIT = "10"
          Push-Location $dir; cmd /d /c "sshore-update.cmd"; $code = $LASTEXITCODE; Pop-Location
          $log = Get-Content (Join-Path $dir "sshore-update.log") -Raw
          if ($code -eq 0) { throw "新版起不来时脚本必须非 0 退出" }
          if ($log -notmatch "RESULT=fail:launch") { throw "日志应记录 RESULT=fail:launch，实际：$log" }
          if ((Get-Item (Join-Path $dir "sshore.exe")).Length -ne $oldSize) { throw "回滚后必须是旧版本" }
          if (-not (Test-Path (Join-Path $dir "sshore.v0.7.0.exe"))) { throw "回滚后 pending 必须保留" }
```

这样 Windows CI 就覆盖了 spec §13.2 要求的 1/2/3/4 与「存活探测失败回滚」：成功路径用长期存活的真 PE（`cmd.exe`，探测通过后由 `taskkill` 收掉），回滚路径用立刻退出的真 PE（`whoami.exe`）。**不再依赖「文本文件当 exe」这种不确定行为**（两阶段评审指出它会 flaky 并自相矛盾）。

- [ ] **Step 3: release job 生成并上传 checksums.txt**

```yaml
      - name: 生成 checksums.txt（Linux runner 上统一算，避免 Windows 格式差异）
        run: |
          cd artifacts
          find . -type f \( -name "*.tar.gz" -o -name "*.zip" \) -print0 \
            | sort -z \
            | xargs -0 sha256sum \
            | awk '{ n=$0; sub(/^[^ ]+  /, "", n); sub(/^.*\//, "", n); print $1"  "n }' > checksums.txt
          cat checksums.txt

      - name: Publish binaries to GitHub Release
        env:
          GH_TOKEN: ${{ github.token }}
          RELEASE_TAG: ${{ github.event_name == 'workflow_dispatch' && github.event.inputs.release_tag || github.ref_name }}
        run: |
          mapfile -t FILES < <(find artifacts -type f \( -name "*.tar.gz" -o -name "*.zip" -o -name "checksums.txt" \) | sort)
          ...（其余与现有步骤一致）
```

- [ ] **Step 4: 本地验证 checksums 语义（不要求字节级一致）**

```bash
mkdir -p /tmp/ck && cd /tmp/ck && printf x > sshore-v0.7.0-linux-amd64.tar.gz && printf y > sshore-v0.7.0-windows-amd64.zip
find . -type f \( -name "*.tar.gz" -o -name "*.zip" \) -print0 | sort -z | xargs -0 sha256sum | awk '{ n=$0; sub(/^[^ ]+  /, "", n); sub(/^.*\\/, "", n); print $1"  "n }' > checksums.txt
cat checksums.txt
```

Expected: 两行 `<hash>  <文件名>`（**只含 basename，不含目录**），且哈希与 `sha256sum <file>` 一致。

**必须按 CI 的真实布局验证**（`actions/download-artifact` 不带 `name` 时会把每个 artifact 放进 `artifacts/<artifact 名>/` 子目录）：

```bash
mkdir -p /tmp/ck/sshore-linux && cd /tmp/ck && printf x > sshore-linux/sshore-v0.7.0-linux-amd64.tar.gz
find . -type f -name "*.tar.gz" -print0 | sort -z | xargs -0 sha256sum | awk '{ n=$0; sub(/^[^ ]+  /, "", n); sub(/^.*\\/, "", n); print $1"  "n }'
```

Expected: 输出是 `hash  sshore-v0.7.0-linux-amd64.tar.gz`（**不是** `hash  ./sshore-linux/...`）；否则应用按 basename 查表会永远 verify-failed。

- [ ] **Step 5: 提交**

```bash
git add .github/workflows/ci.yml
git commit -m "ci: 校验文件生成、shellcheck 与 Windows 侧真实 cmd 跑升级脚本"
```

---

### Task 18: README「更新与升级」一节

**Files:**
- Modify: `README.md`（在「下载安装」之后新增一节）、`README.en.md`（同节英文，保持双语）

- [ ] **Step 1: 写中文节（内容必须覆盖 spec §14.9 的全部要点）**

```markdown
## 更新与升级

sshore 会按设置里的间隔（默认 12 小时）向**更新源**查询最新 Release，并在侧栏「⚙ 设置」上显示角标；
检查与下载的进度都在「设置 → 更新」里可见。

**更新源与校验**

- 默认源是 `https://api.github.com/repos/i2534/sshore`；可在「设置 → 更新」里换成镜像/内网地址（非本机地址必须 `https://`，且需要提供 GitHub 兼容的 API）。
- 校验强度：发布产物配套 `checksums.txt`（SHA256），下载后比对；比对失败**拒绝安装**。
- 能防什么：传输损坏、半包、资产错发。**不能防**更新源被攻破或 GitHub 账户被盗后发布的被篡改版本 —— 这类风险只能靠 HTTPS + 上游账户安全。

**升级流程（必须由你点一下）**

1. 有新版时点「下载更新」：程序下载压缩包 → 校验 SHA256 → 解包出待安装文件（与当前二进制同目录，带版本号）→ 写入同名 `.sha256`。
2. 点「重启并升级」：程序写出一次性升级脚本、分离启动它、然后自己退出。
3. 脚本等旧进程退出 → 把当前二进制备份为 `sshore.<旧版本号>`（只保留最近一份）→ 把待安装文件换成正式名 → 启动新版；若新版起不来会**自动回滚**并保留待安装文件。
4. 成功会删除脚本与日志；失败会保留 `sshore-update.log`，下次启动时「设置 → 更新」会提示「上次升级未完成」并可重试。

**隐私**

检查更新会向更新源发出一个带版本号 User-Agent 的 HTTPS 请求（仅此而已，不上报任何本机信息）。关闭「自动检查更新」后只有你手动点「检查更新」时才会请求。

**手动升级（不依赖脚本）**

```bash
# Linux
tar -xzf sshore-v0.7.0-linux-amd64.tar.gz -C /tmp/upd
install -m 0755 /tmp/upd/sshore ~/bin/sshore
```

```powershell
# Windows：退出 sshore 后，把解压出来的 sshore.exe 覆盖到原位置即可
```

**杀毒软件误报**

自升级会「联网下载可执行文件并替换自身」，这和部分恶意软件的行为特征重合；Release 产物又经过 UPX 压缩（`README` 的「尺寸优化」一节已说明 UPX 本身易被误报）。如果被杀软拦截：

1. 用上面的**手动升级**步骤；
2. 或自行 `make both COMPRESS=0` 构建不带 UPX 的版本。
```

- [ ] **Step 2: 提交**

```bash
git add README.md README.en.md
git commit -m "docs: README 增「更新与升级」一节（源/校验/隐私/手动步骤/AV 注意事项）"
```

---

### Task 19: 端到端验收、DoD 与变异校验

**Files:**
- Create（**入库**）: `docs/superpowers/reports/2026-09-19-update-check-upgrade-acceptance.md`（证据清单 + 结论；DoD #5/#6/#7/#10 的可复核依据）。原始截图/日志可放 `.superpowers/update-2026-09-19/`（gitignore），但报告里要写清每个证据文件的路径
- Create: `e2e/fake_update_source.py`（把假源与「现场构建产物」的用法固化成可复现脚本，入库）

- [ ] **Step 1: 写假源脚本（本地 HTTP，提供 /releases/latest 与资产）**

```python
#!/usr/bin/env python3
"""为更新功能提供本地假源：/releases/latest 的 JSON + 资产 + checksums.txt。

用法：fake_update_source.py <端口> <目录> <tag>；目录里放：
  sshore-v<tag>-linux-amd64.tar.gz / -windows-amd64.zip、checksums.txt
（三个参数都必填：代码从 sys.argv[1..3] 读 PORT/ROOT/TAG）
"""
import hashlib
import json
import pathlib
import sys
from http.server import BaseHTTPRequestHandler, HTTPServer

PORT = int(sys.argv[1])
ROOT = pathlib.Path(sys.argv[2])
TAG = sys.argv[3]

def assets():
    out = []
    for p in sorted(ROOT.iterdir()):
        out.append({"name": p.name, "browser_download_url": f"http://127.0.0.1:{PORT}/assets/{p.name}"})
    return out

class H(BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path == "/releases/latest":
            body = json.dumps({"tag_name": TAG, "body": "假源测试", "published_at": "2026-09-19T00:00:00Z", "assets": assets()}).encode()
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return
        if self.path.startswith("/assets/"):
            f = ROOT / self.path.split("/", 2)[2]
            if not f.is_file():
                self.send_error(404)
                return
            data = f.read_bytes()
            self.send_response(200)
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)
            return
        self.send_error(404)

HTTPServer(("127.0.0.1", PORT), H).serve_forever()
```

- [ ] **Step 2: 现场构建一个「更高版本」的产物 + checksums**

```bash
rm -rf /tmp/upd-src && git clone --depth 1 . /tmp/upd-src && cd /tmp/upd-src
# 用当前工作树构建：版本号抬高到 v9.9.9（只要 > 当前版本即可）
# ① 先构建「升级前」的旧二进制：必须是**干净 tag** 版本的 release 构建，
#    否则 Task 9 的门卫（!IsRelease → disabled）或版本比较（Compare<=0 → up-to-date）会让自动检查不产生 available。
make linux COMPRESS=0 VERSION=v0.6.0 2>/dev/null || wails build -platform linux/amd64 -skipbindings -ldflags "-X main.Version=v0.6.0" -trimpath
cp build/bin/sshore /tmp/sshore-old-v0.6.0
# ② 再构建「新版本」产物（版本号只要大于 v0.6.0 即可）
make linux COMPRESS=0 VERSION=v9.9.9 2>/dev/null || wails build -platform linux/amd64 -skipbindings -ldflags "-X main.Version=v9.9.9" -trimpath
mkdir -p /tmp/upd-www && tar -czf /tmp/upd-www/sshore-v9.9.9-linux-amd64.tar.gz -C build/bin sshore
cd /tmp/upd-www && sha256sum sshore-v9.9.9-linux-amd64.tar.gz | awk '{print $1"  "$2}' > checksums.txt
# ③ 启动假源（注意：假源脚本在 /tmp/upd-src 里，且 git clone 只含**已提交**内容 —— 若脚本还没入库，用仓库工作区的绝对路径）
python3 /home/lan/workspace/scripts/sshkit/e2e/fake_update_source.py 8799 /tmp/upd-www v9.9.9 &
```

Expected: 假源在 `http://127.0.0.1:8799` 提供 JSON、资产与校验文件。

- [ ] **Step 3: Linux 端到端（自动化到 ready；升级那一步允许人工点击并如实记录）**

1. 用**独立目录**放一份「升级前」的二进制：`mkdir -p /tmp/upd-live && cp /tmp/sshore-old-v0.6.0 /tmp/upd-live/sshore`（**必须**是 Step 2 ① 里那个 `VERSION=v0.6.0` 的 Clean 版本构建）；
2. 把配置写进 `config.DefaultConfigPath()` 指向的文件（Linux `~/.config/sshore/sshore.toml`、Windows `%AppData%\\sshore\\sshore.toml`；**先把它备份成 `sshore.toml.bak`，验收结束原样还原**）：`update_source = "http://127.0.0.1:8799"`、`update_check_auto = true`、`update_check_interval_hours = 0`（关轮询，只留首查）；
3. 启动应用（Xvfb :9 或当前桌面），等 5s 首查，断言界面出现 `available` 与角标；
4. 点「下载更新」（合成点击或人工），等状态 `ready`，检查磁盘：`sshore.v9.9.9` 与 `sshore.v9.9.9.sha256` 存在；
5. 点「重启并升级」；脚本会替换二进制并启动新版；**断言**：`sshore` 内容 = 新构建（比对 sha256）、`sshore.<旧版本>` 备份存在、脚本与日志消失、窗口标题显示 `SSHore v9.9.9`（app.go:56 的 appTitle 是 `"SSHore " + Version`，注意大小写）。

- [ ] **Step 4: Windows VM 端到端 + Defender 证据**

1. 重复 Step 3 的 1–5（平台为 windows/amd64，产物为 `.zip`）；
2. 采集 Defender 证据：

```powershell
Get-MpPreference | Select-Object DisableRealtimeMonitoring, MAPSReporting, SubmitSamplesConsent
Get-MpThreatDetection | Format-List
Get-WinEvent -LogName "Microsoft-Windows-Windows Defender/Operational" -MaxEvents 50 | Where-Object { $_.Id -in 1006,1007,1116,1117 }
& "$env:ProgramFiles\Windows Defender\MpCmdRun.exe" -Scan -ScanType 3 -File <待安装文件>
```

3. 把两类证据（无拦截 / 有拦截）写进 `.superpowers/update-2026-09-19/acceptance.md`；**若被拦截 → 按 spec §10.4 切 helper 模式或转人工替换，并在结论里写明**。

- [ ] **Step 5: 跑 spec §13.5 的 10 个变体**

逐条执行并记录：跳过版本、缺 `checksums.txt`、无平台资产、哈希改一位、`ExeDir` 只读、pending/备份判别、脚本失败、sidecar 篡改、两实例并发 apply、成功后再启动。

- [ ] **Step 6: 变异校验（6 个变异，每个都必须让至少一条测试失败）**

```bash
# ① 判 pending 不比较版本序：把 plan.go 的 Compare(ver, current) > 0 改成 >= 0
#    期望：TestIsPendingNameUsesVersionOrder 失败
go test ./internal/update/ -run TestIsPendingName -count=1

# ② 清理时按模式删除（连 pending 一起删）：把脚本第 4 步的 PB 比较注释掉
#    期望：TestScriptCleanupKeepsPending 失败
go test ./internal/update/ -run TestScriptCleanup -count=1

# ③ 去掉 sidecar 重算：删掉 ApplyAndRestart 里的 VerifyFile 调用
#    期望：TestApplyRejectsTamperedPendingSidecar 失败
go test ./internal/update/ -run "TestApplyRejectsTamperedPendingSidecar" -count=1

# ④ 去掉 Check 在 applying 的守卫：把 StateApplying 从拒绝分支移除
#    期望：TestCheckRejectedWhileDownloadingOrReady 的 applying 断言失败
go test ./internal/update/ -run TestCheckRejected -count=1

# ⑤ 脚本第 8 步去掉存活探测：删掉 if ! kill -0 分支
go test ./internal/update/ -run TestScript -count=1

# ⑥ 去掉角标的 ready 条件：把 stores/update.js 的 ["available","ready"] 改成 ["available"]
cd frontend && npx vitest run src/stores/update.test.js
```

每一条都记录「改了什么 → 哪条测试失败 → 恢复」；把结果写进 `acceptance.md`。

- [ ] **Step 7: 跑完整闸门并提交验收证据**

```bash
make ci && bash e2e/test_local.sh
git add e2e/fake_update_source.py
git commit -m "test(e2e): 更新功能假源脚本与端到端验收夹具"
```

Expected: `make ci` 全绿；既有 `e2e/test_local.sh` 不回归。

---

## Self-Review（写完后的自查结论）

**1. spec 覆盖**（逐条映射到任务）：

| spec 章节 | 任务 |
|---|---|
| §4 F1/F5/F6/F7/F8 复核 | Task 0（F3）、Task 2（F6 归档形状）、Task 3（F1）、Task 4（F7 常量）、Task 11（F8 版本类别） |
| §5.1 各文件 | Task 1–9 一一对应（version/checksum/extract/source/asset/download/plan/scripts/script/lock/service） |
| §5.2 Service 契约 | Task 8（状态机）+ Task 9（自检/调度/下载） |
| §5.3 绑定与接线 | Task 11 |
| §5.4 前端文件 + wailsjs 重生成 | Task 11（绑定）、13–16（前端） |
| §6 配置（含双写修复） | Task 10（字段与 Service 侧读写）、Task 15（前端重读） |
| §7 状态机与守卫 | Task 8（守卫）、Task 9（自检判别） |
| §8 脚本契约 | Task 6（脚本与参数）、Task 12（行为验证） |
| §9 错误与边界 | Task 8/9（状态收敛）、Task 12（失败路径）、Task 13（文案） |
| §10 信任边界/AV/预案 | Task 3（SameOrigin）、Task 9（sidecar）、Task 19（Defender 证据与预案判定） |
| §11 CI | Task 17 |
| §12 UI | Task 13–16 |
| §13 测试策略 | Task 1–9（单测）、12（脚本真跑）、13–16（前端）、19（端到端与变体） |
| §14 DoD | Task 19（含变异校验 6 条） |
| §15 风险 | 由对应任务的实现与 Task 19 的证据覆盖 |
| §16 决策记录 | 已在 spec 内固化，本计划不重复 |

**2. 占位符扫描**：全文无 TBD/TODO；每个代码步骤都给了可直接落盘的内容；命令都带期望输出。

**3. 类型与命名一致性**：`Plan`/`UpdateInfo`/`Settings`/`Options` 的字段在 Task 5、8、9、11、13、14 之间一致；`ScriptArgs`/`ScriptEnv`/`ScriptName`/`ScriptBytes`/`StartDetached` 在 Task 6 定义、Task 11 使用；`StateXxx` 常量只在 Task 8 定义；前端 `actions()` 的键名（check/download/cancel/apply/discard/skip/clearSkip/openPage）在 Task 13、15、16 一致。


---

## 两阶段审核记录（对**计划本身**）

第一阶段（本地自审，已提交 `486aad3`）修正 13 处；第二阶段委托两个**全新上下文、只读**的 subagent 独立评审：
A 负责 Task 0–12（Go 后端 + 脚本 + CI 里的 Windows 脚本），B 负责 Task 13–19（前端 + CI + 端到端/DoD）。
两份结论共 **17 条 Blocking、20 条 Important、21 条 Minor**，逐条在本地复核后处置如下。

### 已修（按主题）

| 主题 | 具体问题 |
|---|---|
| 不能编译 | `pendingRe` 定义丢失；`StartDetached` 在非 build-tag 文件里用了 Windows 专属 `SysProcAttr` 字段（Linux 编译失败）；`lock_windows.go` 的 `"Global" + "\"` 非法字面量；`download_test.go` 缺 `}))` |
| 测试与实现互相矛盾 | `checksum_test.go` 用 6 位哈希 vs 实现要求 64 位；`extract_test.go` 期望拒绝 `../` 但 `path.Base` 把它洗白；`plan_test.go` 期望 describe 备份名而实现走了时间戳；`service_test.go` 期望 `v0.7.0` 而实现存 `0.7.0`；`store_test.go` 对完整结构体做 `==`（被既有 Theme/FontScale 默认值打破）；脚本夹具用不可执行的 `NEW` 导致成功路径必然回滚且日志已被删除 |
| 端到端必然失败 | checksums.txt 写成 `./<artifact 名>/<文件>`（应用按 basename 查表 → 永远 verify-failed）；假源脚本用相对路径调用；旧二进制不是 Clean release 版本（门卫/版本比较会让 `available` 永不出现） |
| DoD 抓不住变异 | 变异 ③（sidecar 重算）与 ④（applying 守卫）没有对应测试 → 补 `TestApplyRejectsTamperedPendingSidecar`、给 Check 拒绝用例加 applying；`-run` 模式写错（`TestSelfCheck` 不匹配） |
| Windows CI 与 DoD 不一致 | 原方案只断言回滚且用「文本文件当 exe」（flaky）；改为**真实 PE**：成功路径用长期存活的 `cmd.exe`（探测通过后 `taskkill`），回滚路径用立刻退出的 `whoami.exe` |
| 并发/健壮性 | `download` 持锁调用 `failIO/failVerify` 会自死锁；空闲超时与用户取消不可区分（改用 `atomic.Bool` 看门狗标志）；`loop` 无锁读 `stopCh`（`-race` 会报）；Task 11 重复导入 `os`/`time`；cmd 缺 PID/SIZE/WAIT 正整数校验且有一行无效赋值；sh 的 `args` 失败按 spec 应退 2 |
| spec ↔ 计划不一致 | spec §5.1 的 `Download`/`PlanFor`/`script.go` 签名、§11.4 的 awk 目录剥除同步到与计划一致；`models.ts` 路径更正为 `frontend/wailsjs/go/models.ts` |
| 缺失能力 | 更新服务没有诊断日志通道（X-RateLimit、门卫原因、自动检查失败）→ `Options.Log` + `App.logUpdate` 接到日志面板；检查请求补 10s 总超时；设置页补「恢复默认」与 notes 2000 字符截断；验收报告改为**入库**到 `docs/superpowers/reports/` |

### 审核之外：我实际跑过的东西（证据）

- **脚本真跑**：把 Task 6 的 `update.sh` 抽出来在临时目录执行 → 抓到「`$0` 为相对路径 + `cd` 后 `rm -f "$0"` 静默失败、升级成功后残留脚本」这个真 bug（已修并用 `$SELF` + 相对路径调用测试锁住）。
- **正则**：`pendingRe` 对 9 个真实文件名逐一验证（含 `sshore.exe`、`*.sha256`、describe 串）。
- **Go 编译**：把 Task 1 的 `version.go` 从计划里抽出来放进临时模块，`gofmt`/`go vet`/`go test` 全绿（覆盖计划里那 3 个测试与 9 个 `ParsePendingName` 用例）。

### 尚未做（诚实记录）

- 计划里其余 Go 代码块**没有整体编译过**（只有 Task 1 的片段做了真实编译）；编译闸门是计划自身的 Task 1–11 各 Step 4/5，执行时逐任务触发。
- Task 12 的脚本真跑测试在本机没有跑过（计划里的脚本片段已跑过，Go 测试代码未跑）。
- 真机（Windows VM / Defender、Linux GUI 端到端）属 Task 19，尚未执行。

**4. 已知的刻意简化**（不是遗漏）：

- Task 8 的 `download` 允许先留骨架，Task 9 用测试把它变成完整实现（两步之间不发布）。
- Task 17 的 Windows CI 只验证到「替换完成」，存活探测/回滚在 Task 12（Linux 可自动化）与 Task 19（Windows 真机）覆盖。
- Task 19 的端到端在 GUI 点击这一步允许人工，并如实记录（spec §13.4 的已知限制）。











