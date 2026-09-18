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

// defaultTransport 是内置默认。Task 16 真机验收通过后改成 KindGo。
const defaultTransport = KindBatch

// TransportSelector 由 app.go 注入，懒解析配置（避免依赖 Init/startup 的先后顺序）。
type TransportSelector func() string

// resolveTransport 的优先级：环境变量 > 配置 > 内置默认。非法值一律回落默认。
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
