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
	// RootMissing 表示"根路径本身未知"（depth==0 那一批里 ListMany 缺了根的 key），
	// 与"某个子目录未知"区分开：单文件规则据此把"远端源文件缺失"识别出来，
	// 而不是当成连续扫描失败。
	RootMissing bool
}

type ListManyFunc func(host, user string, paths []string) (map[string][]sftp.Item, error)

const scanBatch = 64

// IsInternalTemp 判定 sshore 自己的临时文件；sync 的 align/ScanTree 与面板都不该把它
// 当业务文件。与 internal/sftp.PartMarker 同字面量；两边各自单测钉住（D18）。
// 用中缀（Contains）而不是前缀：常规名是 <name>.sshore-sftppart-…，退化短名才是
// .sshore-sftppart-… 开头；两种都要命中，且备份名复用同一中缀 ⇒ 一条规则全覆盖。
func IsInternalTemp(rel string) bool {
	return strings.Contains(path.Base(rel), ".sshore-sftppart-")
}

// MatchExclude 判定相对路径是否命中忽略规则。
//
// 以 "/" 结尾的模式表示"该目录名在**任意深度**都忽略"（ruling 1）：
// 前缀匹配、中间段匹配、结尾段匹配都算命中，所以 "a/node_modules/x.js"
// 与 "x/.git/config" 都会被 ".git/"、"node_modules/" 排除，而不只是根层。
// 其余模式按 path.Match 同时匹配 basename 与完整相对路径。
func MatchExclude(rel string, excludes []string) bool {
	// D18 内置忽略：sshore 自己的传输临时文件（中缀 .sshore-sftppart-）永远不是业务文件，
	// 与用户配置的 excludes 无关 —— 用户删改规则也不会让 .part/.bak 泄漏进面板或同步清单。
	// 这也是中缀判定与「一套规则覆盖备份」的设计用意（见 internal/sftp.PartMarker）。
	if IsInternalTemp(rel) {
		return true
	}
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
				if n.depth == 0 {
					snap.RootMissing = true
				}
				continue
			}
			for _, it := range items {
				// 本层（n.depth==0）的 key 一律取 basename：真实 sftp 对**目录根**
				// 返回 basename，但对**文件根**（kind=file 的单个远端文件）
				// sftp ls -l <file> 会把完整远端路径当作 Item.Name 返回。若照抄，
				// 事件 rel 就成了绝对路径，单文件规则会被 inScope 丢弃或被
				// SafeRelPath 拒绝，运行中的远端变更永远收不到。文件名不含 "/"，
				// 所以对目录根取 basename 是恒等的无损操作。
				rel := path.Base(it.Name)
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
