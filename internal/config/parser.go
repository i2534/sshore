package config

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	osuser "os/user"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/kevinburke/ssh_config"

	"sshore/internal/osutil"
)

// maxIncludeDepth 参照 kevinburke/ssh_config 的 maxRecurseDepth（5 层 Include），
// 与 visited 去重一起防止循环 Include 无限递归。
const maxIncludeDepth = 5

// sshUserHome 与库的 homedir() 同规则：os/user.Current 的 HomeDir（passwd），
// 失败时回退 $HOME。注意它**不随 $HOME 覆盖而变**（os/user.Current 优先 passwd），
// 所以相对 Include 的解析与库保持一致，cfg.Get 才能钻到同一批文件。
func sshUserHome() string {
	if u, err := osuser.Current(); err == nil {
		return u.HomeDir
	}
	return os.Getenv("HOME")
}

// resolveIncludeGlob 按 ssh 规则展开 Include glob：绝对路径原样；~/ 展开到用户
// home；其余相对路径落在 base（用户配置 ~/.ssh、系统配置 /etc/ssh，见
// includeBaseDirs）。与库 NewInclude 的路径规则逐条对齐。
func resolveIncludeGlob(glob, base string) string {
	if filepath.IsAbs(glob) {
		return glob
	}
	if strings.HasPrefix(glob, "~/") {
		return filepath.Join(sshUserHome(), glob[2:])
	}
	return filepath.Join(base, glob)
}

// includeBaseDirs 返回相对 Include 的解析基目录（按序）：
// ① ssh 规则基目录（用户配置 ~/.ssh；系统配置 /etc/ssh）——与库一致，cfg.Get
//
//	能钻到这批文件，值优先级正确；
//
// ② 配置文件所在目录——配置不在 ~/.ssh 下时的兜底（任务约定）。这批文件库的
//
//	Get 看不到，值由 EnumerateHosts 的递归 Decode 兜底补齐。
func includeBaseDirs(configPath string) []string {
	primary := filepath.Join(sshUserHome(), ".ssh")
	if strings.HasPrefix(filepath.Clean(configPath), "/etc/ssh") {
		primary = "/etc/ssh"
	}
	bases := []string{primary}
	if dir := filepath.Dir(configPath); filepath.Clean(dir) != filepath.Clean(primary) {
		bases = append(bases, dir)
	}
	return bases
}

// includeGlobs 从一行配置文本提取 Include 指令的 glob 列表；非 Include 行返回
// nil。支持多 glob、双引号、`Include=` 写法与行尾注释（与库 lexer 语义对齐）。
func includeGlobs(line string) []string {
	s := strings.TrimLeft(line, " \t")
	if s == "" || s[0] == '#' {
		return nil
	}
	i := 0
	for i < len(s) && s[i] != ' ' && s[i] != '\t' && s[i] != '=' {
		i++
	}
	if !strings.EqualFold(s[:i], "include") {
		return nil
	}
	rest := strings.TrimLeft(s[i:], " \t")
	if strings.HasPrefix(rest, "=") {
		rest = strings.TrimLeft(rest[1:], " \t")
	}
	var globs []string
	for rest != "" {
		if rest[0] == '#' {
			break
		}
		if rest[0] == '"' {
			end := strings.IndexByte(rest[1:], '"')
			if end < 0 {
				globs = append(globs, rest[1:])
				break
			}
			globs = append(globs, rest[1:1+end])
			rest = rest[2+end:]
		} else {
			j := 0
			for j < len(rest) && rest[j] != ' ' && rest[j] != '\t' && rest[j] != '#' {
				j++
			}
			globs = append(globs, rest[:j])
			rest = rest[j:]
		}
		rest = strings.TrimLeft(rest, " \t")
	}
	return globs
}

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
//
// A3: kevinburke/ssh_config 把 Include 解析成**私有**嵌套 Config，顶层 cfg.Hosts
// 看不到内嵌主机。这里自行预扫描配置文本的 Include 指令（多 glob、~ 展开、相对
// 路径按 ssh 规则 ~/.ssh 或配置所在目录），递归 Decode 被包含文件并枚举其 Hosts
// （深度上限 5 层 + visited 去重防环）。字段值优先顶层 cfg.Get(alias, key)——它本身
// 会钻 Include，首个匹配的优先级正确；仅当库看不到该文件（配置目录兜底解析出的
// 文件）时，用递归 Decode 的 Config 补齐。同一 alias 只出现一次。
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

	src := collectHostSources(configPath, cfg)
	get := func(alias, key string) string {
		if v := cfgGet(cfg, alias, key); v != "" {
			return v
		}
		for _, inc := range src.included {
			if v := cfgGet(inc, alias, key); v != "" {
				return v
			}
		}
		return ""
	}
	var hosts []Host
	for _, alias := range src.aliases {
		host := Host{Alias: alias}
		host.HostName = get(alias, "HostName")
		host.User = get(alias, "User")
		host.IdentityFile = get(alias, "IdentityFile")
		host.ProxyJump = get(alias, "ProxyJump")
		if p := get(alias, "Port"); p != "" {
			host.Port = atoiOrZero(p)
		}
		if host.HostName == "" {
			continue // skip blocks with no resolvable hostname
		}
		hosts = append(hosts, host)
	}
	return hosts, nil
}

// hostSources 汇总枚举结果：aliases 按扫描顺序去重；included 是自行递归 Decode
// 出的被包含文件 Config（供库 Get 看不到时的值兜底）。
type hostSources struct {
	aliases  []string
	included []*ssh_config.Config
}

// collectHostSources 从顶层 Config 出发，预扫描每层配置文本的 Include 指令并
// 递归展开（深度 ≤ maxIncludeDepth，visited 按绝对路径去重防环），收集所有
// 非通配符、非否定的 Host 别名。A4: 每个 Host 块遍历全部 pattern，不只第一个。
func collectHostSources(configPath string, top *ssh_config.Config) hostSources {
	var src hostSources
	seenAlias := map[string]bool{}
	visited := map[string]bool{}
	addPatterns := func(cfg *ssh_config.Config) {
		for _, h := range cfg.Hosts {
			for _, p := range h.Patterns {
				alias := p.String()
				if alias == "" || alias == "*" || containsWildcard(alias) || strings.HasPrefix(alias, "!") {
					continue
				}
				if !seenAlias[alias] {
					seenAlias[alias] = true
					src.aliases = append(src.aliases, alias)
				}
			}
		}
	}
	var walk func(path string, depth int, cfg *ssh_config.Config)
	walk = func(path string, depth int, cfg *ssh_config.Config) {
		if depth > maxIncludeDepth {
			return
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			return
		}
		if visited[abs] {
			return
		}
		visited[abs] = true
		data, err := os.ReadFile(path)
		if err != nil {
			return
		}
		if cfg == nil {
			c, derr := ssh_config.DecodeBytes(data)
			if derr != nil {
				return // 坏文件/超深文件不枚举（与库的降级语义一致）
			}
			cfg = c
			src.included = append(src.included, cfg)
		}
		addPatterns(cfg)
		for _, line := range strings.Split(string(data), "\n") {
			for _, glob := range includeGlobs(line) {
				for _, base := range includeBaseDirs(path) {
					matches, _ := filepath.Glob(resolveIncludeGlob(glob, base))
					for _, m := range matches {
						walk(m, depth+1, nil)
					}
				}
			}
		}
	}
	walk(configPath, 0, top)
	return src
}

// sshGTimeout 限制单次 ssh -G 调用的时间（契约：每 alias ≤5s）。
const sshGTimeout = 5 * time.Second

// defaultSSHGRun 以 5s 超时运行 `ssh -G <alias>` 并返回其 stdout。
// 声明为变量而非函数，便于测试在不依赖真实 ssh 二进制的情况下验证接线。
//
// 这是本包**真实 exec 的那一层**：命令名交给 osutil 的 OpenSSH 解析器
// （windows/386 上 WOW64 会把 System32 重定向到没有 OpenSSH 的 SysWOW64，
// 见 internal/osutil/execpath.go），参数列表逐字不变。
var defaultSSHGRun = func(alias string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), sshGTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, osutil.SSHExecutable(), "-G", alias).Output()
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
