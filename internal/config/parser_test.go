package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
