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

func TestParseQuotedSpecToken(t *testing.T) {
	tunnels, err := Parse(`ssh -L "8080:localhost:80" host`)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(tunnels) != 1 {
		t.Fatalf("want 1 tunnel got %d: %+v", len(tunnels), tunnels)
	}
	t0 := tunnels[0]
	if t0.Mode != "local" || t0.ListenPort != 8080 || t0.TargetHost != "localhost" || t0.TargetPort != 80 || t0.Host != "host" {
		t.Fatalf("tunnel wrong: %+v", t0)
	}
}

func TestParseBracketedIPv6Specs(t *testing.T) {
	tunnels, err := Parse(`ssh -D "[::1]:1080" host`)
	if err != nil {
		t.Fatalf("dynamic ipv6: %v", err)
	}
	if len(tunnels) != 1 {
		t.Fatalf("want 1 tunnel got %d: %+v", len(tunnels), tunnels)
	}
	t0 := tunnels[0]
	if t0.Mode != "dynamic" || t0.ListenBind != "[::1]" || t0.ListenPort != 1080 {
		t.Fatalf("dynamic ipv6 tunnel wrong: %+v", t0)
	}

	tunnels, err = Parse(`ssh -L "[::1]:8080:db:80" host`)
	if err != nil {
		t.Fatalf("local 4-part ipv6: %v", err)
	}
	if len(tunnels) != 1 {
		t.Fatalf("want 1 tunnel got %d: %+v", len(tunnels), tunnels)
	}
	t0 = tunnels[0]
	if t0.Mode != "local" || t0.ListenBind != "[::1]" || t0.ListenPort != 8080 || t0.TargetHost != "db" || t0.TargetPort != 80 {
		t.Fatalf("local 4-part ipv6 tunnel wrong: %+v", t0)
	}

	tunnels, err = Parse(`ssh -L "8080:[::1]:80" host`)
	if err != nil {
		t.Fatalf("local 3-part ipv6: %v", err)
	}
	if len(tunnels) != 1 {
		t.Fatalf("want 1 tunnel got %d: %+v", len(tunnels), tunnels)
	}
	t0 = tunnels[0]
	if t0.Mode != "local" || t0.ListenPort != 8080 || t0.TargetHost != "[::1]" || t0.TargetPort != 80 {
		t.Fatalf("local 3-part ipv6 tunnel wrong: %+v", t0)
	}
}

func TestParseUserAtHost(t *testing.T) {
	tunnels, err := Parse("ssh -L 8080:localhost:80 bob@myhost")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(tunnels) != 1 {
		t.Fatalf("want 1 tunnel got %d: %+v", len(tunnels), tunnels)
	}
	t0 := tunnels[0]
	if t0.Host != "myhost" || t0.User != "bob" {
		t.Fatalf("user@host split wrong: %+v", t0)
	}
}

func TestParseCombinedQuotedIPv6UserAtHost(t *testing.T) {
	tunnels, err := Parse(`ssh -L "8080:[::1]:80" alice@myhost`)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(tunnels) != 1 {
		t.Fatalf("want 1 tunnel got %d: %+v", len(tunnels), tunnels)
	}
	t0 := tunnels[0]
	if t0.Mode != "local" || t0.ListenPort != 8080 || t0.TargetHost != "[::1]" || t0.TargetPort != 80 || t0.Host != "myhost" || t0.User != "alice" {
		t.Fatalf("combined tunnel wrong: %+v", t0)
	}
}
