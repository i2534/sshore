package sftp

import (
	"os"
	"strings"
)

// BackendKind 是传输后端的种类。
type BackendKind int

const (
	KindBatch BackendKind = iota // 旧的 sftp -b 实现
	KindGo                       // 新的 pkg/sftp 实现
)

// defaultTransport 是内置默认：Task 16 起为 KindGo（pkg/sftp 底座）。
// 环境变量 SSHORE_SFTP_TRANSPORT 与配置项 [app] sftp_transport 仍可双向覆盖
// （batch 可回退旧后端）。回退一行改动就是把这里换回 KindBatch。
const defaultTransport = KindGo

// TransportSelector 由 app.go 注入，懒解析配置（避免依赖 Init/startup 的先后顺序）。
type TransportSelector func() string

// resolveTransport 的优先级：环境变量 > 配置 > 内置默认。非法 env 值被忽略、继续看配置；
// 非法配置值（已由 config.Normalize 规范化为空）回落内置默认。
func resolveTransport(sel TransportSelector) BackendKind {
	if v := strings.ToLower(strings.TrimSpace(os.Getenv("SSHORE_SFTP_TRANSPORT"))); v != "" {
		switch v {
		case "batch":
			return KindBatch
		case "gosftp":
			return KindGo
		}
	}
	if sel != nil {
		switch strings.ToLower(strings.TrimSpace(sel())) {
		case "batch":
			return KindBatch
		case "gosftp":
			return KindGo
		}
	}
	return defaultTransport
}
