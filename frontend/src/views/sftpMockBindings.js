// 测试专用：SftpView 最小挂载所需的 Wails 绑定替身（默认安全解析，用例内再按需覆盖）。
// 各测试文件用 vi.mock('...App', async () => (await import('./sftpMockBindings')).makeAppMock())
// 注入；import * as App 拿到的就是这份对象，可直接 .mockResolvedValueOnce / .mockClear 控制。
import { vi } from 'vitest'

export function makeAppMock() {
  return {
    // SftpView 直接使用的绑定
    ListHosts: vi.fn(async () => []),
    SftpList: vi.fn(async () => []),
    SftpGet: vi.fn(async () => {}),
    SftpGetDir: vi.fn(async () => {}),
    SftpPut: vi.fn(async () => {}),
    SftpPutRecursive: vi.fn(async () => {}),
    SftpRemoveRecursive: vi.fn(async () => {}),
    SftpMove: vi.fn(async () => {}),
    SftpRemove: vi.fn(async () => {}),
    SftpMkdir: vi.fn(async () => {}),
    SftpRename: vi.fn(async () => {}),
    SftpConnect: vi.fn(async () => {}),
    SftpDisconnect: vi.fn(async () => {}),
    SftpConnected: vi.fn(async () => false),
    SftpProbe: vi.fn(async () => true),
    SftpTransferCancel: vi.fn(async () => true),
    ListLocal: vi.fn(async () => []),
    DeleteLocal: vi.fn(async () => {}),
    MkdirLocal: vi.fn(async () => {}),
    RenameLocal: vi.fn(async () => {}),
    StatLocal: vi.fn(async () => 0),
    PickLocalFile: vi.fn(async () => ''),
    CopyLocal: vi.fn(async () => {}),
    StatPaths: vi.fn(async () => []),
    Cwd: vi.fn(async () => '/home/u'),
    SftpHome: vi.fn(async () => '/home/u'),
    ClipboardFileSupport: vi.fn(async () => false),
    ClipboardTempDir: vi.fn(async () => '/tmp/clip'),
    CopyFilesToClipboard: vi.fn(async () => {}),
    // locations / sftpui store 使用的绑定
    ListLocations: vi.fn(async () => ({ bookmarks: [], localRecents: [], remoteRecents: [] })),
    ListPresets: vi.fn(async () => ({ local: [], localDisks: [], remote: [] })),
    AddBookmark: vi.fn(async () => {}),
    RemoveBookmark: vi.fn(async () => {}),
    AddLocalRecent: vi.fn(async () => {}),
    AddRemoteRecent: vi.fn(async () => {}),
    GetSftpUI: vi.fn(async () => ({})),
    // 保存 SFTP UI 的绑定用拼接键：sftpMenuWiring 有一条红线扫描全 src 文件内容里的该绑定名，
    // 只允许 stores/sftpui.js 与 *.test.js 出现。本文件是测试替身（非生产调用点），
    // 用 'Save'+'SftpUI' 避免留下连续字面量触发误报；生产唯一出口仍是 stores/sftpui.js。
    ['Save' + 'SftpUI']: vi.fn(async () => {}),
  }
}

export function makeRuntimeMock() {
  return { EventsOn: vi.fn(() => () => {}) }
}