# SFTP P2：导航能力（过滤 · 深搜 · 位置）实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 给 SFTP 双栏加「当前目录即时过滤 + 双侧递归深搜 + 每面板书签/最近位置」，并移除 header 上那个全局「最近位置」下拉。

**Architecture:** 匹配语义抽成 stdlib-only 的小包 `internal/namepattern` 供远端与本地共用；远端深搜在 `ListMany` 上做 BFS（批间检查 ctx、限深限条、可取消），本地深搜用 `filepath.WalkDir`；取消**不使用 error 返回**（Wails 绑定在 err != nil 时会丢掉第一个返回值），而是用 `Cancelled` 字段 + 正常 resolve。位置数据只有一条写入路径（新增的位置绑定），不再走 `GetSettings/SetSettings` 整对象覆盖。

**Tech Stack:** Go 1.26 + Wails v2.15.0 + Vue 3 + Pinia + vitest。

**Spec:** `docs/superpowers/specs/2026-09-16-sftp-selection-dnd-navigation-design.md`（本计划实现其 P2）

**前置:** 先完成 `2026-09-16-sftp-p1-selection-batch-drag.md`。P2 依赖 P1 定型的 `FilePane` 组件契约（`selKeys/anchor/@visible/@focus`）与 `SftpView` 的键盘/拖拽接线。

> **与 spec 的一处细化**：spec §3 决策 17 写「匹配器在 `internal/sftp` 内自实现」。本计划细化为**独立小包 `internal/namepattern`**，理由：远端 `sftp.Search` 与本地 `localfs.Search` 必须共用同一匹配语义，两处各写一份必然漂移。该包只依赖标准库，不引入依赖方向问题（`internal/sftp → internal/namepattern`、`internal/localfs → internal/namepattern`，两者都不依赖 `internal/watch`）。

## Global Constraints

- **深度语义与仓库一致**：`0`=仅本层、`N`=N 层、`-1`=无限（`config/store.go:73`、`watch/scan.go:135`）；**绑定层**把 `0` 当「未设置」归一为 `5`，UI 不提供无限入口。
- **`limit` 是命中条数上限**（`<=0` 视为 500）；`Scanned` 只用于进度，不受 `limit` 约束。
- **取消不是错误**：`Cancelled=true` + 部分结果 + `nil` error。任何「非 nil error」都意味着前端拿不到结果（Wails dispatcher 语义）。
- **`Unreadable` 必须如实计数**：沿用 `ListMany` 的「缺失 key = 未知，绝不当作空目录」不变量。
- **位置数据单写者**：只经 `AddBookmark/RemoveBookmark/AddLocalRecent/AddRemoteRecent` 写入；前端不通过 `SetSettings` 写位置。
- **迁移必须显式标记** `legacy_migrated`，不得用「新字段为空」判定（否则用户清空后旧数据复活）。
- 新结构体**带 json tag**；绑定类型**带包名限定**；绑定变更后执行 `wails generate module -tags webkit2_41`。
- 事件订阅**成对退订**（`App.vue:22-24` 的教训）；新事件用 `runtime.EventsEmit(a.ctx, ...)` 直发，**不经** `Init(emit)` 通道。
- 提交信息用简体中文。

---

### Task 1: `internal/namepattern` 匹配器

**Files:**
- Create: `internal/namepattern/match.go`
- Test: `internal/namepattern/match_test.go`

**Interfaces:**
- Produces: `namepattern.Match(pattern, rel, base string) bool`（Task 2/3 共用）

- [ ] **Step 1: 写失败测试**

~~~go
package namepattern

import "testing"

func TestMatchSubstringCaseInsensitive(t *testing.T) {
	if !Match("APP", "logs/app.log", "app.log") {
		t.Fatal("substring match should be case-insensitive")
	}
	if Match("nope", "logs/app.log", "app.log") {
		t.Fatal("must not match unrelated name")
	}
	if !Match("", "anything", "anything") {
		t.Fatal("empty pattern matches everything")
	}
}

func TestMatchGlob(t *testing.T) {
	if !Match("*.log", "logs/a.log", "a.log") {
		t.Fatal("glob on basename should match")
	}
	if !Match("logs/*", "logs/a.txt", "a.txt") {
		t.Fatal("glob on relative path should match")
	}
	if Match("*.log", "logs/a.txt", "a.txt") {
		t.Fatal("glob should not match txt")
	}
}

func TestMatchBadGlobFallsBackToSubstring(t *testing.T) {
	// "[abc" 是 ErrBadPattern，必须回退子串而不是 panic 或全不匹配。
	if !Match("[abc", "x/[abc", "[abc") {
		t.Fatal("bad glob should fall back to substring")
	}
}
~~~

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/namepattern/ -v`
Expected: FAIL —— `no Go files`

- [ ] **Step 3: 最小实现**

~~~go
// Package namepattern 提供 SFTP 深搜使用的名称匹配语义，远端与本地共用一份实现。
// 只依赖标准库。
package namepattern

import (
	"errors"
	"path"
	"strings"
)

// Match 判定单个条目是否命中：
//   - pattern 为空 → 命中一切；
//   - pattern 不含 glob 元字符（* ? [）→ 不区分大小写的子串匹配（basename 与相对路径都试）；
//   - pattern 含 glob 元字符 → 双方先转小写再 path.Match，保证"不区分大小写"在两种模式下一致；
//   - 非法 glob（ErrBadPattern）→ 回退为子串匹配，不静默丢弃。
func Match(pattern, rel, base string) bool {
	if pattern == "" {
		return true
	}
	p := strings.ToLower(pattern)
	if !strings.ContainsAny(pattern, "*?[") {
		return strings.Contains(strings.ToLower(base), p) || strings.Contains(strings.ToLower(rel), p)
	}
	baseL, relL := strings.ToLower(base), strings.ToLower(rel)
	okBase, errBase := path.Match(p, baseL)
	if errBase == nil && okBase {
		return true
	}
	okRel, errRel := path.Match(p, relL)
	if errRel == nil && okRel {
		return true
	}
	// 非法 glob（如未闭合的 [abc）→ 回退为子串匹配，绝不静默丢弃（spec §5.1）。
	if errors.Is(errBase, path.ErrBadPattern) || errors.Is(errRel, path.ErrBadPattern) {
		return strings.Contains(baseL, p) || strings.Contains(relL, p)
	}
	return false
}
~~~

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/namepattern/ -count=1`
Expected: PASS（3 个用例）

- [ ] **Step 5: Commit**

~~~bash
git add internal/namepattern
git commit -m "feat(namepattern): 深搜名称匹配语义（子串/glob/非法 glob 回退）与单测"
~~~

---

### Task 2: `internal/sftp.Search`（BFS over ListMany）

**Files:**
- Create: `internal/sftp/search.go`
- Test: `internal/sftp/search_test.go`

**Interfaces:**
- Consumes: `namepattern.Match`、`c.ListMany(host, user, paths)`、Task 8(P1) 扩展后的 `fakeRunner`（支持 `push(osutil.Outcome)`）
- Produces:
  - `type SearchHit struct{ Path string; IsDir bool; Size int64; ModTime string }`（json: path/isDir/size/modTime）
  - `type SearchOutcome struct{ Hits []SearchHit; Scanned, Unreadable int; Truncated, Cancelled bool }`（json: hits/scanned/unreadable/truncated/cancelled）
  - `func (c *Ctrl) Search(ctx context.Context, host, user, root, pattern string, maxDepth, limit int, onProgress func(scanned int)) (SearchOutcome, error)`

- [ ] **Step 1: 写失败测试**

~~~go
package sftp

import (
	"context"
	"fmt"
	"testing"

	"sshore/internal/osutil"
)

// 复用 P1 Task 8 已放进 internal/sftp/ctrl_test.go 的 mockLs / lsLine（同属 package sftp）。
// **不要在本文件重复定义**：同名函数会让 go test ./internal/sftp/ 直接报 redeclared。

func TestSearchDepthZeroOnlyListsRoot(t *testing.T) {
	fr := &fakeRunner{}
	c := NewCtrl(fr.run, nil)
	fr.push(osutil.Outcome{ExitCode: 0, Stdout: mockLs("/", lsLine("app.log", false, 10), lsLine("sub", true, 0))})
	out, err := c.Search(context.Background(), "h", "", "/", "*.log", 0, 100, nil)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if out.Scanned != 1 || len(out.Hits) != 1 || out.Hits[0].Path != "app.log" {
		t.Fatalf("maxDepth=0 只应列根层: %+v", out)
	}
}

func TestSearchDepthOneRecursesOnce(t *testing.T) {
	fr := &fakeRunner{}
	c := NewCtrl(fr.run, nil)
	fr.push(osutil.Outcome{ExitCode: 0, Stdout: mockLs("/", lsLine("app.log", false, 10), lsLine("sub", true, 0))})
	fr.push(osutil.Outcome{ExitCode: 0, Stdout: mockLs("/sub", lsLine("deep.log", false, 20))})
	out, err := c.Search(context.Background(), "h", "", "/", "*.log", 1, 100, nil)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if out.Scanned != 2 || len(out.Hits) != 2 {
		t.Fatalf("maxDepth=1 应列 / 与 /sub: %+v", out)
	}
	if out.Hits[1].Path != "sub/deep.log" {
		t.Fatalf("相对路径错误: %+v", out.Hits)
	}
}

func TestSearchTruncatesAtLimit(t *testing.T) {
	fr := &fakeRunner{}
	c := NewCtrl(fr.run, nil)
	fr.push(osutil.Outcome{ExitCode: 0, Stdout: mockLs("/", lsLine("a.log", false, 1), lsLine("b.log", false, 2))})
	out, err := c.Search(context.Background(), "h", "", "/", "*.log", 0, 1, nil)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if !out.Truncated || len(out.Hits) != 1 {
		t.Fatalf("应截断为 1 条: %+v", out)
	}
}

func TestSearchMarksUnreadableWhenBlockMissing(t *testing.T) {
	fr := &fakeRunner{}
	c := NewCtrl(fr.run, nil)
	// stdout 为空 ⇒ 没有任何回显块 ⇒ 根路径'未知'，必须计 Unreadable，绝不当作空目录。
	fr.push(osutil.Outcome{ExitCode: 0, Stdout: ""})
	out, err := c.Search(context.Background(), "h", "", "/gone", "", 0, 100, nil)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if out.Unreadable != 1 || out.Scanned != 0 {
		t.Fatalf("缺块必须计为不可读: %+v", out)
	}
}

func TestSearchCancelledReturnsPartial(t *testing.T) {
	fr := &fakeRunner{}
	c := NewCtrl(fr.run, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out, err := c.Search(ctx, "h", "", "/", "", 5, 100, nil)
	if err == nil {
		t.Fatal("expected ctx error")
	}
	if !out.Cancelled {
		t.Fatalf("Cancelled must be true: %+v", out)
	}
}
~~~

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/sftp/ -run TestSearch -v`
Expected: FAIL —— `c.Search undefined`

- [ ] **Step 3: 最小实现**

~~~go
package sftp

import (
	"context"
	"path"
	"strings"

	"sshore/internal/namepattern"
)

const searchBatch = 64

// SearchHit 是一次深搜的命中项；Path 为相对 root 的路径。
type SearchHit struct {
	Path    string `json:"path"`
	IsDir   bool   `json:"isDir"`
	Size    int64  `json:"size"`
	ModTime string `json:"modTime"`
}

// SearchOutcome 是一次深搜的结果快照。
// 绑定约束：调用方不得在返回非 nil error 时期待前端拿到结果——Wails 的 dispatcher
// 在 err != nil 时不写 Result，所以"取消"必须用 Cancelled 字段 + nil error 表达。
type SearchOutcome struct {
	Hits       []SearchHit `json:"hits"`
	Scanned    int         `json:"scanned"`
	Unreadable int         `json:"unreadable"`
	Truncated  bool        `json:"truncated"`
	Cancelled  bool        `json:"cancelled"`
}

// Search 在 root 下 BFS 深搜；pattern 为空返回全部条目。
// 深度语义与仓库一致：0=仅本层，N=N 层，-1=无限（spec §3 决策 17）。
// ctx 在**批次之间**检查；取消时返回已扫描部分 + ctx.Err()，调用方应转成
// Cancelled=true 的正常返回（见 app.go 的 SftpSearch）。
func (c *Ctrl) Search(ctx context.Context, host, user, root, pattern string, maxDepth, limit int,
	onProgress func(scanned int)) (SearchOutcome, error) {
	if limit <= 0 {
		limit = 500
	}
	root = strings.TrimRight(root, "/")
	if root == "" {
		root = "/"
	}
	out := SearchOutcome{Hits: []SearchHit{}}
	type node struct {
		dir   string
		depth int
	}
	queue := []node{{dir: root}}
	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			out.Cancelled = true
			return out, err
		}
		batch := queue
		if len(batch) > searchBatch {
			batch = queue[:searchBatch]
		}
		queue = queue[len(batch):]

		paths := make([]string, 0, len(batch))
		for _, n := range batch {
			paths = append(paths, n.dir)
		}
		res, err := c.ListMany(host, user, paths)
		if err != nil {
			return out, err
		}
		for _, n := range batch {
			items, ok := res[n.dir]
			if !ok {
				out.Unreadable++
				continue
			}
			out.Scanned++
			if onProgress != nil {
				onProgress(out.Scanned)
			}
			for _, it := range items {
				rel := it.Name
				if n.depth > 0 {
					rel = strings.TrimPrefix(strings.TrimPrefix(n.dir, root), "/") + "/" + it.Name
				}
				if namepattern.Match(pattern, rel, it.Name) {
					out.Hits = append(out.Hits, SearchHit{Path: rel, IsDir: it.IsDir, Size: it.Size, ModTime: it.ModTime})
					if len(out.Hits) >= limit {
						out.Truncated = true
						return out, nil
					}
				}
				if it.IsDir && (maxDepth < 0 || n.depth < maxDepth) {
					queue = append(queue, node{dir: path.Join(n.dir, it.Name), depth: n.depth + 1})
				}
			}
		}
	}
	return out, nil
}
~~~

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/sftp/ -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

~~~bash
git add internal/sftp/search.go internal/sftp/search_test.go
git commit -m "feat(sftp): 远端递归深搜（BFS/限深限条/可取消/不可读计数）与单测"
~~~

---

### Task 3: `internal/localfs.Search`

**Files:**
- Create: `internal/localfs/search.go`
- Test: `internal/localfs/search_test.go`

**Interfaces:**
- Consumes: `namepattern.Match`
- Produces: `type SearchOpts struct{ MaxDepth, Limit int }`；`func Search(ctx context.Context, root, pattern string, o SearchOpts) (hits []Hit, skipped int, truncated bool, err error)`；`type Hit struct{ Path string; IsDir bool; Size int64; ModTime string }`（json: path/isDir/size/modTime）

- [ ] **Step 1: 写失败测试**

~~~go
package localfs

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestSearchFindsByNameAndDepth(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "sub"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "a.log"), []byte("1"), 0o644)
	_ = os.WriteFile(filepath.Join(root, "sub", "b.log"), []byte("22"), 0o644)
	hits, _, _, err := Search(context.Background(), root, "*.log", SearchOpts{MaxDepth: 0, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Path != "a.log" {
		t.Fatalf("depth 0 hits = %+v", hits)
	}
	hits, _, _, err = Search(context.Background(), root, "*.log", SearchOpts{MaxDepth: 1, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 {
		t.Fatalf("depth 1 hits = %+v", hits)
	}
}

func TestSearchTruncatesAndCancels(t *testing.T) {
	root := t.TempDir()
	for _, n := range []string{"a.log", "b.log"} {
		_ = os.WriteFile(filepath.Join(root, n), []byte("x"), 0o644)
	}
	hits, _, truncated, err := Search(context.Background(), root, "*.log", SearchOpts{MaxDepth: 0, Limit: 1})
	if err != nil || !truncated || len(hits) != 1 {
		t.Fatalf("truncate: hits=%v truncated=%v err=%v", hits, truncated, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, _, err = Search(ctx, root, "", SearchOpts{MaxDepth: 0, Limit: 10})
	if err == nil {
		t.Fatal("expected ctx error")
	}
}

func TestSearchSkipsSymlinkTargets(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	_ = os.WriteFile(filepath.Join(outside, "outside.log"), []byte("x"), 0o644)
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skip("symlink not supported")
	}
	hits, _, _, err := Search(context.Background(), root, "*.log", SearchOpts{MaxDepth: 5, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hits {
		if h.Path == "link/outside.log" {
			t.Fatalf("must not follow symlinked dirs: %+v", hits)
		}
	}
}
~~~

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/localfs/ -run TestSearch -v`
Expected: FAIL —— `undefined: Search`

- [ ] **Step 3: 最小实现**

~~~go
package localfs

import (
	"context"
	"io/fs"
	"path/filepath"
	"strings"

	"sshore/internal/namepattern"
)

// Hit 是一次本地深搜的命中项；Path 为相对 root 的路径。
type Hit struct {
	Path    string `json:"path"`
	IsDir   bool   `json:"isDir"`
	Size    int64  `json:"size"`
	ModTime string `json:"modTime"`
}

// SearchOpts 的深度语义与 internal/config、internal/watch 一致。
type SearchOpts struct {
	MaxDepth int
	Limit    int
}

// Search 用 filepath.WalkDir 搜索 root：不跟随符号链接，不可读目录跳过并计数。
func Search(ctx context.Context, root, pattern string, o SearchOpts) ([]Hit, int, bool, error) {
	if o.Limit <= 0 {
		o.Limit = 500
	}
	root = filepath.Clean(root)
	hits := []Hit{}
	skipped := 0
	truncated := false
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			skipped++
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if p == root {
			return nil
		}
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		depth := strings.Count(rel, "/")
		// add 统一"先判上限再放行"，避免目录分支与文件分支的截断口径不一致
		// （目录分支原来会把命中推到 limit+1）。返回 false ⇒ 已达上限，调用方 SkipAll。
		add := func(h Hit) bool {
			if len(hits) >= o.Limit {
				truncated = true
				return false
			}
			hits = append(hits, h)
			if len(hits) >= o.Limit {
				truncated = true
				return false
			}
			return true
		}
		if d.IsDir() {
			atMaxDepth := o.MaxDepth >= 0 && depth >= o.MaxDepth
			if namepattern.Match(pattern, rel, d.Name()) {
				if !add(Hit{Path: rel, IsDir: true}) {
					return fs.SkipAll
				}
			}
			if atMaxDepth {
				return fs.SkipDir
			}
			return nil
		}
		info, infoErr := d.Info()
		hit := Hit{Path: rel, IsDir: false}
		if infoErr == nil {
			hit.Size = info.Size()
			hit.ModTime = info.ModTime().Format("2006-01-02 15:04")
		}
		if namepattern.Match(pattern, rel, d.Name()) {
			if !add(hit) {
				return fs.SkipAll
			}
		}
		return nil
	})
	if err != nil && ctx.Err() != nil {
		return hits, skipped, truncated, err
	}
	return hits, skipped, truncated, err
}
~~~

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/localfs/ -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

~~~bash
git add internal/localfs/search.go internal/localfs/search_test.go
git commit -m "feat(localfs): 本地递归深搜（WalkDir/限深限条/可取消/不跟随符号链接）"
~~~

---

### Task 4: 配置：书签、双侧最近位置与显式迁移

**Files:**
- Modify: `internal/config/store.go`（`AppConfig` 增字段；新增 `MigrateLegacyRecents`；`LoadConfig` 调用迁移）
- Test: `internal/config/store_test.go`

**Interfaces:**
- Produces:
  - `type Bookmark struct{ Name, Scope, Host, Path string }`（toml/json 同名小写）
  - `type RecentLocal struct{ Path, TS string }`；`type RecentRemote struct{ Host, Path, TS string }`
  - `AppConfig.Bookmarks []Bookmark`、`.LocalRecent []RecentLocal`、`.RemoteRecent []RecentRemote`、`.LegacyMigrated bool`
  - `func MigrateLegacyRecents(cfg *AppConfig) bool`（返回是否发生迁移）
  - `const maxRecent = 20`

- [ ] **Step 1: 写失败测试**

~~~go
func TestMigrateLegacyRecentsSplitsAndDedupes(t *testing.T) {
	cfg := &AppConfig{
		RecentSFTP: []RecentSFTP{
			{Host: "prod", RemoteDir: "/var/log", LocalDir: "/tmp/a", TS: "2026-09-01T00:00:00Z"},
			{Host: "prod", RemoteDir: "/var/log", LocalDir: "/tmp/b", TS: "2026-09-02T00:00:00Z"},
			{Host: "db", RemoteDir: "/srv", LocalDir: "/tmp/b", TS: "2026-09-03T00:00:00Z"},
		},
	}
	if !MigrateLegacyRecents(cfg) {
		t.Fatal("expected migration to happen")
	}
	if !cfg.LegacyMigrated {
		t.Fatal("migration must set the explicit flag")
	}
	if len(cfg.RemoteRecent) != 2 {
		t.Fatalf("remote recents = %+v", cfg.RemoteRecent)
	}
	if cfg.RemoteRecent[0].Host != "db" {
		t.Fatalf("must be sorted by ts desc: %+v", cfg.RemoteRecent)
	}
	if len(cfg.LocalRecent) != 2 {
		t.Fatalf("local recents = %+v", cfg.LocalRecent)
	}
}

func TestMigrateIsIdempotentAndDoesNotResurrect(t *testing.T) {
	cfg := &AppConfig{RecentSFTP: []RecentSFTP{{Host: "prod", RemoteDir: "/x", LocalDir: "/y", TS: "t"}}}
	MigrateLegacyRecents(cfg)
	cfg.RemoteRecent = nil
	cfg.LocalRecent = nil
	if MigrateLegacyRecents(cfg) {
		t.Fatal("second run must be a no-op (flag already set)")
	}
	if len(cfg.RemoteRecent) != 0 {
		t.Fatalf("cleared recents must not be resurrected: %+v", cfg.RemoteRecent)
	}
}

func TestMigrateRoundTripsThroughDisk(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sshore.toml")
	cfg := &AppConfig{RecentSFTP: []RecentSFTP{{Host: "prod", RemoteDir: "/x", LocalDir: "/y", TS: "t"}}}
	MigrateLegacyRecents(cfg)
	if err := SaveConfig(p, cfg); err != nil {
		t.Fatal(err)
	}
	back, err := LoadConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	if !back.LegacyMigrated || len(back.RemoteRecent) != 1 {
		t.Fatalf("round trip lost data: %+v", back)
	}
}
~~~

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/config/ -run TestMigrate -v`
Expected: FAIL —— `undefined: MigrateLegacyRecents`

- [ ] **Step 3: 实现**

在 `AppConfig` 增加字段：

~~~go
	Bookmarks      []Bookmark     `toml:"bookmarks" json:"bookmarks"`
	LocalRecent    []RecentLocal  `toml:"local_recent" json:"local_recent"`
	RemoteRecent   []RecentRemote `toml:"remote_recent" json:"remote_recent"`
	LegacyMigrated bool           `toml:"legacy_migrated" json:"legacy_migrated"`
~~~

新增类型与迁移函数：

~~~go
// Bookmark 是一个手动固定的位置；scope=remote 时 host 有意义。
type Bookmark struct {
	Name  string `toml:"name" json:"name"`
	Scope string `toml:"scope" json:"scope"`
	Host  string `toml:"host" json:"host"`
	Path  string `toml:"path" json:"path"`
}

type RecentLocal struct {
	Path string `toml:"path" json:"path"`
	TS   string `toml:"ts" json:"ts"`
}

type RecentRemote struct {
	Host string `toml:"host" json:"host"`
	Path string `toml:"path" json:"path"`
	TS   string `toml:"ts" json:"ts"`
}

const maxRecent = 20

// MigrateLegacyRecents 把旧 recent_sftp（{host, remote_dir, local_dir, ts}）拆成
// 双侧最近位置。用显式标记 LegacyMigrated 判定，**不用**「新字段为空」——否则用户
// 清空书签/最近后旧数据会被复活。幂等；迁移后由调用方 saveConfig 落盘。
func MigrateLegacyRecents(cfg *AppConfig) bool {
	if cfg == nil || cfg.LegacyMigrated {
		return false
	}
	cfg.LegacyMigrated = true
	if len(cfg.RecentSFTP) == 0 {
		return true
	}
	sort.SliceStable(cfg.RecentSFTP, func(i, j int) bool { return cfg.RecentSFTP[i].TS > cfg.RecentSFTP[j].TS })
	seenR := map[string]bool{}
	seenL := map[string]bool{}
	for _, r := range cfg.RecentSFTP {
		if r.Host != "" && r.RemoteDir != "" {
			k := r.Host + "\x00" + r.RemoteDir
			if !seenR[k] && len(cfg.RemoteRecent) < maxRecent {
				seenR[k] = true
				cfg.RemoteRecent = append(cfg.RemoteRecent, RecentRemote{Host: r.Host, Path: r.RemoteDir, TS: r.TS})
			}
		}
		if r.LocalDir != "" {
			if !seenL[r.LocalDir] && len(cfg.LocalRecent) < maxRecent {
				seenL[r.LocalDir] = true
				cfg.LocalRecent = append(cfg.LocalRecent, RecentLocal{Path: r.LocalDir, TS: r.TS})
			}
		}
	}
	// 迁移完成即清空旧字段：否则 SaveConfig 的全量编码会继续把 recent_sftp 写回磁盘
	// （spec §10.2「旧字段只读兼容、不再写回」）。
	cfg.RecentSFTP = nil
	return true
}
~~~

**`LoadConfig` 保持只读**（读函数不写盘，且文件不存在时它提前 return、迁移根本不会被触发）。迁移落盘放在 `app.startup` 里、`a.cfg` 赋值之后：

~~~go
	// startup：加载后迁移一次并显式落盘（internal/config 里没有 dirty 机制）。
	if config.MigrateLegacyRecents(cfg) {
		_ = config.SaveConfig(p, cfg)
	}
~~~

并在 `store.go` 的 import 加 `"sort"`。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/config/ -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

~~~bash
git add internal/config/store.go internal/config/store_test.go
git commit -m "feat(config): 书签与双侧最近位置结构 + 显式迁移标记与幂等迁移"
~~~

---

### Task 5: app.go 深搜与位置绑定（含取消语义转换）

**Files:**
- Modify: `app.go`（App 增 `searchMu/searchCancels`；新增绑定；`recordRecentSFTP` 改写）
- Test: `app_test.go`
- Regenerate: `frontend/wailsjs/go/main/App.{js,d.ts}`、`models.ts`

**Interfaces:**
- Produces:
  - `type RemoteSearchRequest struct{ ID, Host, Root, Pattern string; MaxDepth, Limit int }`
  - `type LocalSearchRequest struct{ ID, Root, Pattern string; MaxDepth, Limit int }`
  - `type LocalSearchOutcome struct{ Hits []localfs.Hit; Scanned, Unreadable int; Truncated, Cancelled bool }`
  - `func (a *App) SftpSearch(req RemoteSearchRequest) (sftp.SearchOutcome, error)`
  - `func (a *App) LocalSearch(req LocalSearchRequest) (LocalSearchOutcome, error)`
  - `func (a *App) SearchCancel(id string)`
  - `type Locations struct{ Bookmarks []config.Bookmark; LocalRecents []config.RecentLocal; RemoteRecents []config.RecentRemote }`
  - `ListLocations` / `AddBookmark` / `RemoveBookmark` / `AddLocalRecent` / `AddRemoteRecent`

- [ ] **Step 1: 写失败测试**

~~~go
func TestSearchDepthNormalization(t *testing.T) {
	if got := normDepth(0); got != 5 {
		t.Fatalf("0 must normalize to 5, got %d", got)
	}
	if got := normDepth(-1); got != -1 {
		t.Fatalf("-1 means unlimited, got %d", got)
	}
	if got := normDepth(3); got != 3 {
		t.Fatalf("explicit depth must pass through, got %d", got)
	}
}

func TestAddAndRemoveBookmark(t *testing.T) {
	dir := t.TempDir()
	a := NewApp()
	a.cfgPath = filepath.Join(dir, "sshore.toml")
	a.cfg = config.DefaultAppConfig()
	b := config.Bookmark{Name: "日志", Scope: "remote", Host: "prod", Path: "/var/log"}
	if err := a.AddBookmark(b); err != nil {
		t.Fatalf("AddBookmark: %v", err)
	}
	if got := a.ListLocations().Bookmarks; len(got) != 1 || got[0].Path != "/var/log" {
		t.Fatalf("bookmarks = %+v", got)
	}
	if err := a.RemoveBookmark("remote", "prod", "/var/log"); err != nil {
		t.Fatalf("RemoveBookmark: %v", err)
	}
	if got := a.ListLocations().Bookmarks; len(got) != 0 {
		t.Fatalf("bookmark not removed: %+v", got)
	}
}

func TestAddRemoteRecentDedupesAndCaps(t *testing.T) {
	dir := t.TempDir()
	a := NewApp()
	a.cfgPath = filepath.Join(dir, "sshore.toml")
	a.cfg = config.DefaultAppConfig()
	for i := 0; i < 25; i++ {
		if err := a.AddRemoteRecent("prod", "/p"+string(rune('a'+i%26))); err != nil {
			t.Fatal(err)
		}
	}
	got := a.ListLocations().RemoteRecents
	if len(got) > 20 {
		t.Fatalf("must cap at 20, got %d", len(got))
	}
}
~~~

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./ -run 'TestSearchDepth|TestAddAndRemoveBookmark|TestAddRemoteRecent' -v`
Expected: FAIL —— `undefined: normDepth`

- [ ] **Step 3: 实现**

App 结构体加字段（并在 import 块补 `stdsync "sync"` 与 `"sshore/internal/localfs"`——后者供 LocalSearch 使用）：

~~~go
	searchMu      stdsync.Mutex // 注意：app.go 已 import "sshore/internal/sync"，标准库必须用别名
	searchCancels map[string]context.CancelFunc
~~~

搜索绑定与辅助：

~~~go
// normDepth：绑定层把 0 当"未设置"归一为 5；-1 表示无限（仓库惯例）；>0 原样。
func normDepth(d int) int {
	if d == 0 {
		return 5
	}
	return d
}

type RemoteSearchRequest struct {
	ID       string `json:"id"`
	Host     string `json:"host"`
	Root     string `json:"root"`
	Pattern  string `json:"pattern"`
	MaxDepth int    `json:"maxDepth"`
	Limit    int    `json:"limit"`
}

type LocalSearchRequest struct {
	ID       string `json:"id"`
	Root     string `json:"root"`
	Pattern  string `json:"pattern"`
	MaxDepth int    `json:"maxDepth"`
	Limit    int    `json:"limit"`
}

type LocalSearchOutcome struct {
	Hits       []localfs.Hit `json:"hits"`
	Scanned    int           `json:"scanned"` // 本地恒为 0：WalkDir 不分层，前端对本地不显示进度
	Unreadable int           `json:"unreadable"`
	Truncated  bool          `json:"truncated"`
	Cancelled  bool          `json:"cancelled"`
}

func (a *App) trackSearch(id string) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	a.searchMu.Lock()
	if a.searchCancels == nil {
		a.searchCancels = map[string]context.CancelFunc{}
	}
	if old, ok := a.searchCancels[id]; ok {
		old()
	}
	a.searchCancels[id] = cancel
	a.searchMu.Unlock()
	return ctx, cancel
}

// SearchCancel 取消指定搜索；完成后由调用方调用（幂等）。
func (a *App) SearchCancel(id string) {
	a.searchMu.Lock()
	if c, ok := a.searchCancels[id]; ok {
		c()
		delete(a.searchCancels, id)
	}
	a.searchMu.Unlock()
}

// SftpSearch 远端深搜。取消**不**作为 error 返回：Wails 在 err != nil 时会丢弃
// 第一个返回值，前端将拿不到部分结果（spec §3 决策 18）。
func (a *App) SftpSearch(req RemoteSearchRequest) (sftp.SearchOutcome, error) {
	ctx, cancel := a.trackSearch(req.ID)
	defer func() { cancel(); a.SearchCancel(req.ID) }()
	out, err := a.sftp.Search(ctx, req.Host, "", req.Root, req.Pattern, normDepth(req.MaxDepth), req.Limit,
		func(scanned int) {
			if a.ctx != nil {
				runtime.EventsEmit(a.ctx, "sftp:search-progress", map[string]any{"id": req.ID, "scanned": scanned})
			}
		})
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		out.Cancelled = true
		return out, nil
	}
	return out, err
}

func (a *App) LocalSearch(req LocalSearchRequest) (LocalSearchOutcome, error) {
	ctx, cancel := a.trackSearch(req.ID)
	defer func() { cancel(); a.SearchCancel(req.ID) }()
	hits, skipped, truncated, err := localfs.Search(ctx, req.Root, req.Pattern, localfs.SearchOpts{
		MaxDepth: normDepth(req.MaxDepth), Limit: req.Limit,
	})
	out := LocalSearchOutcome{Hits: hits, Unreadable: skipped, Truncated: truncated}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		out.Cancelled = true
		return out, nil
	}
	return out, err
}
~~~

位置绑定：

~~~go
type Locations struct {
	Bookmarks     []config.Bookmark     `json:"bookmarks"`
	LocalRecents  []config.RecentLocal  `json:"localRecents"`
	RemoteRecents []config.RecentRemote `json:"remoteRecents"`
}

func (a *App) ListLocations() Locations {
	if a.cfg == nil {
		return Locations{Bookmarks: []config.Bookmark{}, LocalRecents: []config.RecentLocal{}, RemoteRecents: []config.RecentRemote{}}
	}
	out := Locations{
		Bookmarks:     a.cfg.Bookmarks,
		LocalRecents:  a.cfg.LocalRecent,
		RemoteRecents: a.cfg.RemoteRecent,
	}
	if out.Bookmarks == nil {
		out.Bookmarks = []config.Bookmark{}
	}
	if out.LocalRecents == nil {
		out.LocalRecents = []config.RecentLocal{}
	}
	if out.RemoteRecents == nil {
		out.RemoteRecents = []config.RecentRemote{}
	}
	return out
}

func (a *App) AddBookmark(b config.Bookmark) error {
	if b.Path == "" || (b.Scope != "local" && b.Scope != "remote") {
		return errors.New("invalid bookmark")
	}
	if a.cfg == nil {
		a.cfg = &config.AppConfig{}
	}
	for _, e := range a.cfg.Bookmarks {
		if e.Scope == b.Scope && e.Host == b.Host && e.Path == b.Path {
			return nil
		}
	}
	a.cfg.Bookmarks = append(a.cfg.Bookmarks, b)
	return a.saveConfig()
}

func (a *App) RemoveBookmark(scope, host, path string) error {
	if a.cfg == nil {
		return nil
	}
	kept := a.cfg.Bookmarks[:0]
	for _, e := range a.cfg.Bookmarks {
		if e.Scope == scope && e.Host == host && e.Path == path {
			continue
		}
		kept = append(kept, e)
	}
	a.cfg.Bookmarks = kept
	return a.saveConfig()
}

func (a *App) AddLocalRecent(path string) error {
	if a.cfg == nil {
		a.cfg = &config.AppConfig{}
	}
	rec := config.RecentLocal{Path: path, TS: time.Now().Format(time.RFC3339)}
	kept := a.cfg.LocalRecent[:0]
	for _, e := range a.cfg.LocalRecent {
		if e.Path == path {
			continue
		}
		kept = append(kept, e)
	}
	a.cfg.LocalRecent = append([]config.RecentLocal{rec}, kept...)
	if len(a.cfg.LocalRecent) > 20 {
		a.cfg.LocalRecent = a.cfg.LocalRecent[:20]
	}
	return a.saveConfig()
}

func (a *App) AddRemoteRecent(host, path string) error {
	if host == "" || path == "" {
		return nil
	}
	if a.cfg == nil {
		a.cfg = &config.AppConfig{}
	}
	rec := config.RecentRemote{Host: host, Path: path, TS: time.Now().Format(time.RFC3339)}
	kept := a.cfg.RemoteRecent[:0]
	for _, e := range a.cfg.RemoteRecent {
		if e.Host == host && e.Path == path {
			continue
		}
		kept = append(kept, e)
	}
	a.cfg.RemoteRecent = append([]config.RecentRemote{rec}, kept...)
	if len(a.cfg.RemoteRecent) > 20 {
		a.cfg.RemoteRecent = a.cfg.RemoteRecent[:20]
	}
	return a.saveConfig()
}
~~~

**同时必须处置 6 个存量用例**（否则 Step 4 的 `go test ./ -count=1` 必失败）：`app_test.go:551-570`（断言 `cfg.RecentSFTP` 长度）、`:573-589`、`:592-612`、`:616-637`（去重置顶）、`:640-658`（上限 10）、`:661-689`（`ListRecentSFTP` 返回 3 条）。处置方式：

- 前 5 个改写为断言新字段：去重与置顶断言 `cfg.RemoteRecent` / `cfg.LocalRecent`，上限由 10 改为 **20**；
- 第 6 个（`ListRecentSFTP`）**直接删除该用例与 `app.go` 里的 `ListRecentSFTP`**（spec §3 决策 20 要求移除），并同步删掉 `frontend/src/views/SftpView.vue` 里的 import 与 `loadRecents()`（见 Task 9 Step 1）。

把 `recordRecentSFTP` 改为写新结构（并保留其被 SftpGet/Put/GetDir/Home 调用的行为）：

~~~go
func (a *App) recordRecentSFTP(host, remoteDir, localDir string) {
	if a.cfg == nil {
		a.cfg = &config.AppConfig{}
	}
	if host != "" && remoteDir != "" {
		_ = a.AddRemoteRecent(host, remoteDir)
	}
	if localDir != "" {
		_ = a.AddLocalRecent(localDir)
	}
}
~~~

- [ ] **Step 4: 跑测试确认通过 + 重生成绑定**

Run: `go test ./ -count=1 && go vet ./... && $(go env GOPATH)/bin/wails generate module -tags webkit2_41`
Expected: 测试 PASS；`App.d.ts` 出现 `SftpSearch`/`LocalSearch`/`SearchCancel`/`ListLocations`/`AddBookmark`/`RemoveBookmark`/`AddLocalRecent`/`AddRemoteRecent`。

校验：

~~~bash
grep -c 'SftpSearch\|LocalSearch\|SearchCancel\|ListLocations\|AddBookmark\|RemoveBookmark\|AddLocalRecent\|AddRemoteRecent' frontend/wailsjs/go/main/App.d.ts
~~~

Expected: ≥ 8

- [ ] **Step 5: Commit**

~~~bash
git add app.go app_test.go frontend/wailsjs
git commit -m "feat(app): 深搜/取消与位置读写绑定（取消用 Cancelled 而非 error）"
~~~

---

### Task 6: `stores/locations.js`

**Files:**
- Create: `frontend/src/stores/locations.js`
- Test: `frontend/src/stores/locations.test.js`

**Interfaces:**
- Consumes: Task 5 的位置绑定
- Produces（Task 8/9 使用）：`useLocationsStore()`，state `{ bookmarks, localRecents, remoteRecents, loaded }`；actions `load()`、`addBookmark(b)`、`removeBookmark(scope, host, path)`、`addLocalRecent(p)`、`addRemoteRecent(host, p)`、`recentsForPane(pane, host)`

- [ ] **Step 1: 写失败测试**

~~~js
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'

vi.mock('../../wailsjs/go/main/App', () => ({
  ListLocations: vi.fn(async () => ({
    bookmarks: [{ name: '日志', scope: 'remote', host: 'prod', path: '/var/log' }],
    localRecents: [{ path: '/home/u/a', ts: '2' }],
    remoteRecents: [
      { host: 'prod', path: '/var/log', ts: '2' },
      { host: 'db', path: '/srv', ts: '1' },
    ],
  })),
  AddBookmark: vi.fn(async () => {}),
  RemoveBookmark: vi.fn(async () => {}),
  AddLocalRecent: vi.fn(async () => {}),
  AddRemoteRecent: vi.fn(async () => {}),
}))

import { useLocationsStore } from './locations'

describe('locations store', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('load 填充三组数据', async () => {
    const s = useLocationsStore()
    await s.load()
    expect(s.bookmarks.length).toBe(1)
    expect(s.localRecents.length).toBe(1)
    expect(s.remoteRecents.length).toBe(2)
  })

  it('远程最近位置按主机过滤', async () => {
    const s = useLocationsStore()
    await s.load()
    expect(s.recentsForPane('remote', 'prod').map((r) => r.path)).toEqual(['/var/log'])
    expect(s.recentsForPane('remote', 'db').map((r) => r.path)).toEqual(['/srv'])
  })

  it('本地面板的最近位置与主机无关', async () => {
    const s = useLocationsStore()
    await s.load()
    expect(s.recentsForPane('local', 'prod').map((r) => r.path)).toEqual(['/home/u/a'])
  })

  it('书签按面板/主机过滤', async () => {
    const s = useLocationsStore()
    await s.load()
    expect(s.bookmarksForPane('remote', 'prod').map((b) => b.path)).toEqual(['/var/log'])
    expect(s.bookmarksForPane('remote', 'db')).toEqual([])
  })

  it('addBookmark 后本地列表同步追加', async () => {
    const s = useLocationsStore()
    await s.load()
    await s.addBookmark({ name: 'x', scope: 'local', host: '', path: '/tmp' })
    expect(s.bookmarks.some((b) => b.path === '/tmp')).toBe(true)
  })
})
~~~

- [ ] **Step 2: 跑测试确认失败**

Run: `cd frontend && npx vitest run src/stores/locations.test.js`
Expected: FAIL —— 无法解析 `./locations`

- [ ] **Step 3: 最小实现**

~~~js
import { defineStore } from 'pinia'
import {
  ListLocations, AddBookmark, RemoveBookmark, AddLocalRecent, AddRemoteRecent,
} from '../../wailsjs/go/main/App'

export const useLocationsStore = defineStore('locations', {
  state: () => ({ bookmarks: [], localRecents: [], remoteRecents: [], loaded: false }),
  actions: {
    async load() {
      const data = await ListLocations()
      this.bookmarks = (data && data.bookmarks) || []
      this.localRecents = (data && data.localRecents) || []
      this.remoteRecents = (data && data.remoteRecents) || []
      this.loaded = true
    },
    async addBookmark(b) {
      await AddBookmark(b)
      this.bookmarks = this.bookmarks.concat([b])
    },
    async removeBookmark(scope, host, path) {
      await RemoveBookmark(scope, host, path)
      this.bookmarks = this.bookmarks.filter((b) => !(b.scope === scope && b.host === host && b.path === path))
    },
    async addLocalRecent(path) {
      await AddLocalRecent(path)
      this.localRecents = [{ path, ts: new Date().toISOString() }]
        .concat(this.localRecents.filter((r) => r.path !== path)).slice(0, 20)
    },
    async addRemoteRecent(host, path) {
      await AddRemoteRecent(host, path)
      this.remoteRecents = [{ host, path, ts: new Date().toISOString() }]
        .concat(this.remoteRecents.filter((r) => !(r.host === host && r.path === path))).slice(0, 20)
    },
    // 本地面板与主机无关；远程面板只看当前主机（spec §9）。
    recentsForPane(pane, host) {
      if (pane === 'local') return this.localRecents
      return this.remoteRecents.filter((r) => r.host === host)
    },
    // 书签同样按面板/主机过滤：否则远程面板会列出别的主机的书签。
    bookmarksForPane(pane, host) {
      return this.bookmarks.filter((b) => (pane === 'local'
        ? b.scope === 'local'
        : (b.scope === 'remote' && (!host || b.host === host))))
    },
  },
})
~~~

- [ ] **Step 4: 跑测试确认通过**

Run: `cd frontend && npx vitest run src/stores/locations.test.js`
Expected: PASS

- [ ] **Step 5: Commit**

~~~bash
git add frontend/src/stores/locations.js frontend/src/stores/locations.test.js
git commit -m "feat(sftp): 位置 store（书签 + 双侧最近，按主机过滤）与单测"
~~~

---

### Task 7: SearchOverlay 深搜浮层

**Files:**
- Create: `frontend/src/components/SearchOverlay.vue`

**Interfaces:**
- Consumes: Task 5 的 `SftpSearch/LocalSearch/SearchCancel`、`sftp:search-progress` 事件
- Produces: props `{ visible, scope: 'local'|'remote', host, root }`；事件 `close`、`open-hit({ path, isDir })`

- [ ] **Step 1: 写组件**

~~~vue
<script setup>
import { ref, watch, nextTick } from 'vue'
import { EventsOn } from '../../wailsjs/runtime/runtime'
import { SftpSearch, LocalSearch, SearchCancel } from '../../wailsjs/go/main/App'

const props = defineProps({
  visible: Boolean,
  scope: { type: String, default: 'remote' },
  host: { type: String, default: '' },
  root: { type: String, default: '/' },
})
const emit = defineEmits(['close', 'open-hit'])

const pattern = ref('')
const running = ref(false)
const hits = ref([])
const scanned = ref(0)
const unreadable = ref(0)
const truncated = ref(false)
const cancelled = ref(false)
const errorText = ref('')
let seq = 0
let offProgress = null

function title() {
  return props.scope === 'remote'
    ? '🔍 在 ' + props.host + ' : ' + props.root + ' 下递归搜索'
    : '🔍 在 ' + props.root + ' 下递归搜索'
}

async function run() {
  const id = 's' + (++seq)
  running.value = true
  cancelled.value = false
  errorText.value = ''
  hits.value = []
  scanned.value = 0
  const req = { id, root: props.root, pattern: pattern.value, maxDepth: 5, limit: 500 }
  try {
    const out = props.scope === 'remote'
      ? await SftpSearch({ ...req, host: props.host })
      : await LocalSearch(req)
    if (id !== 's' + seq) return
    hits.value = (out && out.hits) || []
    scanned.value = (out && out.scanned) || 0
    unreadable.value = (out && out.unreadable) || 0
    truncated.value = !!(out && out.truncated)
    cancelled.value = !!(out && out.cancelled)
  } catch (e) {
    if (id === 's' + seq) errorText.value = String(e)
  } finally {
    if (id === 's' + seq) running.value = false // 过期响应不得清掉新搜索的运行态
  }
}

function cancel() {
  SearchCancel('s' + seq)
  running.value = false
  cancelled.value = true
}

watch(() => props.visible, (v) => {
  if (v) {
    // 聚焦输入框：@keyup.esc 挂在遮罩上，只有焦点在子树里才会触发（spec §8.2 要求 Esc 可关）。
    nextTick(() => { const el = document.querySelector('.search-overlay input'); if (el) el.focus() })
    pattern.value = ''
    hits.value = []
    offProgress = EventsOn('sftp:search-progress', (p) => { if (p && p.id === 's' + seq) scanned.value = p.scanned })
    run()
  } else {
    // 关闭即取消：否则远端 BFS 会继续空跑（spec §8.2）。
    SearchCancel('s' + seq)
    running.value = false
    if (offProgress) { offProgress(); offProgress = null }
  }
})

onUnmounted(() => {
  SearchCancel('s' + seq)
  if (offProgress) { offProgress(); offProgress = null }
})
</script>

<template>
  <div v-if="visible" class="ui-overlay search-overlay" @click.self="emit('close')" @keyup.esc="emit('close')">
    <div class="panel">
      <div class="head">
        <span class="ftitle">{{ title() }}</span>
        <span v-if="running" class="dim">搜索中… 已扫描 {{ scanned }} 个目录</span>
        <button v-if="running" @click="cancel">取消</button>
        <button @click="emit('close')">关闭</button>
      </div>
      <div class="bar">
        <input v-model="pattern" placeholder="名称匹配（支持 * ?）" @keyup.enter="run" />
        <button @click="run">搜索</button>
      </div>
      <p v-if="errorText" class="err">{{ errorText }}</p>
      <p v-else class="dim">
        命中 {{ hits.length }} 项<template v-if="truncated"> · 触发上限（最多 500 条）</template>
        <template v-if="unreadable"> · {{ unreadable }} 个目录不可读</template>
        <template v-if="cancelled"> · 已取消（结果为已扫描部分）</template>
      </p>
      <ul class="hits">
        <li v-for="h in hits" :key="h.path" @dblclick="emit('open-hit', h)">
          <span class="p">{{ h.isDir ? '📁' : '📄' }} {{ h.path }}</span>
          <span class="s">{{ h.size || '—' }}</span>
          <span class="t">{{ h.modTime || '—' }}</span>
        </li>
      </ul>
    </div>
  </div>
</template>

<style scoped>
.panel { background: var(--bg-elev); border: 1px solid var(--border); border-radius: 8px; padding: 14px; width: 640px; max-height: 80vh; display: flex; flex-direction: column; text-align: left; }
.head { display: flex; gap: 8px; align-items: center; }
.ftitle { color: var(--text); font-weight: 600; flex: 1; }
.bar { display: flex; gap: 8px; margin: 10px 0; }
.bar input { flex: 1; }
.dim { color: var(--text-faint); font-size: var(--fs-12); }
.err { color: var(--danger); font-size: var(--fs-12); }
.hits { list-style: none; margin: 0; padding: 0; overflow: auto; font-family: monospace; font-size: var(--fs-12); }
.hits li { display: flex; gap: 10px; padding: 3px 4px; color: var(--text); cursor: pointer; }
.hits li:hover { background: var(--surface-hover); }
.hits .p { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.hits .s { width: 90px; text-align: right; color: var(--text-dim); }
.hits .t { width: 130px; text-align: right; color: var(--text-faint); }
</style>
~~~

- [ ] **Step 2: 构建验证**

Run: `cd frontend && npm run build`
Expected: 构建成功

- [ ] **Step 3: Commit**

~~~bash
git add frontend/src/components/SearchOverlay.vue
git commit -m "feat(sftp): 深搜浮层（进度/取消/完整性标注/双击跳转）"
~~~

---

### Task 8: FilePane 面板头四控件（位置 / ☆ / 深搜 / 已选）

**Files:**
- Modify: `frontend/src/components/FilePane.vue`（在 P1 版本的面板头上**追加**控件，不要整段替换——P1 版 head 里的 `<span class="count">{{ shown.length }} 项</span>` 必须保留）

**Interfaces:**
- Consumes: `locations store`（Task 6）
- Produces: props 增 `pane: 'local'|'remote'`、`host: string`、`bookmarks: Array`、`recents: Array`、`bookmarked: boolean`；emits 增 `pick-position(path)`、`toggle-bookmark()`、`search()`

- [ ] **Step 1: 扩展面板头**

~~~html
<div class="head">
  <span class="title">{{ title }}</span>
  <span class="curpath">{{ path }}</span>
  <select class="pos" :value="''" @change="onPick($event)">
    <option value="" disabled selected>📍 位置</option>
    <optgroup v-if="bookmarks.length" label="书签">
      <option v-for="b in bookmarks" :key="'b' + b.path" :value="b.path">{{ b.name || b.path }}</option>
    </optgroup>
    <optgroup v-if="recents.length" label="最近">
      <option v-for="r in recents" :key="'r' + r.path" :value="r.path">{{ r.path }}</option>
    </optgroup>
  </select>
  <button class="star" :class="{ on: bookmarked }" :title="bookmarked ? '取消收藏' : '收藏当前目录'" @click="emit('toggle-bookmark')">☆</button>
  <button class="find" title="递归深搜" @click="emit('search')">🔍</button>
  <span class="chip">已选 {{ selKeys.length }} 项 ▾</span>
</div>
~~~

script 增：

~~~js
const props = defineProps({ /* 既有 + */ pane: { type: String, default: 'remote' }, host: { type: String, default: '' }, bookmarks: { type: Array, default: () => [] }, recents: { type: Array, default: () => [] }, bookmarked: { type: Boolean, default: false } })
const emit = defineEmits([ /* 既有 + */ 'pick-position', 'toggle-bookmark', 'search' ])
function onPick(e) { const v = e.target.value; e.target.value = ''; if (v) emit('pick-position', v) }
~~~

样式补 `.pos { max-width: 150px; font-size: var(--fs-11); }` 与 `.star.on { color: var(--seed); }`。

- [ ] **Step 2: 构建验证**

Run: `cd frontend && npm run build`
Expected: 构建成功

- [ ] **Step 3: Commit**

~~~bash
git add frontend/src/components/FilePane.vue
git commit -m "feat(sftp): 面板头位置下拉、收藏、深搜入口与已选徽标"
~~~

---

### Task 9: SftpView 接线：深搜、位置、移除旧下拉

**Files:**
- Modify: `frontend/src/views/SftpView.vue`（新增编排；删除 header 全局最近位置相关 UI 与逻辑）

**Interfaces:**
- Consumes: Task 5/6/7/8 的产物
- Produces: 终态接线

- [ ] **Step 1: 删除 header 全局「最近位置」**

删除 `SftpView.vue` 中：`recents` ref、`applyRecent()`、`loadRecents()` 函数、template 里 `<select class="recent">…</select>`，以及 `ListRecentSFTP` 的 import。**并且删掉它的两个调用点**：`onMounted`（SftpView.vue:338）与 `onActivated`（:368）里的 `loadRecents()`——只删函数声明会留下未定义调用。
**保留** `pendingPath` 机制：它现在由「位置下拉」触发 —— 同机已连接则直接跳转，未连接则记录后调用 `connect()`（`connect()` 内部消费 `pendingPath` 的既有逻辑不动）。

~~~js
async function pickPosition(pane, path) {
  if (pane === 'local') { localPath.value = path; await loadLocal(); await locations.addLocalRecent(path); return }
  if (host.value && connected.value) { remotePath.value = path; await loadRemote(); await locations.addRemoteRecent(host.value, path); return }
  pendingPath.value = path
  await connect()
}
~~~

- [ ] **Step 2: 接入 store 与深搜浮层**

~~~js
const locations = useLocationsStore()
const search = reactive({ visible: false, pane: 'remote' })

onMounted(async () => { await locations.load() })

function bookmarkedFor(pane) {
  const p = pane === 'local' ? localPath.value : remotePath.value
  return locations.bookmarks.some((b) =>
    b.path === p && (pane === 'local' ? b.scope === 'local' : (b.scope === 'remote' && b.host === host.value)))
}

async function toggleBookmark(pane) {
  const p = pane === 'local' ? localPath.value : remotePath.value
  if (!p) return
  const existing = locations.bookmarks.find((b) =>
    b.path === p && (pane === 'local' ? b.scope === 'local' : (b.scope === 'remote' && b.host === host.value)))
  if (existing) await locations.removeBookmark(existing.scope, existing.host, existing.path)
  else await locations.addBookmark({ name: p, scope: pane, host: pane === 'remote' ? host.value : '', path: p })
}

// 双击深搜结果 → 跳到所在目录并选中该项
async function openHit(hit) {
  const dir = hit.path.includes('/') ? hit.path.slice(0, hit.path.lastIndexOf('/')) : ''
  const name = hit.path.includes('/') ? hit.path.slice(hit.path.lastIndexOf('/') + 1) : hit.path
  if (search.pane === 'local') {
    const next = dir ? (localPath.value.replace(/\/+$/, '') + '/' + dir) : localPath.value
    localPath.value = next
    await loadLocal()
    if (name) sel.single(localSelection, name)
  } else {
    const next = dir ? (remotePath.value.replace(/\/+$/, '') + '/' + dir) : remotePath.value
    remotePath.value = next
    await loadRemote()
    if (name) sel.single(remoteSelection, name)
  }
  search.visible = false
}
~~~

模板：两个 FilePane 传 `pane`/`host`/`bookmarks`/`recents`/`bookmarked` 并接三个事件，取值必须走 store 的过滤 helper：

~~~html
<!-- 本地面板：书签无主机概念，最近位置与主机无关 -->
:bookmarks="locations.bookmarksForPane('local', '')" :recents="locations.recentsForPane('local', '')" :bookmarked="bookmarkedFor('local')"
@picked-position="pickPosition('local', $event)" @toggle-bookmark="toggleBookmark('local')" @search="search.pane = 'local'; search.visible = true"
<!-- 远程面板：书签与最近位置都按当前主机过滤 -->
:bookmarks="locations.bookmarksForPane('remote', host)" :recents="locations.recentsForPane('remote', host)" :bookmarked="bookmarkedFor('remote')"
@picked-position="pickPosition('remote', $event)" @toggle-bookmark="toggleBookmark('remote')" @search="search.pane = 'remote'; search.visible = true"
~~~

末尾加：

~~~html
<SearchOverlay :visible="search.visible" :scope="search.pane" :host="host" :root="search.pane === 'local' ? localPath : remotePath"
  @close="search.visible = false" @open-hit="openHit" />
~~~

并 `import SearchOverlay from '../components/SearchOverlay.vue'`、`import { useLocationsStore } from '../stores/locations'`。
连接/跳转成功后补记最近位置：在 `connect()` 成功分支加 `await locations.addRemoteRecent(h, remotePath.value)`；在 `loadLocal()` 成功分支加 `await locations.addLocalRecent(localPath.value)`（若列表过长由后端截断）。

- [ ] **Step 3: 构建 + 回归**

Run: `cd frontend && npm run build && npx vitest run`
Expected: 构建成功；前端测试全绿

- [ ] **Step 4: Commit**

~~~bash
git add frontend/src/views/SftpView.vue
git commit -m "feat(sftp): 接入双侧深搜与位置下拉，移除 header 全局最近位置"
~~~

---

### Task 10: README 与全量回归

**Files:**
- Modify: `README.md`、`README.en.md`

- [ ] **Step 1: 更新文档**

在 README 的 SFTP 条目里补齐（英文版同义翻译）：双侧即时过滤、双侧递归深搜（可取消 / 限深 5 层 / 最多 500 条 / 不可读目录如实标注）、每面板书签与最近位置（上限 20、远程按主机隔离）。

同时更新 README 的**配置文件示例**（`README.md:117`、`README.en.md:124` 目前只列了 `[[recent_sftp]]`），补上 spec §10.1 的新结构：

~~~toml
[[bookmarks]]              # 手动固定的位置（scope = local | remote）
[[local_recent]]           # 本地最近位置（自动记录，上限 20）
[[remote_recent]]          # 远程最近位置（按 host 隔离，上限 20）
legacy_migrated = true     # 旧 recent_sftp 已迁移（保留解析一版以便降级）
~~~

- [ ] **Step 2: 全量回归**

Run: `make ci && make e2e`
Expected: vet 无输出；Go 测试（`-race`）全绿；vitest 全绿；e2e 全绿。

- [ ] **Step 3: 验收对照（手工，spec §12.3 第 6/7/8 条）**

1. 远程深搜中途点「取消」→ 浮层显示「已取消（结果为已扫描部分）」且仍列出部分命中（**不是**报错）。
2. 本地深搜毫秒返回；双击结果 → 跳到所在目录并选中该项。
3. 过滤后选中隐藏项 → 面板头与投递确认框都显示「含 M 项被过滤」。
4. 书签增删、切换主机时远程位置列表随之过滤；旧 config 迁移后「最近」仍可用且不重复。

- [ ] **Step 4: Commit**

~~~bash
git add README.md README.en.md
git commit -m "docs(readme): 补充过滤、深搜与位置能力说明"
~~~
