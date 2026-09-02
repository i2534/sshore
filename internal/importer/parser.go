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

// unquote strips ONE pair of surrounding double quotes from a shell-pasted
// token, e.g. `"8080:localhost:80"` from `ssh -L "8080:localhost:80" host`.
func unquote(tok string) string {
	if len(tok) >= 2 && tok[0] == '"' && tok[len(tok)-1] == '"' {
		return tok[1 : len(tok)-1]
	}
	return tok
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
				port, _ = strconv.Atoi(argv[i+1])
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
			t.ListenPort, _ = strconv.Atoi(parts[0])
		} else {
			t.ListenBind = parts[0]
			t.ListenPort, _ = strconv.Atoi(parts[1])
		}
		return t, nil
	}
	switch len(parts) {
	case 3: // port:host:port
		t.ListenPort, _ = strconv.Atoi(parts[0])
		t.TargetHost = parts[1]
		t.TargetPort, _ = strconv.Atoi(parts[2])
	case 4: // bind:port:host:port
		t.ListenBind = parts[0]
		t.ListenPort, _ = strconv.Atoi(parts[1])
		t.TargetHost = parts[2]
		t.TargetPort, _ = strconv.Atoi(parts[3])
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
