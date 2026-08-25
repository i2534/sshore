package importer

import "testing"

func TestParseLocalMultiple(t *testing.T) {
	cmd := "ssh -N -L 5432:127.0.0.1:5432 -L 8080:127.0.0.1:80 prod-db"
	tunnels, err := Parse(cmd)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(tunnels) != 2 {
		t.Fatalf("want 2 tunnels got %d: %+v", len(tunnels), tunnels)
	}
	if tunnels[0].Mode != "local" || tunnels[0].ListenPort != 5432 || tunnels[0].TargetPort != 5432 || tunnels[0].Host != "prod-db" {
		t.Fatalf("tunnel[0] wrong: %+v", tunnels[0])
	}
	if tunnels[1].ListenPort != 8080 {
		t.Fatalf("tunnel[1] wrong: %+v", tunnels[1])
	}
}

func TestParseDynamicAndJump(t *testing.T) {
	cmd := "ssh -N -D 1080 -J bastion prod-db"
	tunnels, err := Parse(cmd)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(tunnels) != 1 {
		t.Fatalf("want 1 tunnel got %d", len(tunnels))
	}
	if tunnels[0].Mode != "dynamic" || tunnels[0].ListenPort != 1080 || tunnels[0].ProxyJump != "bastion" {
		t.Fatalf("tunnel wrong: %+v", tunnels[0])
	}
}

func TestParseMaliciousHostRejected(t *testing.T) {
	cmd := "ssh -N -L 5432:127.0.0.1:5432 -oProxyCommand=evil prod-db"
	if _, err := Parse(cmd); err == nil {
		t.Fatal("expected error for malicious/unknown flag")
	}
}
