package importer

import (
	"fmt"
	"strconv"
	"strings"

	"sshkit/internal/config"
	"sshkit/internal/forward"
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
		tok := fields[i]
		switch {
		case tok == "-N" || tok == "-f":
			i++
		case tok == "-L" || tok == "-R" || tok == "-D":
			if i+1 >= len(fields) {
				return nil, fmt.Errorf("missing value for %s", tok)
			}
			argv = append(argv, tok, fields[i+1])
			i += 2
		case tok == "-J":
			if i+1 >= len(fields) {
				return nil, fmt.Errorf("missing value for -J")
			}
			argv = append(argv, tok, fields[i+1])
			i += 2
		case tok == "-p":
			if i+1 >= len(fields) {
				return nil, fmt.Errorf("missing value for -p")
			}
			argv = append(argv, tok, fields[i+1])
			i += 2
		case tok == "-l":
			if i+1 >= len(fields) {
				return nil, fmt.Errorf("missing value for -l")
			}
			argv = append(argv, tok, fields[i+1])
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
	if !forward.ValidateHost(host) {
		return nil, fmt.Errorf("invalid host %q", host)
	}
	return buildTunnels(argv, host)
}

func buildTunnels(argv []string, host string) ([]config.Tunnel, error) {
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
		ListenBind: "127.0.0.1",
	}
	parts := strings.Split(spec, ":")
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
	return t, nil
}
