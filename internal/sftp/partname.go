package sftp

import (
	"crypto/rand"
	"encoding/hex"
	"path/filepath"
	"strings"
)

// PartMarker 是内部临时文件的中缀（唯一来源）。前端有同名同值常量，两边各自单测钉住字面量。
const PartMarker = ".sshore-sftppart-"

// partNameMax 是临时名长度阈值（字节）。超过就退化为目录内短名，避免 ENAMETOOLONG。
const partNameMax = 200

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "000000000000"
	}
	return hex.EncodeToString(b)[:n]
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	if id == "" {
		return "anon"
	}
	return id
}

// PartName 返回 <target> + 中缀 + <短id>-<随机>；过长时退化为同目录短名。
func PartName(target, id string) string {
	name := target + PartMarker + shortID(id) + "-" + randHex(6)
	if len(name) <= partNameMax {
		return name
	}
	return filepath.Join(filepath.Dir(target), ShortPartName(id))
}

// ShortPartName 是退化短名（以点开头，天然隐藏；仍由中缀判定认出）。
func ShortPartName(id string) string {
	return PartMarker + shortID(id) + "-" + randHex(6)
}

// BakName 是 backup-swap 的备份名，复用同一中缀，保证清理/过滤/忽略一套规则覆盖它。
func BakName(target string) string {
	name := target + PartMarker + "bak-" + randHex(6)
	if len(name) <= partNameMax {
		return name
	}
	return filepath.Join(filepath.Dir(target), PartMarker+"bak-"+randHex(6))
}

// IsInternalTemp 用中缀判定：常规名（<name>.sshore-sftppart-…）与退化短名（.sshore-sftppart-…）都命中。
// 不能用 strings.HasPrefix(name, PartMarker)，常规名不以它开头。
func IsInternalTemp(name string) bool {
	return strings.Contains(filepath.Base(name), PartMarker)
}
