package forward

import (
	"fmt"
	"net"
	"reflect"
	"testing"

	"sshkit/internal/config"
	"sshkit/internal/osutil"
)

func base() config.Tunnel {
	return config.Tunnel{
		ID: "abc", Name: "t", Host: "prod-db",
		ListenBind: "127.0.0.1", ListenPort: 5432,
		TargetHost: "127.0.0.1", TargetPort: 5432,
	}
}

func expect(t *testing.T, got, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestBuildArgsLocal(t *testing.T) {
	expect(t, BuildArgs(base()),
		[]string{"ssh", "-N", "-o", "ConnectTimeout=10", "-o", "BatchMode=yes",
			"-L", "127.0.0.1:5432:127.0.0.1:5432", "prod-db"})
}

func TestBuildArgsRemote(t *testing.T) {
	c := base()
	c.Mode = "remote"
	expect(t, BuildArgs(c),
		[]string{"ssh", "-N", "-o", "ConnectTimeout=10", "-o", "BatchMode=yes",
			"-R", "127.0.0.1:5432:127.0.0.1:5432", "prod-db"})
}

func TestBuildArgsDynamic(t *testing.T) {
	c := base()
	c.Mode = "dynamic"
	expect(t, BuildArgs(c),
		[]string{"ssh", "-N", "-o", "ConnectTimeout=10", "-o", "BatchMode=yes",
			"-D", "127.0.0.1:5432", "prod-db"})
}

func TestBuildArgsProxyJump(t *testing.T) {
	c := base()
	c.ProxyJump = "bastion"
	expect(t, BuildArgs(c),
		[]string{"ssh", "-N", "-o", "ConnectTimeout=10", "-o", "BatchMode=yes",
			"-L", "127.0.0.1:5432:127.0.0.1:5432", "-J", "bastion", "prod-db"})
}

func TestValidateHost(t *testing.T) {
	if !ValidateHost("prod-db") || !ValidateHost("10.0.0.5") || !ValidateHost("a.b.c") {
		t.Fatal("valid host rejected")
	}
	if ValidateHost("-oProxyCommand=evil") || ValidateHost("host with space") {
		t.Fatal("malicious host accepted")
	}
}

func TestStartInvalidHost(t *testing.T) {
	var emitted []Event
	tr := config.Tunnel{ID: "x", Host: "-oProxyCommand=evil", Mode: "local", ListenBind: "127.0.0.1", ListenPort: 1}
	called := false
	sp := fakeSpawner{startFunc: func(name string, args ...string) (*osutil.Process, error) {
		called = true
		return &osutil.Process{}, nil
	}}
	ctrl := NewCtrl(sp, func(e Event) { emitted = append(emitted, e) })
	if err := ctrl.Start(tr); err == nil {
		t.Fatal("expected error for malicious host")
	}
	if called {
		t.Fatal("spawner should not be called for invalid host")
	}
	if ctrl.State("x") != StateError {
		t.Fatalf("state should be error, got %s", ctrl.State("x"))
	}
}

// fakeSpawner implements osutil.Spawner for tests.
type fakeSpawner struct {
	startFunc func(name string, args ...string) (*osutil.Process, error)
}

func (f fakeSpawner) Start(name string, args ...string) (*osutil.Process, error) {
	return f.startFunc(name, args...)
}

func TestCheckLocalPortFreeAndBusy(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	if !CheckLocalPort("127.0.0.1", port) {
		t.Fatal("freshly closed port should be free")
	}
	ln2, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	defer ln2.Close()
	if CheckLocalPort("127.0.0.1", port) {
		t.Fatal("occupied port should report not-free")
	}
}

func TestCheckRemoteConflict(t *testing.T) {
	c1 := config.Tunnel{ID: "1", Host: "h", Mode: "remote", ListenBind: "127.0.0.1", ListenPort: 9000}
	c2 := config.Tunnel{ID: "2", Host: "h", Mode: "remote", ListenBind: "127.0.0.1", ListenPort: 9000}
	c3 := config.Tunnel{ID: "3", Host: "other", Mode: "remote", ListenBind: "127.0.0.1", ListenPort: 9000}
	if err := CheckRemoteConflict([]config.Tunnel{c1}, c2); err == nil {
		t.Fatal("same host+bind+port remote should conflict")
	}
	if err := CheckRemoteConflict([]config.Tunnel{c1}, c3); err != nil {
		t.Fatalf("different host remote should NOT conflict: %v", err)
	}
}

func TestClassifyError(t *testing.T) {
	cases := []struct {
		in   osutil.Outcome
		want string
	}{
		{osutil.Outcome{Stderr: "bind: Address already in use"}, "port already in use"},
		{osutil.Outcome{Stderr: "root@10.0.0.5: Permission denied (publickey)."}, "authentication failed — configure key/agent auth for this host"},
		{osutil.Outcome{Stderr: "ssh: Could not resolve hostname nodomain."}, "host not resolvable"},
		{osutil.Outcome{ExitCode: 1}, "unknown error (exit 1)"},
	}
	for _, c := range cases {
		if got := classifyError(c.in); got != c.want {
			t.Fatalf("classifyError(%+v)=%q want %q", c.in, got, c.want)
		}
	}
}
