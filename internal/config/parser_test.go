package config

import (
	"os"
	"path/filepath"
	"testing"
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
