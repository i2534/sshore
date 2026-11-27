package importer

import (
	"fmt"
	"strconv"
	"strings"

	"sshore/internal/config"
	"sshore/internal/forward"
)

// Parse tokenizes a pasted `ssh -L/-R/-D ...` command into tunnel rules.
// It reconstructs typed args; it never re-runs the raw string.
func Parse(command string) ([]config.Tunnel, error) {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return nil, fmt.Errorf("empty command")
	}
	if fields[0] == "ssh" {
		fields = fields[1:]
	}
	host := ""
	var argv []string
	i := 0
	for i < len(fields) {
		tok := unquote(fields[i])
		switch {
		case tok == "-N" || tok == "-f":
			i++
		case tok == "-L" || tok == "-R" || tok == "-D":
			if i+1 >= len(fields) {
				return nil, fmt.Errorf("missing value for %s", tok)
			}
			argv = append(argv, tok, unquote(fields[i+1]))
			i += 2
		case tok == "-J":
			if i+1 >= len(fields) {
				return nil, fmt.Errorf("missing value for -J")
			}
			argv = append(argv, tok, unquote(fields[i+1]))
			i += 2
		case tok == "-p":
			if i+1 >= len(fields) {
				return nil, fmt.Errorf("missing value for -p")
			}
			argv = append(argv, tok, unquote(fields[i+1]))
			i += 2
		case tok == "-l":
			if i+1 >= len(fields) {
				return nil, fmt.Errorf("missing value for -l")
			}
			argv = append(argv, tok, unquote(fields[i+1]))
			i += 2
		case strings.HasPrefix(tok, "-"):
			return nil, fmt.Errorf("unsupported/malicious flag %q", tok)
		default:
			host = tok
			i++
		}
	}
	if host == "" {
		return nil, fmt.Errorf("no host given")
	}
	// M8: host 可能以 user@host 形式出现，按最后一个 @ 拆分
	hostUser := ""
	if idx := strings.LastIndex(host, "@"); idx >= 0 {
		hostUser = host[:idx]
		host = host[idx+1:]
	}
	if !forward.ValidateHost(host) {
		return nil, fmt.Errorf("invalid host %q", host)
	}
	return buildTunnels(argv, host, hostUser)
}

// unquote strips ONE pair of surrounding double OR single quotes from a
// shell-pasted token, e.g. `"8080:localhost:80"` / `'8080:localhost:80'`
// from `ssh -L "8080:localhost:80" host`（A2：单引号同样常见，原来只剥双引号）。
func unquote(tok string) string {
	if len(tok) >= 2 {
		if tok[0] == '"' && tok[len(tok)-1] == '"' {
			return tok[1 : len(tok)-1]
		}
		if tok[0] == '\'' && tok[len(tok)-1] == '\'' {
			return tok[1 : len(tok)-1]
		}
	}
	return tok
}

// parsePort 解析端口 token；失败返回含非法 token 的明确错误
// （A2：绝不 `_` 吞错静默产出端口 0 —— 那会让坏规则落盘后连接即失败）。
func parsePort(tok string) (int, error) {
	p, err := strconv.Atoi(tok)
	if err != nil {
		return 0, fmt.Errorf("invalid port %q", tok)
	}
	return p, nil
}

// splitSpec splits a forward spec on colons that are outside square brackets,
// so bracketed IPv6 addresses stay intact: "[::1]:8080:db:80" ->
// ["[::1]", "8080", "db", "80"].
func splitSpec(spec string) []string {
	var parts []string
	start := 0
	inBrackets := false
	for i := 0; i < len(spec); i++ {
		switch spec[i] {
		case '[':
			inBrackets = true
		case ']':
			inBrackets = false
		case ':':
			if !inBrackets {
				parts = append(parts, spec[start:i])
				start = i + 1
			}
		}
	}
	parts = append(parts, spec[start:])
	return parts
}

func buildTunnels(argv []string, host, hostUser string) ([]config.Tunnel, error) {
	proxyJump := ""
	user := ""
	port := 0
	// First scan globals (-J/-p/-l) that apply to all tunnels regardless of order.
	for i := 0; i < len(argv); i += 2 {
		switch argv[i] {
		case "-J":
			if i+1 < len(argv) {
				proxyJump = argv[i+1]
			}
		case "-p":
			if i+1 < len(argv) {
				p, err := parsePort(argv[i+1])
				if err != nil {
					return nil, err
				}
				port = p
			}
		case "-l":
			if i+1 < len(argv) {
				user = argv[i+1]
			}
		}
	}
	// An explicit -l wins over a user@host user (same precedence as ssh itself).
	if user == "" {
		user = hostUser
	}
	var out []config.Tunnel
	for i := 0; i < len(argv); i += 2 {
		if argv[i] == "-L" || argv[i] == "-R" || argv[i] == "-D" {
			t, err := makeTunnel(argv[i], argv[i+1], host, proxyJump, user, port)
			if err != nil {
				return nil, err
			}
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no forward flags found")
	}
	return out, nil
}

func makeTunnel(flag, spec, host, jump, user string, port int) (config.Tunnel, error) {
	mode := ""
	switch flag {
	case "-L":
		mode = "local"
	case "-R":
		mode = "remote"
	case "-D":
		mode = "dynamic"
	}
	t := config.Tunnel{
		ID: config.NewTunnelID(), Mode: mode, Host: host,
		ProxyJump: jump, User: user, Port: port,
		ListenBind:    "127.0.0.1",
		AutoReconnect: true, // 导入规则与手建规则默认一致（前端 newTunnel 同为 true）
	}
	parts := splitSpec(spec)
	if mode == "dynamic" {
		if len(parts) == 1 {
			p, err := parsePort(parts[0])
			if err != nil {
				return t, err
			}
			t.ListenPort = p
		} else {
			t.ListenBind = parts[0]
			p, err := parsePort(parts[1])
			if err != nil {
				return t, err
			}
			t.ListenPort = p
		}
		return t, nil
	}
	switch len(parts) {
	case 3: // port:host:port
		lp, err := parsePort(parts[0])
		if err != nil {
			return t, err
		}
		t.ListenPort = lp
		t.TargetHost = parts[1]
		tp, err := parsePort(parts[2])
		if err != nil {
			return t, err
		}
		t.TargetPort = tp
	case 4: // bind:port:host:port
		t.ListenBind = parts[0]
		lp, err := parsePort(parts[1])
		if err != nil {
			return t, err
		}
		t.ListenPort = lp
		t.TargetHost = parts[2]
		tp, err := parsePort(parts[3])
		if err != nil {
			return t, err
		}
		t.TargetPort = tp
	default:
		return t, fmt.Errorf("malformed %s spec %q", mode, spec)
	}
	// 空目标主机(如 `-L 23080::3080`)是历史坏规则来源:
	// ssh 会解析空主机名失败 → 连接即 RST,而进程不退出 → 界面误报 connected。
	if t.TargetHost == "" {
		return t, fmt.Errorf("malformed %s spec %q: empty target host", mode, spec)
	}
	return t, nil
}
