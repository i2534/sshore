package localfs

import (
	"context"
	"io/fs"
	"path/filepath"
	"strings"

	"sshore/internal/namepattern"
)

// Hit 是一次本地深搜的命中项；Path 为相对 root 的路径。
type Hit struct {
	Path    string `json:"path"`
	IsDir   bool   `json:"isDir"`
	Size    int64  `json:"size"`
	ModTime string `json:"modTime"`
}

// SearchOpts 的深度语义与 internal/config、internal/watch 一致。
type SearchOpts struct {
	MaxDepth int
	Limit    int
}

// Search 用 filepath.WalkDir 搜索 root：不跟随符号链接，不可读目录跳过并计数。
func Search(ctx context.Context, root, pattern string, o SearchOpts) ([]Hit, int, bool, error) {
	if o.Limit <= 0 {
		o.Limit = 500
	}
	root = filepath.Clean(root)
	hits := []Hit{}
	skipped := 0
	truncated := false
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			skipped++
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if p == root {
			return nil
		}
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		depth := strings.Count(rel, "/")
		// add 统一"先判上限再放行"，避免目录分支与文件分支的截断口径不一致
		// （目录分支原来会把命中推到 limit+1）。返回 false ⇒ 已达上限，调用方 SkipAll。
		add := func(h Hit) bool {
			if len(hits) >= o.Limit {
				truncated = true
				return false
			}
			hits = append(hits, h)
			if len(hits) >= o.Limit {
				truncated = true
				return false
			}
			return true
		}
		if d.IsDir() {
			atMaxDepth := o.MaxDepth >= 0 && depth >= o.MaxDepth
			if namepattern.Match(pattern, rel, d.Name()) {
				if !add(Hit{Path: rel, IsDir: true}) {
					return fs.SkipAll
				}
			}
			if atMaxDepth {
				return fs.SkipDir
			}
			return nil
		}
		info, infoErr := d.Info()
		hit := Hit{Path: rel, IsDir: false}
		if infoErr == nil {
			hit.Size = info.Size()
			hit.ModTime = info.ModTime().Format("2006-01-02 15:04")
		}
		if namepattern.Match(pattern, rel, d.Name()) {
			if !add(hit) {
				return fs.SkipAll
			}
		}
		return nil
	})
	if err != nil && ctx.Err() != nil {
		return hits, skipped, truncated, err
	}
	return hits, skipped, truncated, err
}
