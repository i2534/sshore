package sftp

import (
	"context"
	"path"
	"strings"

	"sshore/internal/namepattern"
)

const searchBatch = 64

// SearchHit 是一次深搜的命中项；Path 为相对 root 的路径。
type SearchHit struct {
	Path    string `json:"path"`
	IsDir   bool   `json:"isDir"`
	Size    int64  `json:"size"`
	ModTime string `json:"modTime"`
}

// SearchOutcome 是一次深搜的结果快照。
// 绑定约束：调用方不得在返回非 nil error 时期待前端拿到结果——Wails 的 dispatcher
// 在 err != nil 时不写 Result，所以"取消"必须用 Cancelled 字段 + nil error 表达。
type SearchOutcome struct {
	Hits       []SearchHit `json:"hits"`
	Scanned    int         `json:"scanned"`
	Unreadable int         `json:"unreadable"`
	Truncated  bool        `json:"truncated"`
	Cancelled  bool        `json:"cancelled"`
}

// Search 在 root 下 BFS 深搜；pattern 为空返回全部条目。
// 深度语义与仓库一致：0=仅本层，N=N 层，-1=无限（spec §3 决策 17）。
// ctx 在**批次之间**检查；取消时返回已扫描部分 + ctx.Err()，调用方应转成
// Cancelled=true 的正常返回（见 app.go 的 SftpSearch）。
func (c *BatchBackend) Search(ctx context.Context, host, user, root, pattern string, maxDepth, limit int,
	onProgress func(scanned int)) (SearchOutcome, error) {
	return searchBFS(ctx, c.ListMany, host, user, root, pattern, maxDepth, limit, onProgress)
}

// searchBFS 是两个后端共用的 BFS 主体：lister 只负责"列一批目录"，
// BatchBackend 传 batch.ListMany，GoBackend（Task 13）传自己的 ListMany。
// 返回 map 缺失 key 的语义在此处解释为"不可读"（Unreadable++）。
func searchBFS(ctx context.Context,
	lister func(host, user string, paths []string) (map[string][]Item, error),
	host, user, root, pattern string, maxDepth, limit int,
	onProgress func(scanned int)) (SearchOutcome, error) {
	if limit <= 0 {
		limit = 500
	}
	root = strings.TrimRight(root, "/")
	if root == "" {
		root = "/"
	}
	out := SearchOutcome{Hits: []SearchHit{}}
	type node struct {
		dir   string
		depth int
	}
	queue := []node{{dir: root}}
	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			out.Cancelled = true
			return out, err
		}
		batch := queue
		if len(batch) > searchBatch {
			batch = queue[:searchBatch]
		}
		queue = queue[len(batch):]

		paths := make([]string, 0, len(batch))
		for _, n := range batch {
			paths = append(paths, n.dir)
		}
		res, err := lister(host, user, paths)
		if err != nil {
			return out, err
		}
		for _, n := range batch {
			items, ok := res[n.dir]
			if !ok {
				out.Unreadable++
				continue
			}
			out.Scanned++
			if onProgress != nil {
				onProgress(out.Scanned)
			}
			for _, it := range items {
				rel := it.Name
				if n.depth > 0 {
					rel = strings.TrimPrefix(strings.TrimPrefix(n.dir, root), "/") + "/" + it.Name
				}
				if namepattern.Match(pattern, rel, it.Name) {
					out.Hits = append(out.Hits, SearchHit{Path: rel, IsDir: it.IsDir, Size: it.Size, ModTime: it.ModTime})
					if len(out.Hits) >= limit {
						out.Truncated = true
						return out, nil
					}
				}
				if it.IsDir && (maxDepth < 0 || n.depth < maxDepth) {
					queue = append(queue, node{dir: path.Join(n.dir, it.Name), depth: n.depth + 1})
				}
			}
		}
	}
	return out, nil
}
