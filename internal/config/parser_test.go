package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	osuser "os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestFindSSHConfigPath(t *testing.T) {
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, ".ssh", "config")
	got, err := FindSSHConfigPath()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestEnumerateHosts(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config")
	cfg := `Host *
  IdentityFile ~/.ssh/global_key
Host prod-db
  HostName 10.0.0.5
  User prod
  Port 2222
  IdentityFile ~/.ssh/prod_key
  ProxyJump bastion
Host staging
  HostName 10.0.0.6
`
	if err := os.WriteFile(cfgPath, []byte(cfg), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := EnumerateHosts(cfgPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 hosts, got %d: %+v", len(got), got)
	}
	var prod *Host
	for i := range got {
		if got[i].Alias == "prod-db" {
			prod = &got[i]
		}
	}
	if prod == nil {
		t.Fatal("prod-db not found")
	}
	// EnumerateHosts only resolves display/read fields. The kevinburke lib returns
	// the FIRST matching host block per key (Host * precedes prod-db), so IdentityFile
	// reflects the global default — that is acceptable; authoritative params come from ssh -G.
	if prod.HostName != "10.0.0.5" || prod.User != "prod" || prod.Port != 2222 || prod.ProxyJump != "bastion" {
		t.Fatalf("prod-db fields wrong: %+v", prod)
	}
}

// A4: 多别名 Host 行必须为每个非通配符别名各生成一条（原来只取 Patterns[0]，
// 其余别名静默丢失），值用 cfg.Get(该 alias) 解析。
func TestEnumerateHostsMultiAliasPatterns(t *testing.T) {
	path := writeTestSSHConfig(t,
		"Host web web.example.com 10.0.0.5",
		"  HostName 10.0.0.5",
		"  User deploy",
		"  Port 2222",
	)
	got, err := EnumerateHosts(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 aliases, got %d: %+v", len(got), got)
	}
	byAlias := map[string]Host{}
	for _, h := range got {
		byAlias[h.Alias] = h
	}
	for _, alias := range []string{"web", "web.example.com", "10.0.0.5"} {
		h, ok := byAlias[alias]
		if !ok {
			t.Fatalf("alias %q missing: %+v", alias, got)
		}
		if h.HostName != "10.0.0.5" || h.User != "deploy" || h.Port != 2222 {
			t.Fatalf("alias %q fields wrong: %+v", alias, h)
		}
	}
}

// A4 去重：同一 alias 出现在多个 Host 块时只列一次（值取 cfg.Get 的首个匹配）。
func TestEnumerateHostsDedupAlias(t *testing.T) {
	path := writeTestSSHConfig(t,
		"Host dup",
		"  HostName 10.0.0.1",
		"Host dup",
		"  HostName 10.0.0.2",
	)
	got, err := EnumerateHosts(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("同 alias 必须去重: want 1, got %d: %+v", len(got), got)
	}
	if got[0].Alias != "dup" || got[0].HostName != "10.0.0.1" {
		t.Fatalf("去重后值应取首个匹配块: %+v", got[0])
	}
}

// A4 护栏：遍历全部 pattern 时，通配符与否定 pattern（!x）不得成为别名。
func TestEnumerateHostsSkipsWildcardAndNegatedPatterns(t *testing.T) {
	path := writeTestSSHConfig(t,
		"Host * !bad",
		"  HostName 10.0.0.9",
		"Host good !bad",
		"  HostName 10.0.0.8",
	)
	got, err := EnumerateHosts(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].Alias != "good" {
		t.Fatalf("只应列出 good（* 通配、!bad 否定都跳过）: %+v", got)
	}
}

// A3: Include 内嵌主机必须被枚举（库把 Include 解析成私有嵌套 Config，
// 顶层 cfg.Hosts 看不到）。相对路径按 ssh 规则（~/.ssh）或配置所在目录解析；
// 字段值优先顶层 cfg.Get，库看不到该文件时用递归 Decode 的 Config 兜底。
func TestEnumerateHostsFollowsInclude(t *testing.T) {
	dir := t.TempDir()
	confDir := filepath.Join(dir, "conf.d")
	if err := os.MkdirAll(confDir, 0o755); err != nil {
		t.Fatal(err)
	}
	main := filepath.Join(dir, "config")
	if err := os.WriteFile(main, []byte("Host top\n  HostName 10.0.0.1\nInclude conf.d/*\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(confDir, "a.conf"), []byte("Host inc-web\n  HostName 10.9.0.1\n  User webuser\n  Port 2201\n  ProxyJump bastion\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(confDir, "b.conf"), []byte("Host inc-db\n  HostName 10.9.0.2\n  User dbuser\n  Port 2202\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := EnumerateHosts(main)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 hosts (top+inc-web+inc-db), got %d: %+v", len(got), got)
	}
	byAlias := map[string]Host{}
	for _, h := range got {
		byAlias[h.Alias] = h
	}
	web, ok := byAlias["inc-web"]
	if !ok {
		t.Fatalf("inc-web missing: %+v", got)
	}
	if web.HostName != "10.9.0.1" || web.User != "webuser" || web.Port != 2201 || web.ProxyJump != "bastion" {
		t.Fatalf("inc-web fields wrong: %+v", web)
	}
	db, ok := byAlias["inc-db"]
	if !ok {
		t.Fatalf("inc-db missing: %+v", got)
	}
	if db.HostName != "10.9.0.2" || db.User != "dbuser" || db.Port != 2202 {
		t.Fatalf("inc-db fields wrong: %+v", db)
	}
}

// A3: 一行 Include 多个 glob 都要展开；值优先级仍以顶层 cfg.Get 的首个匹配为准
// （主配置与 Include 同 alias 时，主配置先出现 ⇒ 取主配置值）。
func TestEnumerateHostsIncludeMultiGlobAndPrecedence(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "config")
	if err := os.WriteFile(main, []byte("Host shared\n  HostName 10.0.0.1\nInclude one.conf two.conf\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "one.conf"), []byte("Host shared\n  HostName 10.9.9.9\nHost only-one\n  HostName 10.9.0.1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "two.conf"), []byte("Host only-two\n  HostName 10.9.0.2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := EnumerateHosts(main)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 hosts (shared+only-one+only-two), got %d: %+v", len(got), got)
	}
	byAlias := map[string]Host{}
	for _, h := range got {
		byAlias[h.Alias] = h
	}
	if byAlias["shared"].HostName != "10.0.0.1" {
		t.Fatalf("顶层 cfg.Get 优先级被破坏: %+v", byAlias["shared"])
	}
	if byAlias["only-one"].HostName != "10.9.0.1" || byAlias["only-two"].HostName != "10.9.0.2" {
		t.Fatalf("多 glob 展开不全: %+v", got)
	}
}

// A3: 递归 Include（主→l1→l2…）逐层枚举；深度上限参照库的 5 层，
// 第 6 层不再展开（防环）。
func TestEnumerateHostsIncludeRecursionDepthCap(t *testing.T) {
	dir := t.TempDir()
	// chain[i] 包含 Host deep<i> 并 Include chain[i+1]；主配置 Include chain1。
	const levels = 7
	for i := 1; i <= levels; i++ {
		body := fmt.Sprintf("Host deep%d\n  HostName 10.7.0.%d\n", i, i)
		if i < levels {
			body += fmt.Sprintf("Include chain%d.conf\n", i+1)
		}
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("chain%d.conf", i)), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	main := filepath.Join(dir, "config")
	if err := os.WriteFile(main, []byte("Include chain1.conf\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := EnumerateHosts(main)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	byAlias := map[string]Host{}
	for _, h := range got {
		byAlias[h.Alias] = h
	}
	for i := 1; i <= 5; i++ {
		if _, ok := byAlias[fmt.Sprintf("deep%d", i)]; !ok {
			t.Fatalf("deep%d 应在深度上限内被枚举: %+v", i, got)
		}
	}
	for i := 6; i <= levels; i++ {
		if _, ok := byAlias[fmt.Sprintf("deep%d", i)]; ok {
			t.Fatalf("deep%d 超出 5 层深度上限，不应被枚举: %+v", i, got)
		}
	}
}

// A3: 循环 Include 不死循环 —— 相对 glob 自包含时靠 visited 去重收敛，
// 该文件的主机恰好列出一次。
func TestEnumerateHostsIncludeSelfCycleTerminates(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "loop.conf")
	if err := os.WriteFile(main, []byte("Host loophost\n  HostName 10.8.0.1\nInclude loop*\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := EnumerateHosts(main)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].Alias != "loophost" || got[0].HostName != "10.8.0.1" {
		t.Fatalf("自包含循环必须收敛且去重: %+v", got)
	}
}

// A3: 绝对路径循环 Include 由库自身的深度守卫报错 —— 必须快速返回错误，
// 绝不能死循环（本用例在超时内完成即证明收敛）。
func TestEnumerateHostsIncludeAbsoluteCycleFailsFast(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "config")
	if err := os.WriteFile(main, []byte(fmt.Sprintf("Host cyc\n  HostName 10.8.0.2\nInclude %s\n", main)), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := EnumerateHosts(main)
	if err == nil {
		t.Fatal("绝对路径自包含循环应触发库的深度守卫错误（而不是死循环或静默）")
	}
}

// A3: Include glob 解析规则单元级 —— 绝对路径原样、~/ 展开到用户 home、
// 相对路径落在给定 base（与库 NewInclude 的 ssh 规则一致）。
func TestResolveIncludeGlob(t *testing.T) {
	u, err := osuser.Current()
	if err != nil {
		t.Skipf("无法解析当前用户 home: %v", err)
	}
	if got := resolveIncludeGlob("~/conf.d/*", "/base"); got != filepath.Join(u.HomeDir, "conf.d", "*") {
		t.Fatalf("~/ 展开错误: %q", got)
	}
	// 绝对路径必须用**本平台原生**形式：Windows 的 filepath.IsAbs 不认 POSIX 的 /abs
	// （无盘符/UNC 前缀），拿 POSIX 绝对路径在 Windows 上断言"原样"是测试不可移植。
	absIn, absWant := "/abs/conf.d/*", "/abs/conf.d/*"
	if runtime.GOOS == "windows" {
		absIn, absWant = `C:\abs\conf.d\*`, `C:\abs\conf.d\*`
	}
	if got := resolveIncludeGlob(absIn, "/base"); got != absWant {
		t.Fatalf("绝对路径应原样: %q", got)
	}
	if got := resolveIncludeGlob("conf.d/*", "/base"); got != filepath.Join("/base", "conf.d", "*") {
		t.Fatalf("相对路径应落在 base: %q", got)
	}
}

func TestResolveViaSSH_G(t *testing.T) {
	output := "user prod\nhostname 10.0.0.5\nport 2222\nidentityfile /home/u/.ssh/prod_key\nproxyjump bastion\n"
	runner := func(alias string) (string, error) {
		if alias != "prod-db" {
			t.Fatalf("unexpected alias %q", alias)
		}
		return output, nil
	}
	got, err := ResolveViaSSH_G("prod-db", runner)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got["user"] != "prod" || got["hostname"] != "10.0.0.5" || got["port"] != "2222" ||
		got["proxyjump"] != "bastion" {
		t.Fatalf("wrong map: %+v", got)
	}
}

// M7: Host 作为 API 边界返回给前端，json 字段名是契约的一部分。
func TestHostJSONTags(t *testing.T) {
	h := Host{Alias: "a", HostName: "h", User: "u", Port: 22, IdentityFile: "i", ProxyJump: "p"}
	b, err := json.Marshal(h)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"alias":"a","host_name":"h","user":"u","port":22,"identity_file":"i","proxy_jump":"p"}`
	if string(b) != want {
		t.Fatalf("json tags wrong:\ngot  %s\nwant %s", b, want)
	}
}

func writeTestSSHConfig(t *testing.T, blocks ...string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	if err := os.WriteFile(path, []byte(strings.Join(blocks, "\n")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

// M7: ssh -G 的权威值（hostname/port/user/proxyjump）非空时覆盖库解析值；
// identityfile 不在覆盖集内，保持库值。
func TestEnumerateHostsDetailedOverridesViaSSH_G(t *testing.T) {
	path := writeTestSSHConfig(t,
		"Host prod-db",
		"  HostName 10.0.0.5",
		"  User prod",
		"  Port 22",
		"  IdentityFile ~/.ssh/prod_key",
		"  ProxyJump oldjump",
	)
	runner := func(alias string) (string, error) {
		if alias != "prod-db" {
			t.Fatalf("unexpected alias %q", alias)
		}
		return "user alice\nhostname real\nport 2222\nproxyjump bastion2\nidentityfile /other/key\n", nil
	}
	hosts, err := EnumerateHostsDetailed(path, runner)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(hosts) != 1 {
		t.Fatalf("want 1 host, got %d: %+v", len(hosts), hosts)
	}
	h := hosts[0]
	if h.Alias != "prod-db" || h.HostName != "real" || h.User != "alice" || h.Port != 2222 || h.ProxyJump != "bastion2" {
		t.Fatalf("ssh -G values not applied: %+v", h)
	}
	if h.IdentityFile != "~/.ssh/prod_key" {
		t.Fatalf("identityfile must NOT be overridden by ssh -G, got %q", h.IdentityFile)
	}
}

// M7: 单个 alias 的 ssh -G 失败/超时必须静默回退库值，不影响其他 alias，
// 也不向上抛错。
func TestEnumerateHostsDetailedFallsBackOnRunnerError(t *testing.T) {
	path := writeTestSSHConfig(t,
		"Host prod-db",
		"  HostName 10.0.0.5",
		"  User prod",
		"  Port 2222",
		"Host staging",
		"  HostName 10.0.0.6",
		"  User stg",
		"  Port 2233",
	)
	runner := func(alias string) (string, error) {
		if alias == "staging" {
			return "", errors.New("ssh -G staging: timeout")
		}
		return "user alice\nhostname real\nport 9999\n", nil
	}
	hosts, err := EnumerateHostsDetailed(path, runner)
	if err != nil {
		t.Fatalf("per-alias ssh -G failure must not bubble up: %v", err)
	}
	byAlias := map[string]Host{}
	for _, h := range hosts {
		byAlias[h.Alias] = h
	}
	prod, staging := byAlias["prod-db"], byAlias["staging"]
	if prod.HostName != "real" || prod.User != "alice" || prod.Port != 9999 {
		t.Fatalf("prod-db should be enriched: %+v", prod)
	}
	if staging.HostName != "10.0.0.6" || staging.User != "stg" || staging.Port != 2233 {
		t.Fatalf("staging must keep library values after runner failure: %+v", staging)
	}
}

// M7: 富化必须并发执行（runner 真实睡眠），且并发度不超过 8 个 worker；
// 24 个 alias 全部正确富化（-race 下验证无共享写冲突）。
func TestEnumerateHostsDetailedEnrichesConcurrently(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 24; i++ {
		fmt.Fprintf(&sb, "Host h%02d\n  HostName 10.1.%d.%d\n  User lib\n  Port 22\n", i, i/10, i%10)
	}
	path := writeTestSSHConfig(t, sb.String())

	var cur, peak int32
	runner := func(alias string) (string, error) {
		n, _ := strconv.Atoi(alias[1:])
		if c := atomic.AddInt32(&cur, 1); c > atomic.LoadInt32(&peak) {
			for {
				p := atomic.LoadInt32(&peak)
				if p >= c || atomic.CompareAndSwapInt32(&peak, p, c) {
					break
				}
			}
		}
		defer atomic.AddInt32(&cur, -1)
		time.Sleep(30 * time.Millisecond)
		return fmt.Sprintf("user user-%d\nhostname real-%d\nport %d\n", n, n, 2200+n), nil
	}
	hosts, err := EnumerateHostsDetailed(path, runner)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(hosts) != 24 {
		t.Fatalf("want 24 hosts, got %d", len(hosts))
	}
	byAlias := map[string]Host{}
	for _, h := range hosts {
		byAlias[h.Alias] = h
	}
	for i := 0; i < 24; i++ {
		want := fmt.Sprintf("h%02d", i)
		h, ok := byAlias[want]
		if !ok {
			t.Fatalf("alias %s missing", want)
		}
		if h.HostName != fmt.Sprintf("real-%d", i) || h.User != fmt.Sprintf("user-%d", i) || h.Port != 2200+i {
			t.Fatalf("alias %s not enriched: %+v", want, h)
		}
	}
	if pk := atomic.LoadInt32(&peak); pk > 8 {
		t.Fatalf("worker cap violated: %d concurrent ssh -G calls", pk)
	}
	if pk := atomic.LoadInt32(&peak); pk < 2 {
		t.Fatalf("expected concurrent enrichment, only %d calls overlapped", pk)
	}
}

// M7: runner==nil 时必须走默认的 `ssh -G` exec 包装（经可替换变量验证接线，
// 避免依赖真实 ssh 二进制）。
func TestEnumerateHostsDetailedNilRunnerUsesDefaultSSH_G(t *testing.T) {
	path := writeTestSSHConfig(t,
		"Host prod-db",
		"  HostName 10.0.0.5",
	)
	old := defaultSSHGRun
	var calls []string
	defaultSSHGRun = func(alias string) (string, error) {
		calls = append(calls, alias)
		return "user alice\nhostname real\nport 2222\n", nil
	}
	t.Cleanup(func() { defaultSSHGRun = old })

	hosts, err := EnumerateHostsDetailed(path, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(calls) != 1 || calls[0] != "prod-db" {
		t.Fatalf("default runner should be invoked per alias, got %v", calls)
	}
	if hosts[0].HostName != "real" || hosts[0].User != "alice" || hosts[0].Port != 2222 {
		t.Fatalf("enrichment via default runner wrong: %+v", hosts[0])
	}
}
