package update

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"
)

// ParseChecksums 解析 sha256sum 输出（每行「<hash>  <file>」；二进制模式带 * 前缀）。
// 空行忽略；字段不足或哈希非法则整份拒绝 —— 校验文件不可信时宁可失败。
func ParseChecksums(r io.Reader) (map[string]string, error) {
	out := map[string]string{}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 2 {
			return nil, fmt.Errorf("校验文件格式非法: %q", line)
		}
		hash := strings.ToLower(parts[0])
		if len(hash) != 64 {
			return nil, fmt.Errorf("校验文件哈希长度非法: %q", hash)
		}
		name := strings.TrimPrefix(parts[1], "*")
		if name == "" {
			return nil, fmt.Errorf("校验文件缺少文件名: %q", line)
		}
		out[name] = hash
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("校验文件为空")
	}
	return out, nil
}

// FileSHA256 流式计算文件哈希（下载与 sidecar 都用它）。
func FileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// VerifyFile 重算并与 want 比对（大小写不敏感）。
func VerifyFile(path, want string) error {
	got, err := FileSHA256(path)
	if err != nil {
		return err
	}
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("哈希不符：期望 %.12s… 实际 %.12s…", want, got)
	}
	return nil
}
