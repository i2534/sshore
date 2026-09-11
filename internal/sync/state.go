package sync

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Fingerprint 决定状态文件是否仍然适用于当前规则。
type Fingerprint struct {
	Host       string   `json:"host"`
	User       string   `json:"user"`
	Kind       string   `json:"kind"`
	RemoteRoot string   `json:"remote_root"`
	LocalRoot  string   `json:"local_root"`
	MaxDepth   int      `json:"max_depth"`
	Excludes   []string `json:"excludes"`
}

func (a Fingerprint) equal(b Fingerprint) bool {
	if a.Host != b.Host || a.User != b.User || a.Kind != b.Kind ||
		a.RemoteRoot != b.RemoteRoot || a.LocalRoot != b.LocalRoot || a.MaxDepth != b.MaxDepth {
		return false
	}
	if len(a.Excludes) != len(b.Excludes) {
		return false
	}
	for i := range a.Excludes {
		if a.Excludes[i] != b.Excludes[i] {
			return false
		}
	}
	return true
}

// Conflict 是一条待用户裁决的冲突。定义在这里（而不是 Task 12）是因为状态文件
// 就要序列化它——否则 Task 9/10/11 结束时 go test ./... 会 undefined: Conflict。
// Task 12 只在其上补三个动作。
type Conflict struct {
	RelPath     string `json:"rel_path"`
	RemoteSize  int64  `json:"remote_size"`
	RemoteMTime string `json:"remote_mtime"`
	LocalSize   int64  `json:"local_size"`
	LocalMTime  string `json:"local_mtime"`
	DetectedAt  string `json:"detected_at"`
}

const stateVersion = 1

type FailedItem struct {
	RelPath string `json:"rel_path"`
	Err     string `json:"err"`
	At      string `json:"at"`
	// Action 记录失败时引擎正在执行的动作（ActionGet / ActionSaveAs）。
	// 手动重试必须原样重放用户的 save_as 裁决，而不是重跑 Decide 再推导一次
	// —— 对同一冲突状态 Decide 只会再判一次 Conflict，用户点过的「另存远端版本」
	// 会变成一张冲突卡片（M2c）。
	Action Action `json:"action,omitempty"`
}

type StateFile struct {
	Version     int               `json:"version"`
	Fingerprint Fingerprint       `json:"fingerprint"`
	Entries     map[string]*Entry `json:"entries"`
	Conflicts   []Conflict        `json:"conflicts,omitempty"`
	Failed      []FailedItem      `json:"failed,omitempty"`
}

// StateStore 串行化对状态文件的访问：引擎 goroutine 与 UI 的 ResolveConflict
// 都会改它，必须单锁保护 + 串行 flush。
type StateStore struct {
	mu   sync.Mutex
	path string
	fp   Fingerprint
	data *StateFile
}

func NewStateStore(path string, fp Fingerprint) *StateStore {
	return &StateStore{path: path, fp: fp}
}

func (s *StateStore) Path() string { return s.path }

// Load 读取状态；文件缺失、损坏、或 fingerprint 不匹配时，**静默重建为空状态**
// ——后果被限定为"下次全量重扫"，不影响规则定义本身。
func (s *StateStore) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data = &StateFile{Version: stateVersion, Fingerprint: s.fp, Entries: map[string]*Entry{}}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return nil // 首次运行
	}
	var got StateFile
	if err := json.Unmarshal(raw, &got); err != nil {
		return nil // 损坏 → 重建
	}
	if got.Version != stateVersion || !got.Fingerprint.equal(s.fp) {
		return nil // 规则已变或版本不符 → 重建
	}
	if got.Entries == nil {
		got.Entries = map[string]*Entry{}
	}
	s.data = &got
	return nil
}

// With 在持锁状态下访问状态；**回调里绝不能做 IO/传输**（否则与引擎的
// 传输串行锁形成 ABBA 死锁）。
func (s *StateStore) With(fn func(*StateFile)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data == nil {
		s.data = &StateFile{Version: stateVersion, Fingerprint: s.fp, Entries: map[string]*Entry{}}
	}
	fn(s.data)
}

// Flush 原子落盘（临时文件 + rename），权限 0600、目录 0700，并对内容 fsync。
func (s *StateStore) Flush() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	// **必须在锁内 marshal**：序列化会遍历整张 Entries map，而引擎 goroutine
	// 可能正在 With 里写它 —— 锁外 marshal 会并发读写 map（-race 报错、运行时
	// 可能直接 fatal）。
	s.mu.Lock()
	if s.data == nil {
		s.mu.Unlock()
		return nil
	}
	body, err := json.MarshalIndent(s.data, "", "  ")
	s.mu.Unlock()
	if err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.tmp-%d-%d", s.path, os.Getpid(), time.Now().UnixNano())
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if _, err := f.Write(body); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
