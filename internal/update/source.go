package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// DefaultSource 是内置更新源（GitHub API）。
const DefaultSource = "https://api.github.com/repos/i2534/sshore"

var (
	// ErrRateLimited 表示更新源限流（403 + X-RateLimit-Remaining: 0，或 429）。
	ErrRateLimited = errors.New("更新源限流")
	// ErrCheckFailed 表示检查失败（网络、非 200、坏 JSON 等）。
	ErrCheckFailed = errors.New("检查更新失败")
	// ErrNoAsset 表示该 Release 没有本平台产物。
	ErrNoAsset = errors.New("本平台暂无可用包")
	// ErrNoChecksum 表示该 Release 没有校验文件。
	ErrNoChecksum = errors.New("该版本未提供校验文件")
)

// Doer 让 HTTP 依赖可注入（生产用 *http.Client，测试用 httptest）。
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// Asset 是 Release 里的一个产物。
type Asset struct {
	Name string
	URL  string
}

// Release 是 /releases/latest 的最小投影。
type Release struct {
	Tag         string
	Notes       string
	PublishedAt string
	Assets      []Asset
}

// Client 是更新源客户端。
type Client struct {
	HTTP      Doer
	UserAgent string
}

type latestJSON struct {
	TagName     string `json:"tag_name"`
	Body        string `json:"body"`
	PublishedAt string `json:"published_at"`
	Assets      []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

// Latest 读取 <source>/releases/latest 并投影成 Release。
func (c *Client) Latest(ctx context.Context, source string) (Release, error) {
	if c.HTTP == nil {
		return Release{}, fmt.Errorf("%w: 未配置 HTTP 客户端", ErrCheckFailed)
	}
	if source == "" {
		source = DefaultSource
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(source, "/")+"/releases/latest", nil)
	if err != nil {
		return Release{}, fmt.Errorf("%w: %v", ErrCheckFailed, err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return Release{}, fmt.Errorf("%w: %v", ErrCheckFailed, err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusOK:
	case resp.StatusCode == http.StatusTooManyRequests:
		return Release{}, ErrRateLimited
	case resp.StatusCode == http.StatusForbidden && resp.Header.Get("X-RateLimit-Remaining") == "0":
		return Release{}, ErrRateLimited
	default:
		return Release{}, fmt.Errorf("%w: HTTP %d", ErrCheckFailed, resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Release{}, fmt.Errorf("%w: %v", ErrCheckFailed, err)
	}
	var parsed latestJSON
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return Release{}, fmt.Errorf("%w: %v", ErrCheckFailed, err)
	}
	if parsed.TagName == "" {
		return Release{}, fmt.Errorf("%w: 缺少 tag_name", ErrCheckFailed)
	}
	out := Release{Tag: parsed.TagName, Notes: parsed.Body, PublishedAt: parsed.PublishedAt}
	for _, a := range parsed.Assets {
		if a.Name == "" || a.URL == "" {
			continue
		}
		out.Assets = append(out.Assets, Asset{Name: a.Name, URL: a.URL})
	}
	return out, nil
}
