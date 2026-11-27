package forward

import (
	"fmt"
	"net"
	"strconv"

	"sshore/internal/config"
)

// CheckLocalPort reports whether a TCP port on bind is available (bindable).
// 用 net.JoinHostPort 拼地址：IPv6 字面量（如 ::1）必须带方括号，
// 否则 net.Listen 报 "too many colons in address"，空闲端口也恒判占用。
func CheckLocalPort(bind string, port int) bool {
	ln, err := net.Listen("tcp", net.JoinHostPort(bind, strconv.Itoa(port)))
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}

// CheckRemoteConflict dedups remote forward rules by (host, listen_bind, listen_port).
func CheckRemoteConflict(existing []config.Tunnel, candidate config.Tunnel) error {
	for _, e := range existing {
		if e.ID == candidate.ID {
			continue
		}
		if e.Host == candidate.Host && e.ListenBind == candidate.ListenBind && e.ListenPort == candidate.ListenPort {
			return fmt.Errorf("remote forward port %d on %s already used by rule %q",
				candidate.ListenPort, candidate.Host, e.Name)
		}
	}
	return nil
}
