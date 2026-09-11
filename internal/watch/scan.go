package watch

import (
	"context"
	"path"
	"strings"

	"sshore/internal/sftp"
)

// Meta 是决策前补齐的远端元信息。**唯一来源是 ls 结果**（两条探测路径共用），
// 绝不由 inotify 事件或本地值写入。
type Meta struct {
	Size    int64
	ModTime string
}

// Snapshot 是一轮远端树的视图。Complete=false 表示有目录未知，
// 调用方**绝不能据此产生任何 delete**。
type Snapshot struct {
	Entries  map[string]Meta
	Complete bool
}

type ListManyFunc func(host, user string, paths []string) (map[string][]sftp.Item, error)

const scanBatch = 64

// MatchExclude 判定相对路径是否命中忽略规则。
//
// 以 "/" 结尾的模式表示"该目录名在**任意深度**都忽略"（ruling 1）：
// 前缀匹配、中间段匹配、结尾段匹配都算命中，所以 "a/node_modules/x.js"
// 与 "x/.git/config" 都会被 ".git/"、"node_modules/" 排除，而不只是根层。
// 其余模式按 path.Match 同时匹配 basename 与完整相对路径。
func MatchExclude(rel string, excludes []string) bool {
	base := path.Base(rel)
	for _, pat := range excludes {
		if pat == "" {
			continue
		}
		if strings.HasSuffix(pat, "/") {
			name := strings.TrimSuffix(pat, "/")
			if name == "" {
				continue
			}
			if rel == name || strings.HasPrefix(rel, name+"/") ||
				strings.Contains(rel, "/"+name+"/") || strings.HasSuffix(rel, "/"+name) {
				return true
			}
			// 目录名本身可含 glob（如 build*/）：逐段匹配。
			if strings.ContainsAny(name, "*?[") {
				for _, seg := range strings.Split(rel, "/") {
					if ok, _ := path.Match(name, seg); ok {
						return true
					}
				}
			}
			continue
		}
		if ok, _ := path.Match(pat, base); ok {
			return true
		}
		if ok, _ := path.Match(pat, rel); ok {
			return true
		}
	}
	return false
}

// ScanTree 从 root 开始 BFS 列举，深度受 maxDepth 限制（0=仅本层，-1=无限）。
// 任一目录未知（ListMany 返回的 map 缺 key）都会把 Complete 置为 false，
// 而"存在的 key + 0 项"是真正的空目录，不置 false。
//
// ctx 在**批次之间**被检查（ruling 2）：大扫描可被取消，返回已扫到的部分快照
// 与 ctx.Err()，而不是把整棵树走完。
func ScanTree(ctx context.Context, list ListManyFunc, host, user, root string, maxDepth int, excludes []string) (Snapshot, error) {
	// 归一根路径：尾部 "/" 会让下面的 rel 计算（TrimPrefix）出错，
	// 例如 root="/r/" 时子项会算成 "r/d1/x" 而不是 "d1/x"。
	root = strings.TrimRight(root, "/")
	if root == "" {
		root = "/"
	}
	snap := Snapshot{Entries: map[string]Meta{}, Complete: true}
	type node struct {
		dir   string
		depth int
	}
	queue := []node{{dir: root}}
	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return snap, err
		}
		batch := queue
		if len(batch) > scanBatch {
			batch = queue[:scanBatch]
		}
		queue = queue[len(batch):]

		paths := make([]string, 0, len(batch))
		for _, n := range batch {
			paths = append(paths, n.dir)
		}
		res, err := list(host, user, paths)
		if err != nil {
			return snap, err
		}
		for _, n := range batch {
			items, ok := res[n.dir]
			if !ok {
				snap.Complete = false // 未知，绝不当作空目录
				continue
			}
			for _, it := range items {
				rel := it.Name
				if n.depth > 0 {
					rel = strings.TrimPrefix(strings.TrimPrefix(n.dir, root), "/") + "/" + it.Name
				}
				if MatchExclude(rel, excludes) {
					continue
				}
				if it.IsDir {
					if maxDepth < 0 || n.depth < maxDepth {
						queue = append(queue, node{dir: path.Join(n.dir, it.Name), depth: n.depth + 1})
					}
					continue
				}
				snap.Entries[rel] = Meta{Size: it.Size, ModTime: it.ModTime}
			}
		}
	}
	return snap, nil
}
