package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kevinburke/ssh_config"
)

// Host represents a resolved SSH host entry from ~/.ssh/config.
type Host struct {
	Alias        string
	HostName     string
	User         string
	Port         int
	IdentityFile string
	ProxyJump    string
}

// FindSSHConfigPath returns the user's SSH config path (~/.ssh/config).
func FindSSHConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home: %w", err)
	}
	return filepath.Join(home, ".ssh", "config"), nil
}

// EnumerateHosts decodes the SSH config and returns discrete (non-wildcard)
// host blocks. It skips wildcard-only and empty-aliased entries.
func EnumerateHosts(configPath string) ([]Host, error) {
	f, err := os.Open(configPath)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", configPath, err)
	}
	defer f.Close()

	cfg, err := ssh_config.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", configPath, err)
	}

	var hosts []Host
	for _, h := range cfg.Hosts {
		if len(h.Patterns) == 0 {
			continue
		}
		alias := h.Patterns[0].String()
		if alias == "" || alias == "*" || containsWildcard(alias) {
			continue
		}
		host := Host{Alias: alias}
		host.HostName = cfgGet(cfg, alias, "HostName")
		host.User = cfgGet(cfg, alias, "User")
		host.IdentityFile = cfgGet(cfg, alias, "IdentityFile")
		host.ProxyJump = cfgGet(cfg, alias, "ProxyJump")
		if p := cfgGet(cfg, alias, "Port"); p != "" {
			host.Port = atoiOrZero(p)
		}
		if host.HostName == "" {
			continue // skip blocks with no resolvable hostname
		}
		hosts = append(hosts, host)
	}
	return hosts, nil
}

// ResolveViaSSH_G runs `ssh -G <alias>` and returns the parsed key/value map.
// runner is injected for testability; the default wrapper uses exec.
func ResolveViaSSH_G(alias string, runner func(string) (string, error)) (map[string]string, error) {
	out, err := runner(alias)
	if err != nil {
		return nil, fmt.Errorf("ssh -G %s: %w", alias, err)
	}
	res := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		parts := strings.Fields(line) // "key value"
		if len(parts) < 2 {
			continue
		}
		res[strings.ToLower(parts[0])] = strings.Join(parts[1:], " ")
	}
	return res, nil
}

func containsWildcard(s string) bool {
	for _, c := range s {
		if c == '*' || c == '?' {
			return true
		}
	}
	return false
}

func cfgGet(cfg *ssh_config.Config, alias, key string) string {
	v, err := cfg.Get(alias, key)
	if err != nil {
		return ""
	}
	return v
}

func atoiOrZero(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}
