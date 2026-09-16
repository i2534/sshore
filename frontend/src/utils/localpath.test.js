import { describe, it, expect } from 'vitest'
import { isWindowsPath, normalize, sepOf, isRoot, join, parentOf, splitPath, joinRel } from './localpath'

describe('localpath · POSIX 回归（本地面板在 Linux/macOS 上的既有行为不能变）', () => {
  it('join / parentOf 维持原语义', () => {
    expect(join('/', 'home')).toBe('/home')
    expect(join('/home/u', 'x')).toBe('/home/u/x')
    expect(join('/home/u/', 'x')).toBe('/home/u/x')
    expect(parentOf('/home/u')).toBe('/home')
    expect(parentOf('/home')).toBe('/')
    expect(parentOf('/')).toBe('/')
    expect(parentOf('')).toBe('/')
  })

  it('根相对路径（深搜命中项恒用 / 分隔）拼接与切分', () => {
    expect(joinRel('/home/u', 'sub/dir')).toBe('/home/u/sub/dir')
    expect(splitPath('sub/a.txt')).toEqual({ dir: 'sub', name: 'a.txt' })
    expect(splitPath('a.txt')).toEqual({ dir: '', name: 'a.txt' })
  })
})

describe('localpath · Windows', () => {
  it('盘根不外溢：上级停在盘根，绝不返回 /', () => {
    expect(parentOf('C:\\')).toBe('C:\\')
    expect(parentOf('C:\\Users')).toBe('C:\\')
    expect(parentOf('C:\\Users\\lan')).toBe('C:\\Users')
    expect(parentOf('D:\\x\\y')).toBe('D:\\x')
    expect(parentOf('D:\\x')).toBe('D:\\')
  })

  it('裸盘符归一为盘根（os.ReadDir("D:") 读的是当前目录，不是根）', () => {
    expect(normalize('D:')).toBe('D:\\')
    expect(parentOf('D:')).toBe('D:\\')
    expect(isRoot('D:')).toBe(true)
  })

  it('分隔符沿用路径自身的风格，不擅自改写', () => {
    expect(join('C:\\', 'Users')).toBe('C:\\Users')
    expect(join('C:/', 'Users')).toBe('C:/Users')
    expect(join('C:\\Users', 'lan')).toBe('C:\\Users\\lan')
    expect(sepOf('C:\\Users')).toBe('\\')
    expect(sepOf('C:/Users')).toBe('/')
    expect(sepOf('/home/u')).toBe('/')
  })

  it('盘根识别与深搜跳转', () => {
    expect(isRoot('C:\\')).toBe(true)
    expect(isRoot('C:\\Users')).toBe(false)
    expect(joinRel('C:\\Users\\lan', 'sub/dir')).toBe('C:\\Users\\lan\\sub\\dir')
    expect(splitPath('C:\\Users\\lan\\a.txt')).toEqual({ dir: 'C:\\Users\\lan', name: 'a.txt' })
    expect(isWindowsPath('C:\\Users')).toBe(true)
    expect(isWindowsPath('C:/Users')).toBe(true)
    expect(isWindowsPath('/home/u')).toBe(false)
  })

  it('UNC 共享根不外溢', () => {
    expect(isRoot('\\\\srv\\share')).toBe(true)
    expect(parentOf('\\\\srv\\share')).toBe('\\\\srv\\share')
    expect(parentOf('\\\\srv\\share\\dir')).toBe('\\\\srv\\share')
  })
})
