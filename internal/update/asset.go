package update

import (
	"fmt"
	"net/url"
	"strings"
)

// PickArchive 挑选本平台产物：名字需同时含 goos 与 goarch，并以平台后缀结尾。
func PickArchive(rel Release, goos, goarch string) (Asset, error) {
	suffix := ".tar.gz"
	if goos == "windows" {
		suffix = ".zip"
	}
	for _, a := range rel.Assets {
		if !strings.HasSuffix(a.Name, suffix) {
			continue
		}
		if !strings.Contains(a.Name, goos) || !strings.Contains(a.Name, goarch) {
			continue
		}
		return a, nil
	}
	return Asset{}, fmt.Errorf("%w: 需要 %s-%s 的 %s", ErrNoAsset, goos, goarch, suffix)
}

// PickChecksums 挑选校验文件（按文件名精确匹配）。
func PickChecksums(rel Release) (Asset, error) {
	for _, a := range rel.Assets {
		if a.Name == "checksums.txt" {
			return a, nil
		}
	}
	return Asset{}, ErrNoChecksum
}

// SameOrigin 判断下载 URL 是否在受信主机集合内。
// 默认源是 api.github.com，而资产实际在 github.com / *.githubusercontent.com 上，
// 所以不能做字面 host 相等（spec §10.1）。
func SameOrigin(source, rawURL string) bool {
	su, err := url.Parse(strings.TrimSpace(source))
	if err != nil || su.Host == "" {
		return false
	}
	du, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || du.Host == "" {
		return false
	}
	if du.Scheme != "https" && !(du.Scheme == "http" && isLoopbackHost(du.Hostname())) {
		return false
	}
	if strings.EqualFold(du.Host, su.Host) {
		return true
	}
	if strings.EqualFold(su.Hostname(), "api.github.com") {
		host := strings.ToLower(du.Hostname())
		return host == "github.com" || host == "codeload.github.com" || strings.HasSuffix(host, ".githubusercontent.com")
	}
	return false
}

func isLoopbackHost(h string) bool {
	h = strings.ToLower(h)
	return h == "127.0.0.1" || h == "localhost" || h == "::1"
}
