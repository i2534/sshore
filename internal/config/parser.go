package config

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/kevinburke/ssh_config"
)

// Host represents a resolved SSH host entry from ~/.ssh/config.
// json 字段名是前端契约（wailsjs 绑定返回该结构）。
type Host struct {
	Alias        string `json:"alias"`
	HostName     string `json:"host_name"`
	User         string `json:"user"`
	Port         int    `json:"port"`
	IdentityFile string `json:"identity_file"`
	ProxyJump    string `json:"proxy_jump"`
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

// sshGTimeout 限制单次 ssh -G 调用的时间（契约：每 alias ≤5s）。
const sshGTimeout = 5 * time.Second

// defaultSSHGRun 以 5s 超时运行 `ssh -G <alias>` 并返回其 stdout。
// 声明为变量而非函数，便于测试在不依赖真实 ssh 二进制的情况下验证接线。
var defaultSSHGRun = func(alias string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), sshGTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ssh", "-G", alias).Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// EnumerateHostsDetailed 在 EnumerateHosts 基础上，用 ssh -G 的权威值富化每个
// Host：hostname/port/user/proxyjump 非空时覆盖库解析值（port 经 atoiOrZero）。
// 富化以 ≤8 个 worker 并发执行；单个 alias 的 ssh -G 失败/超时静默回退库值。
// runner 为 nil 时使用默认的 exec 包装（见 defaultSSHGRun）。
func EnumerateHostsDetailed(configPath string, runner func(string) (string, error)) ([]Host, error) {
	hosts, err := EnumerateHosts(configPath)
	if err != nil {
		return nil, err
	}
	if runner == nil {
		runner = defaultSSHGRun
	}
	const maxWorkers = 8
	jobs := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < maxWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				enrichHostViaSSH_G(&hosts[i], runner)
			}
		}()
	}
	for i := range hosts {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	return hosts, nil
}

// enrichHostViaSSH_G 就地富化单个 Host；ssh -G 失败时保持库值不变。
func enrichHostViaSSH_G(h *Host, runner func(string) (string, error)) {
	res, err := ResolveViaSSH_G(h.Alias, runner)
	if err != nil {
		return
	}
	if v := res["hostname"]; v != "" {
		h.HostName = v
	}
	if v := res["user"]; v != "" {
		h.User = v
	}
	if v := res["port"]; v != "" {
		if p := atoiOrZero(v); p > 0 {
			h.Port = p
		}
	}
	if v := res["proxyjump"]; v != "" {
		h.ProxyJump = v
	}
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
