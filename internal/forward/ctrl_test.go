package forward

import (
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
