package forward

import (
	"net"
	"strconv"
	"testing"
)

// F2 回归：IPv6 字面量 bind 的端口预检必须可绑定。
// 旧实现 fmt.Sprintf("%s:%d", "::1", port) 拼出 "::1:port"，
// net.Listen 报 "too many colons in address"，空闲端口也恒返回 false。
func TestCheckLocalPortIPv6Loopback(t *testing.T) {
	ln, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		t.Skipf("本机无 IPv6 回环，跳过: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	if !CheckLocalPort("::1", port) {
		t.Fatalf("CheckLocalPort(\"::1\", %d) = false, want true（刚释放的 IPv6 回环端口应可绑定）", port)
	}
	// 占用中的 IPv6 端口仍须报 not-free（语义不变）。
	ln2, err := net.Listen("tcp", net.JoinHostPort("::1", strconv.Itoa(port)))
	if err != nil {
		t.Fatal(err)
	}
	defer ln2.Close()
	if CheckLocalPort("::1", port) {
		t.Fatal("occupied IPv6 port should report not-free")
	}
}

// F2 回归：BuildArgs 对 IPv6 字面量 bind 必须按 ssh 语法加方括号。
// 字面量断言：-L/-R/-D 三种模式的转发 spec。
func TestBuildArgsIPv6ListenBind(t *testing.T) {
	prefix := []string{"ssh", "-N", "-o", "ConnectTimeout=10", "-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes", "-o", "ExitOnForwardFailure=yes"}
	c := base()
	c.ListenBind = "::1"
	expect(t, BuildArgs(c), append(append([]string{}, prefix...), "-L", "[::1]:5432:127.0.0.1:5432", "prod-db"))
	c.Mode = "remote"
	expect(t, BuildArgs(c), append(append([]string{}, prefix...), "-R", "[::1]:5432:127.0.0.1:5432", "prod-db"))
	c.Mode = "dynamic"
	expect(t, BuildArgs(c), append(append([]string{}, prefix...), "-D", "[::1]:5432", "prod-db"))
}

// F2 守卫：IPv4 与空 bind 的拼接行为逐字不变（空 bind 仍产出 ":port" 形式）。
func TestBuildArgsEmptyListenBindUnchanged(t *testing.T) {
	c := base()
	c.ListenBind = ""
	expect(t, BuildArgs(c),
		[]string{"ssh", "-N", "-o", "ConnectTimeout=10", "-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes", "-o", "ExitOnForwardFailure=yes",
			"-L", ":5432:127.0.0.1:5432", "prod-db"})
}
