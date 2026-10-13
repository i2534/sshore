// Package clipboard 提供"把文件/目录作为文件对象写入系统剪贴板"的能力。
//
// 本文件（hdrops.go）只放纯逻辑，不带构建标签，因此 Linux 上也能被单测覆盖。
package clipboard

import (
	"encoding/binary"
	"unicode/utf16"
)

// dropFilesHeaderSize 是 Win32 DROPFILES 结构体的字节数：
// DWORD pFiles(4) + POINT pt(8) + BOOL fNC(4) + BOOL fWide(4) = 20。
const dropFilesHeaderSize = 20

// buildHDropBytes 构造 CF_HDROP 剪贴板数据块：
// 20 字节 DROPFILES 头 + UTF-16LE 路径列表（每条路径以一个 \0 码元结尾，
// 整个列表再补一个 \0 码元）。资源管理器按 fWide=TRUE 解析该块。
func buildHDropBytes(paths []string) []byte {
	buf := make([]byte, dropFilesHeaderSize)
	binary.LittleEndian.PutUint32(buf[0:4], dropFilesHeaderSize) // pFiles：路径列表相对块首的字节偏移
	binary.LittleEndian.PutUint32(buf[4:8], 0)                   // pt.x
	binary.LittleEndian.PutUint32(buf[8:12], 0)                  // pt.y
	binary.LittleEndian.PutUint32(buf[12:16], 0)                 // fNC = FALSE
	binary.LittleEndian.PutUint32(buf[16:20], 1)                 // fWide = TRUE（UTF-16）

	for _, p := range paths {
		for _, u := range utf16.Encode([]rune(p)) {
			var pair [2]byte
			binary.LittleEndian.PutUint16(pair[:], u)
			buf = append(buf, pair[:]...)
		}
		buf = append(buf, 0, 0) // 每条路径以 \0 码元结尾
	}
	buf = append(buf, 0, 0) // 路径列表结束
	return buf
}
