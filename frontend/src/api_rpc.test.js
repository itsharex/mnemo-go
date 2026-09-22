import { beforeEach, describe, expect, it, vi } from 'vitest'

const bridge = vi.hoisted(() => ({
  GetDirectoryCache: vi.fn(),
  SaveDirectoryCache: vi.fn(),
  DeleteDirectoryCache: vi.fn(),
  ClearCache: vi.fn(),
  ListDir: vi.fn(),
  ListDirPage: vi.fn(),
  ProviderLogin: vi.fn(),
}))

vi.mock('../wailsjs/go/app/App', () => bridge)

import { ClearCache, GetDirectoryCache, SaveDirectoryCache, listDir, listDirSilently, login, prewarmRootDirectories } from './api'

beforeEach(() => {
  bridge.GetDirectoryCache.mockReset()
  bridge.SaveDirectoryCache.mockReset()
  bridge.DeleteDirectoryCache.mockReset()
  bridge.ClearCache.mockReset()
  bridge.ListDir.mockReset()
  bridge.ListDirPage.mockReset()
  bridge.ProviderLogin.mockReset()
})

describe('Wails 缓存 RPC 队列', () => {
  it('前一个调用失败后仍按提交顺序继续执行', async () => {
    const calls = []
    bridge.GetDirectoryCache.mockImplementation(async () => {
      calls.push('get')
      throw new Error('读取失败')
    })
    bridge.SaveDirectoryCache.mockImplementation(async () => {
      calls.push('save')
    })
    bridge.ClearCache.mockImplementation(async () => {
      calls.push('clear')
    })

    const first = GetDirectoryCache('account/root')
    const second = SaveDirectoryCache('account/root', [{ name: 'a.txt' }])
    const third = ClearCache()

    await expect(first).rejects.toThrow('读取失败')
    await second
    await third
    expect(calls).toEqual(['get', 'save', 'clear'])
    expect(bridge.SaveDirectoryCache).toHaveBeenCalledWith('account/root', [{ name: 'a.txt' }])
  })

  it('网盘错误统一发送友好通知并隐藏后端详情', async () => {
    bridge.ListDir.mockRejectedValueOnce(new Error('http 403 AccessDenied: request_id=private'))
    const notices = []
    const listener = event => notices.push(event.detail)
    window.addEventListener('mnemo:drive-notice', listener)
    try {
      await expect(listDir('user', 'drive', 'root')).rejects.toThrow('没有权限完成此操作，请检查账号或文件权限')
      expect(notices).toEqual([expect.objectContaining({ category: 'permission', type: 'warn', message: '没有权限完成此操作，请检查账号或文件权限' })])
      expect(JSON.stringify(notices)).not.toContain('request_id')
    } finally {
      window.removeEventListener('mnemo:drive-notice', listener)
    }
  })

  it('后台目录读取失败不发送全局错误通知', async () => {
    bridge.ListDir.mockRejectedValueOnce(new Error('temporary session refresh'))
    const notices = []
    const listener = event => notices.push(event.detail)
    window.addEventListener('mnemo:drive-notice', listener)
    try {
      await expect(listDirSilently('user', 'drive', 'root')).rejects.toThrow('temporary session refresh')
      expect(notices).toEqual([])
    } finally {
      window.removeEventListener('mnemo:drive-notice', listener)
    }
  })

  it('验证码挑战作为登录续办流程保留，不误报为普通网盘错误', async () => {
    const challenge = new Error('captcha_required\nurl=https://verify.example.test\ntoken=temporary')
    bridge.ProviderLogin.mockRejectedValueOnce(challenge)
    const notices = []
    const listener = event => notices.push(event.detail)
    window.addEventListener('mnemo:drive-notice', listener)
    try {
      await expect(login('pikpak', { username: 'user' })).rejects.toBe(challenge)
      expect(notices).toEqual([])
    } finally {
      window.removeEventListener('mnemo:drive-notice', listener)
    }
  })
})

describe('根目录长期预缓存', () => {
  const account = { user_id: 'webdav:home', drive_id: 'webdav:home' }
  const providers = [{ ID: 'webdav', Meta: { rootKey: '/' } }]

  it('已有持久缓存时不再访问网盘', async () => {
    bridge.GetDirectoryCache.mockResolvedValue([])
    const result = await prewarmRootDirectories([account], providers)
    expect(result).toEqual({ cached: 1, warmed: 0, failed: 0 })
    expect(bridge.ListDirPage).not.toHaveBeenCalled()
    expect(bridge.ListDir).not.toHaveBeenCalled()
  })

  it('缓存缺失时只保存根目录第一页', async () => {
    const files = [{ file_id: 'first', name: '第一页.txt' }]
    bridge.GetDirectoryCache.mockResolvedValueOnce(null).mockResolvedValueOnce(null)
    bridge.ListDirPage.mockResolvedValue({ items: files, nextMarker: 'page-2' })
    const result = await prewarmRootDirectories([account], providers)
    expect(result).toEqual({ cached: 0, warmed: 1, failed: 0 })
    expect(bridge.ListDirPage).toHaveBeenCalledWith(account.user_id, account.drive_id, '/', '')
    expect(bridge.SaveDirectoryCache).toHaveBeenCalledWith('webdav|webdav%3Ahome|webdav%3Ahome|list|%2F|', files)
  })

  it('不支持分页的网盘回退到普通目录接口', async () => {
    const files = [{ file_id: 'only', name: '文件.txt' }]
    bridge.GetDirectoryCache.mockResolvedValueOnce(null).mockResolvedValueOnce(null)
    bridge.ListDirPage.mockRejectedValue(new Error('drive: listPaged not supported by this provider'))
    bridge.ListDir.mockResolvedValue(files)
    const result = await prewarmRootDirectories([account], providers)
    expect(result.warmed).toBe(1)
    expect(bridge.ListDir).toHaveBeenCalledWith(account.user_id, account.drive_id, '/')
  })

  it('前台完整目录先写入时不被预热第一页覆盖', async () => {
    const complete = [{ file_id: 'complete', name: '完整目录.txt' }]
    bridge.GetDirectoryCache.mockResolvedValueOnce(null).mockResolvedValueOnce(complete)
    bridge.ListDirPage.mockResolvedValue({ items: [{ file_id: 'first', name: '第一页.txt' }], nextMarker: 'page-2' })
    const result = await prewarmRootDirectories([account], providers)
    expect(result).toEqual({ cached: 1, warmed: 0, failed: 0 })
    expect(bridge.SaveDirectoryCache).not.toHaveBeenCalled()
  })
})
