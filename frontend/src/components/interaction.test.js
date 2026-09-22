import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { nextTick } from 'vue'
import Modal from './Modal.vue'
import UiSelect from './UiSelect.vue'
import ContextMenu from './ContextMenu.vue'
import TreeNode from './TreeNode.vue'
import AccountRail from './AccountRail.vue'
import PreviewModal from './PreviewModal.vue'
import PlayerPanel from './PlayerPanel.vue'
import ShareView from '../views/ShareView.vue'
import SyncView from '../views/SyncView.vue'
import SettingsView from '../views/SettingsView.vue'
import UpdateModal from './UpdateModal.vue'
import * as appearance from '../appearance'
import App from '../App.vue'
import WorkspaceView from '../views/WorkspaceView.vue'
import PanView from '../views/PanView.vue'

const api = vi.hoisted(() => ({
  listDir: vi.fn().mockResolvedValue([]),
  listDirSilently: vi.fn((...args) => api.listDir(...args)),
  GetDirectoryCache: vi.fn().mockResolvedValue(null),
  SaveDirectoryCache: vi.fn().mockResolvedValue(undefined),
  ListFavorites: vi.fn().mockResolvedValue([]),
  AddFavorite: vi.fn().mockResolvedValue(undefined),
  RemoveFavorite: vi.fn().mockResolvedValue(undefined),
  RestoreFavorite: vi.fn().mockResolvedValue(undefined),
  favorite: vi.fn().mockResolvedValue([]),
  onFileDrop: vi.fn(() => () => {}),
  formatTimeParts: vi.fn(() => ({ date: '', clock: '' })),
  CheckUpdate: vi.fn(),
  GetUpdateStatus: vi.fn(),
  DownloadUpdate: vi.fn(),
  CancelUpdate: vi.fn(),
  ApplyUpdate: vi.fn(),
  listAccounts: vi.fn(),
  listProviders: vi.fn(),
  prewarmRootDirectories: vi.fn().mockResolvedValue({ cached: 0, warmed: 0, failed: 0 }),
  setAccountCustomMeta: vi.fn(),
  login: vi.fn(),
  ListShareHistory: vi.fn(),
  ListSyncConfigs: vi.fn(),
  GetSettings: vi.fn(),
  GetDownloadDirectory: vi.fn().mockResolvedValue('D:/系统下载'),
  OpenDownloadDirectory: vi.fn().mockResolvedValue(undefined),
  SaveSettings: vi.fn(),
  GetLogPath: vi.fn(),
  ClearCache: vi.fn(),
  RevealInFolder: vi.fn(),
  ClearLogs: vi.fn(),
  ExportLogs: vi.fn(),
  ListRunningSyncIDs: vi.fn(),
  SaveSyncConfig: vi.fn(),
  DeleteSyncConfig: vi.fn(),
  RunSync: vi.fn(),
  CancelSync: vi.fn(),
  PickDirectory: vi.fn(),
  capsOf: vi.fn(),
  importShare: vi.fn(),
  saveImportedShare: vi.fn(),
  cancelShare: vi.fn(),
  saveMounted: vi.fn(),
  validateMountedWrite: vi.fn(),
  SendGuangyaSms: vi.fn(),
  SendPan139SMS: vi.fn(),
  SendPan189SMS: vi.fn(),
  providerIconUrl: vi.fn(() => ''),
  OpenBrowser: vi.fn(),
  onEvent: vi.fn(() => () => {}),
  ClosePikPakCaptcha: vi.fn(),
  ShowPikPakCaptcha: vi.fn(),
  refreshAccountSilently: vi.fn(),
  refreshAccountNow: vi.fn(),
  accountName: vi.fn((account) => account?.user_id || ''),
  providerMetaOf: vi.fn(() => ({ key: 'webdav', label: 'WebDAV' })),
  providerOf: vi.fn((id) => String(id).split(':')[0]),
  formatBytes: vi.fn((value) => `${value} B`),
  PreviewURL: vi.fn(),
  PinFileSnapshot: vi.fn().mockResolvedValue(undefined),
  openKindOf: vi.fn(file => file.name.endsWith('.wav') ? 'audio' : 'image'),
  formatTime: vi.fn(() => ''),
  saveCloudText: vi.fn(),
  copyText: vi.fn(),
  iconOf: vi.fn(() => 'image'),
  getPlayCursor: vi.fn().mockResolvedValue(0),
  savePlayCursor: vi.fn().mockResolvedValue(undefined),
  playVideo: vi.fn(),
  playVideoQuality: vi.fn(),
  pinFileSnapshot: vi.fn(),
  getSettings: vi.fn(),
  previewUrl: vi.fn(),
  download: vi.fn(),
  migrateFiles: vi.fn().mockResolvedValue({ id: 'migration-test', status: 'pending' }),
  move: vi.fn().mockResolvedValue(['folder', 'file']),
  copy: vi.fn().mockResolvedValue(['folder', 'file']),
  DeleteDirectoryCache: vi.fn().mockResolvedValue(undefined),
}))

const tsMock = vi.hoisted(() => ({ createPlayer: vi.fn(), isSupported: vi.fn(() => true), Events: { ERROR: 'error' }, ErrorTypes: { NETWORK_ERROR: 'network' } }))
vi.mock('mpegts.js', () => ({ default: tsMock }))

vi.mock('../api', () => api)
vi.mock('../logger', () => ({
  debug: vi.fn(),
  info: vi.fn(),
  warn: vi.fn(),
  error: vi.fn(),
  errorText: vi.fn((value) => String(value)),
  configKeys: vi.fn(() => []),
  installGlobalErrorLogging: vi.fn(() => () => {}),
}))

import LoginModal from './LoginModal.vue'
import AccountAvatar from './AccountAvatar.vue'

const storage = new Map()
Object.defineProperty(globalThis, 'localStorage', {
  configurable: true,
  value: {
    getItem: (key) => storage.has(key) ? storage.get(key) : null,
    setItem: (key, value) => storage.set(String(key), String(value)),
    removeItem: (key) => storage.delete(String(key)),
    clear: () => storage.clear(),
  },
})

const wrappers = []
beforeEach(() => {
  api.ListFavorites.mockReset().mockResolvedValue([])
  api.AddFavorite.mockReset().mockResolvedValue(undefined)
  api.RemoveFavorite.mockReset().mockResolvedValue(undefined)
  api.openKindOf.mockImplementation(file => file.name.endsWith('.wav') ? 'audio' : 'image')
  vi.spyOn(HTMLMediaElement.prototype, 'pause').mockImplementation(() => {})
  vi.spyOn(HTMLMediaElement.prototype, 'load').mockImplementation(() => {})
})

describe('网盘收藏', () => {
  const account = { user_id: 'pikpak_favorites', drive_id: 'drive' }
  const file = { file_id: 'file', name: 'photo.jpg', isDir: false, size: 42, parent_file_id: 'parent', category: 'image' }
  async function prepare() {
    api.capsOf.mockReturnValue({ favorite: true })
    api.listDir.mockResolvedValue([file])
    const wrapper = mountAttached(PanView, { props: { account } })
    await flushPromises()
    await wrapper.get('.fileitem').trigger('contextmenu', { clientX: 10, clientY: 10 })
    await nextTick()
    return wrapper
  }
  it('添加收藏只调用统一入口，并保存完整文件信息', async () => {
    const wrapper = await prepare()
    Array.from(document.querySelectorAll('[role="menuitem"]')).find(item => item.textContent.includes('加入收藏')).click()
    await flushPromises()
    expect(api.AddFavorite).toHaveBeenCalledExactlyOnceWith(account.user_id, account.drive_id, expect.objectContaining({ file_id: file.file_id, file: expect.objectContaining({ size: 42, parent_file_id: 'parent' }) }))
    expect(api.favorite).not.toHaveBeenCalled()
  })
  it('收藏失败显示错误并重新获取已经部分成功的列表', async () => {
    api.AddFavorite.mockRejectedValue(new Error('云端收藏失败'))
    const wrapper = await prepare()
    const before = api.ListFavorites.mock.calls.length
    Array.from(document.querySelectorAll('[role="menuitem"]')).find(item => item.textContent.includes('加入收藏')).click()
    await flushPromises()
    expect(wrapper.emitted('toast')).toContainEqual([expect.stringContaining('云端收藏失败'), 'error'])
    expect(wrapper.emitted('toast').some(event => event[1] === 'success')).toBe(false)
    expect(api.ListFavorites.mock.calls.length).toBeGreaterThan(before)
  })
  it('收藏列表保留大小和预览信息', async () => {
    api.ListFavorites.mockResolvedValue([{ ...file, file, source: 'cloud', added: 10 }])
    const wrapper = await prepare()
    await wrapper.findAll('.tree-node').find(node => node.text().startsWith('收藏')).trigger('click')
    await flushPromises()
    expect(wrapper.get('.fileitem').text()).toContain('42 B')
  })
  it('取消收藏只调用统一入口', async () => {
    api.ListFavorites.mockResolvedValue([{ ...file, file, source: 'cloud' }])
    const wrapper = await prepare()
    Array.from(document.querySelectorAll('[role="menuitem"]')).find(item => item.textContent.includes('移出收藏')).click()
    await flushPromises()
    expect(api.RemoveFavorite).toHaveBeenCalledExactlyOnceWith(account.user_id, account.drive_id, 'file')
    expect(api.favorite).not.toHaveBeenCalled()
    expect(wrapper.emitted('toast')).toContainEqual(['已移出收藏', 'success'])
  })
  it('快速刷新不会让较早返回的旧收藏覆盖新收藏', async () => {
    let first, second
    api.ListFavorites.mockImplementationOnce(() => new Promise(resolve => { first = resolve }))
      .mockImplementationOnce(() => new Promise(resolve => { second = resolve }))
    const wrapper = await prepare()
    await wrapper.findAll('.tree-node').find(node => node.text().startsWith('收藏')).trigger('click')
    second([{ file_id: 'new', name: '新收藏.jpg', isDir: false, source: 'cloud' }])
    await flushPromises()
    first([{ file_id: 'old', name: '旧收藏.jpg', isDir: false, source: 'cloud' }])
    await flushPromises()
    expect(wrapper.text()).toContain('新收藏.jpg')
    expect(wrapper.text()).not.toContain('旧收藏.jpg')
  })
  it('批量收藏期间切换账号仍只操作原账号，部分失败显示进度', async () => {
    let finishFirst
    api.AddFavorite.mockImplementationOnce(() => new Promise(resolve => { finishFirst = resolve }))
      .mockRejectedValueOnce(new Error('第二项失败'))
    api.capsOf.mockReturnValue({})
    api.listDir.mockResolvedValue([file, { ...file, file_id: 'second', name: 'second.jpg' }])
    const wrapper = mountAttached(PanView, { props: { account } })
    await flushPromises()
    const rows = wrapper.findAll('.fileitem')
    await rows[0].trigger('click')
    await rows[1].trigger('click', { ctrlKey: true })
    await rows[0].trigger('contextmenu', { clientX: 10, clientY: 10 })
    Array.from(document.querySelectorAll('[role="menuitem"]')).find(item => item.textContent.includes('加入收藏')).click()
    await flushPromises()
    await wrapper.setProps({ account: { user_id: 'webdav:other', drive_id: 'other' } })
    finishFirst()
    await flushPromises()
    expect(api.AddFavorite).toHaveBeenCalledTimes(2)
    expect(api.AddFavorite.mock.calls.every(([user, drive]) => user === account.user_id && drive === account.drive_id)).toBe(true)
    expect(wrapper.emitted('toast')).toContainEqual([expect.stringContaining('已完成 1/2 项'), 'error'])
  })
})

function mountAttached(component, options = {}) {
  const wrapper = mount(component, { attachTo: document.body, ...options })
  wrappers.push(wrapper)
  return wrapper
}

async function setDomInput(input, value) {
  input.value = value
  input.dispatchEvent(new Event('input', { bubbles: true }))
  await nextTick()
}

afterEach(async () => {
  for (const wrapper of wrappers.splice(0)) wrapper.unmount()
  await nextTick()
  document.body.replaceChildren()
  localStorage.clear()
  vi.useRealTimers()
  vi.clearAllMocks()
  vi.restoreAllMocks()
})

describe('关键交互组件', () => {
  it('不限量账号在侧栏显示容量说明而不是零容量进度条', () => {
    const wrapper = mountAttached(AccountRail, { props: { accounts: [{ user_id: 'lanzou:quota', usage: { type: 'unlimited', size: 0, status: 'available' } }] } })
    expect(wrapper.text()).toContain('总空间不限量')
    expect(wrapper.find('.rail-quota').exists()).toBe(false)
  })
  it('账号栏收起时仍为当前账号保留明确选中标记', () => {
    const current = { user_id: 'pikpak:current' }
    const wrapper = mountAttached(AccountRail, {
      props: { accounts: [current, { user_id: 'pikpak:other' }], current },
    })
    const items = wrapper.findAll('.rail-item')
    expect(wrapper.get('.account-rail').classes()).not.toContain('expanded')
    expect(items[0].classes()).toContain('active')
    expect(items[0].attributes('data-selected')).toBe('true')
    expect(items[1].attributes('data-selected')).toBe('false')
  })
  it('账号上下切换时仅为新选中账号标记进入方向', async () => {
    const accounts = [{ user_id: 'pikpak:top' }, { user_id: 'pikpak:bottom' }]
    const wrapper = mountAttached(AccountRail, { props: { accounts, current: accounts[0] } })
    await wrapper.setProps({ current: accounts[1] })
    const items = wrapper.findAll('.rail-item')
    expect(items[1].classes()).toContain('active-enter-down')
    expect(items[1].attributes('data-switch-direction')).toBe('down')
    await wrapper.setProps({ current: accounts[0] })
    expect(items[0].classes()).toContain('active-enter-up')
    expect(items[0].attributes('data-switch-direction')).toBe('up')
  })
  it('展开账号栏后仍可正常切换账号', async () => {
    vi.useFakeTimers()
    const accounts = [{ user_id: 'pikpak:one' }, { user_id: 'pikpak:two' }]
    const wrapper = mountAttached(AccountRail, { props: { accounts, current: accounts[0] } })
    await wrapper.get('.account-rail').trigger('mouseenter')
    await vi.advanceTimersByTimeAsync(250)
    await wrapper.findAll('.rail-item')[1].trigger('click')
    expect(wrapper.emitted('select')?.[0]).toEqual([accounts[1]])
  })
  it('系统默认下载目录显示实际路径，可直接打开并用于文件夹选择器', async () => {
    api.GetSettings.mockResolvedValue({ downloadDir: '' })
    api.GetLogPath.mockResolvedValue('')
    api.GetDownloadDirectory.mockResolvedValue('D:/系统下载')
    api.PickDirectory.mockResolvedValue('')
    const wrapper = mountAttached(SettingsView)
    await flushPromises()
    expect(wrapper.get('#sg-transfer input.input').attributes('placeholder')).toBe('D:/系统下载')
    const open = wrapper.get('#sg-transfer').findAll('button').find(button => button.text() === '打开')
    expect(open).toBeTruthy()
    await open.trigger('click')
    await flushPromises()
    expect(api.OpenDownloadDirectory).toHaveBeenCalledOnce()
    await wrapper.get('#sg-transfer').findAll('button').find(button => button.text() === '选择').trigger('click')
    expect(api.PickDirectory).toHaveBeenCalledWith('选择下载文件夹', 'D:/系统下载')
  })

  it('设置页合并相关分类，并使用可访问的开关按钮', async () => {
    api.GetSettings.mockResolvedValue({})
    api.GetLogPath.mockResolvedValue('')
    api.GetDownloadDirectory.mockResolvedValue('D:/系统下载')
    const wrapper = mountAttached(SettingsView)
    await flushPromises()
    expect(wrapper.findAll('.settings-nav .sn-item')).toHaveLength(6)
    expect(wrapper.get('#sg-maintenance').text()).toContain('日志等级')
    expect(wrapper.find('#sg-network').exists()).toBe(false)
    const switches = wrapper.findAll('button.switch')
    expect(switches.length).toBeGreaterThan(0)
    expect(switches.every(item => item.attributes('role') === 'switch')).toBe(true)
  })

  it('恢复默认下载位置保存空配置，并更新当前生效路径', async () => {
    api.GetSettings.mockResolvedValue({ downloadDir: 'E:/自定义下载' })
    api.GetLogPath.mockResolvedValue('')
    api.GetDownloadDirectory.mockResolvedValueOnce('E:/自定义下载').mockResolvedValue('D:/系统下载')
    api.SaveSettings.mockResolvedValue(undefined)
    const wrapper = mountAttached(SettingsView)
    await flushPromises()
    const reset = wrapper.get('#sg-transfer').findAll('button').find(button => button.text() === '恢复默认')
    expect(reset).toBeTruthy()
    await reset.trigger('click')
    await flushPromises()
    expect(api.SaveSettings).toHaveBeenLastCalledWith(expect.objectContaining({ downloadDir: '' }))
    expect(wrapper.get('#sg-transfer input.input').element.value).toBe('')
    expect(wrapper.get('#sg-transfer input.input').attributes('placeholder')).toBe('D:/系统下载')
  })
  it('下一级目录预缓存限制数量和并发，展开树使用缓存且不递归扫描', async () => {
    vi.useFakeTimers()
    const account = { user_id: 'pan189:prefetch', drive_id: 'prefetch' }
    const folders = Array.from({ length: 10 }, (_, i) => ({ file_id: `folder-${i}`, name: `Folder ${i}`, isDir: true }))
    const pending = []
    api.listDir.mockImplementation(async (_user, _drive, dir) => {
      if (dir === 'root') return folders
      return new Promise(resolve => pending.push({ dir, resolve }))
    })
    api.capsOf.mockReturnValue({})
    const wrapper = mountAttached(PanView, { props: { account, accounts: [account], providers: [] } })
    await flushPromises()
    expect(api.listDir).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(181)
    expect(pending).toHaveLength(2)
    for (let batch = 0; batch < 3; batch++) {
      for (const request of pending.splice(0)) request.resolve([{ file_id: `${request.dir}-child`, name: 'Child', isDir: true }])
      await flushPromises()
      expect(pending.length).toBeLessThanOrEqual(2)
    }
    expect(api.listDir).toHaveBeenCalledTimes(7)
    await wrapper.findAll('.tree-node').find(node => node.text() === 'Folder 0').get('.tn-arrow').trigger('click')
    await flushPromises()
    expect(wrapper.findAll('.tree-node').map(node => node.text())).toContain('Child')
    await vi.advanceTimersByTimeAsync(1000)
    expect(api.listDir).toHaveBeenCalledTimes(7)
  })

  it('快速切换账号丢弃旧响应，列表网格切换和空目录更新不会破坏 DOM', async () => {
    const accounts = [{ user_id: 'pan189:slow', drive_id: 'slow' }, { user_id: 'webdav:fast', drive_id: 'fast' }]
    let slow
    api.listDir.mockImplementation(user => user === accounts[0].user_id ? new Promise(resolve => { slow = resolve }) : Promise.resolve([{ file_id: 'fast', name: '新账号.txt', isDir: false }]))
    api.capsOf.mockReturnValue({})
    const wrapper = mountAttached(WorkspaceView, { props: { account: accounts[0], accounts, providers: [], dual: false } })
    await flushPromises()
    await wrapper.setProps({ account: accounts[1] })
    await flushPromises()
    slow([{ file_id: 'slow', name: '旧账号.txt', isDir: false }])
    await flushPromises()
    expect(wrapper.text()).toContain('新账号.txt')
    expect(wrapper.text()).not.toContain('旧账号.txt')
    for (let i = 0; i < 3; i++) {
      await wrapper.get('[title="网格视图"]').trigger('click')
      expect(wrapper.findAll('.griditem')).toHaveLength(1)
      await wrapper.get('[title="列表视图"]').trigger('click')
      expect(wrapper.findAll('.fileitem')).toHaveLength(1)
    }
    api.listDir.mockResolvedValue([])
    await wrapper.vm.refresh()
    await flushPromises()
    expect(wrapper.findAll('.fileitem')).toHaveLength(0)
    expect(wrapper.text()).toContain('空目录')
  })

  it('目录缓存先展示再后台更新，写缓存失败不影响目录显示', async () => {
    const account = { user_id: 'webdav:cache', drive_id: 'cache' }
    let finish
    api.GetDirectoryCache.mockResolvedValue([{ file_id: 'cached', name: '缓存.txt', isDir: false }])
    api.SaveDirectoryCache.mockRejectedValue(new Error('cache unavailable'))
    api.listDir.mockImplementation(() => new Promise(resolve => { finish = resolve }))
    api.capsOf.mockReturnValue({})
    try {
      const wrapper = mountAttached(PanView, { props: { account } })
      await flushPromises()
      expect(wrapper.text()).toContain('缓存.txt')
      finish([{ file_id: 'fresh', name: '最新.txt', isDir: false }])
      await flushPromises()
      expect(wrapper.text()).toContain('最新.txt')
      expect(wrapper.text()).not.toContain('缓存.txt')
    } finally {
      api.GetDirectoryCache.mockResolvedValue(null)
      api.SaveDirectoryCache.mockResolvedValue(undefined)
    }
  })

  it('账号切走再切回后，旧目录树响应不能覆盖新一轮展开结果', async () => {
    vi.useFakeTimers()
    const accounts = [{ user_id: 'pan189:tree-race', drive_id: 'tree-race' }, { user_id: 'webdav:tree-other', drive_id: 'other' }]
    let finishOld
    let childCalls = 0
    api.capsOf.mockReturnValue({})
    api.listDir.mockImplementation(async (user, _drive, dir) => {
      if (user !== accounts[0].user_id) return []
      if (dir === 'root') return [{ file_id: 'folder', name: 'Folder', isDir: true }]
      if (++childCalls === 1) return new Promise(resolve => { finishOld = resolve })
      return [{ file_id: 'fresh-child', name: 'Fresh child', isDir: true }]
    })
    const wrapper = mountAttached(PanView, { props: { account: accounts[0] } })
    await flushPromises()
    const expandFolder = () => wrapper.findAll('.tree-node').find(node => node.text() === 'Folder').get('.tn-arrow').trigger('click')
    await expandFolder()
    await flushPromises()
    await wrapper.setProps({ account: accounts[1] })
    await flushPromises()
    await wrapper.setProps({ account: accounts[0] })
    await flushPromises()
    await expandFolder()
    await flushPromises()
    expect(wrapper.text()).toContain('Fresh child')
    finishOld([{ file_id: 'old-child', name: 'Old child', isDir: true }])
    await flushPromises()
    expect(wrapper.text()).toContain('Fresh child')
    expect(wrapper.text()).not.toContain('Old child')
  })

  it.each(['webdav:drop', 'pan189:drop'])('双栏拖动选中的文件和文件夹到 %s 账号目录会提交移动迁移', async targetUser => {
    const accounts = [{ user_id: 'pan189:drag', drive_id: 'source' }, { user_id: targetUser, drive_id: 'target' }]
    api.listDir.mockImplementation(async user => user === accounts[0].user_id
      ? [{ file_id: 'folder', name: '文件夹', isDir: true }, { file_id: 'file', name: '文档.txt', isDir: false }]
      : [{ file_id: 'destination', name: '目标文件夹', isDir: true }])
    api.capsOf.mockReturnValue({ download: true, upload: true, move: true, copy: true })
    const wrapper = mountAttached(WorkspaceView, { props: { account: accounts[0], accounts, providers: [], dual: true } })
    await flushPromises()
    const panes = wrapper.findAll('.workspace-pane')
    await panes[0].findAll('.fileitem')[0].trigger('click')
    await panes[0].findAll('.fileitem')[1].trigger('click', { ctrlKey: true })
    const dataTransfer = { setData: vi.fn(), types: [], effectAllowed: '', dropEffect: '', files: [] }
    await panes[0].findAll('.fileitem')[0].trigger('dragstart', { dataTransfer })
    await panes[1].get('.fileitem').trigger('dragover', { dataTransfer })
    expect(dataTransfer.dropEffect).toBe('move')
    await panes[1].get('.fileitem').trigger('drop', { dataTransfer })
    await flushPromises()
    expect(api.migrateFiles).toHaveBeenCalledWith('pan189:drag', 'source', targetUser, 'target', 'destination', ['folder', 'file'], true)
    expect(api.move).not.toHaveBeenCalled()
    expect(wrapper.text()).not.toContain('松开鼠标移动')
  })

  it('同账号跨栏拖动走盘内移动，目标是自身目录时阻止提交', async () => {
    const account = { user_id: 'pan189:local-drag', drive_id: 'local-drag' }
    api.listDir.mockImplementation(async (_user, _drive, dir) => dir === 'root' ? [{ file_id: 'folder', name: '来源目录', isDir: true }] : [])
    api.capsOf.mockReturnValue({ move: true, copy: true })
    api.move.mockResolvedValueOnce(['folder'])
    const wrapper = mountAttached(WorkspaceView, { props: { account, accounts: [account], providers: [], dual: true } })
    await flushPromises()
    const views = wrapper.findAllComponents(PanView)
    const dataTransfer = { setData: vi.fn(), types: [], files: [] }
    await views[1].vm.navigate({ dirId: 'folder', name: '来源目录' })
    await flushPromises()
    await views[0].get('.fileitem').trigger('dragstart', { dataTransfer })
    await views[1].get('.pan-right').trigger('drop', { dataTransfer })
    await flushPromises()
    expect(api.move).not.toHaveBeenCalled()
    expect(wrapper.emitted('toast').at(-1)[0]).toContain('自身')
    await views[1].vm.navigate({ dirId: 'destination', name: '目标目录' })
    await flushPromises()
    await views[0].get('.fileitem').trigger('dragstart', { dataTransfer })
    await views[1].get('.pan-right').trigger('drop', { dataTransfer })
    await flushPromises()
    expect(api.move).toHaveBeenCalledWith(account.user_id, account.drive_id, ['folder'], 'destination')
    expect(api.migrateFiles).not.toHaveBeenCalled()
    expect(api.DeleteDirectoryCache.mock.calls.some(([key]) => key.includes('destination'))).toBe(true)
  })

  it('迁移结束会刷新两栏，拖动期间切换账号会取消原拖动', async () => {
    const handlers = new Map()
    api.onEvent.mockImplementation((name, handler) => { handlers.set(name, handler); return () => handlers.delete(name) })
    const accounts = [{ user_id: 'pan189:events', drive_id: 'source' }, { user_id: 'webdav:events', drive_id: 'target' }]
    api.listDir.mockResolvedValue([{ file_id: 'file', name: '原文件.txt', isDir: false }])
    api.capsOf.mockReturnValue({ download: true, upload: true })
    try {
      const wrapper = mountAttached(WorkspaceView, { props: { account: accounts[0], accounts, providers: [], dual: true } })
      await flushPromises()
      const dataTransfer = { setData: vi.fn(), types: [], files: [] }
      await wrapper.findAll('.workspace-pane')[0].get('.fileitem').trigger('dragstart', { dataTransfer })
      await wrapper.setProps({ account: accounts[1] })
      await flushPromises()
      await wrapper.findAll('.workspace-pane')[1].get('.pan-right').trigger('drop', { dataTransfer })
      await flushPromises()
      expect(api.migrateFiles).not.toHaveBeenCalled()
      await wrapper.setProps({ account: accounts[0] })
      await flushPromises()
      api.listDir.mockResolvedValue([{ file_id: 'updated', name: '迁移后.txt', isDir: false }])
      handlers.get('migrate:progress')({ id: 'done', srcUser: accounts[0].user_id, srcDrive: 'source', dstUser: accounts[1].user_id, dstDrive: 'target', dstParent: 'root', status: 'completed' })
      await flushPromises()
      expect(wrapper.findAll('.fileitem .filename').map(row => row.text())).toEqual(['迁移后.txt', '迁移后.txt'])
    } finally { api.onEvent.mockImplementation(() => () => {}) }
  })

  it('忽略空白或重复 ID 的旧目录缓存，数字 ID 字符串的文件夹独立选中', async () => {
    const account = { user_id: 'pan189:ids', drive_id: 'ids' }
    let finish
    api.listDir.mockImplementation(() => new Promise(resolve => { finish = resolve }))
    api.GetDirectoryCache.mockResolvedValue([{ file_id: '', name: '旧目录A', isDir: true }, { file_id: '', name: '旧目录B', isDir: true }])
    api.capsOf.mockReturnValue({})
    try {
      const wrapper = mountAttached(WorkspaceView, { props: { account, accounts: [account], providers: [], dual: false } })
      await flushPromises()
      expect(wrapper.findAll('.fileitem')).toHaveLength(0)
      finish([{ file_id: '9007199254740992', name: 'A', isDir: true }, { file_id: '9007199254740993', name: 'B', isDir: true }])
      await flushPromises()
      await wrapper.findAll('.fileitem')[0].trigger('click')
      expect(wrapper.findAll('.fileitem.selected')).toHaveLength(1)
      await wrapper.findAll('.fileitem')[1].trigger('click', { ctrlKey: true })
      expect(wrapper.findAll('.fileitem.selected')).toHaveLength(2)
      await wrapper.findAll('.fileitem')[0].trigger('click', { ctrlKey: true })
      expect(wrapper.findAll('.fileitem.selected')).toHaveLength(1)
      expect(wrapper.get('.fileitem.selected').text()).toContain('B')
    } finally { api.GetDirectoryCache.mockResolvedValue(null) }
  })

  it('工作区真实列表在单栏、双栏和账号切换后显示返回文件', async () => {
    const accounts = [{ user_id: 'webdav:one', drive_id: 'one' }, { user_id: 'webdav:two', drive_id: 'two' }]
    api.listDir.mockImplementation(async user => [{ file_id: user + '/file', name: user + '.txt', size: 1, isDir: false }])
    api.capsOf.mockReturnValue({})
    const wrapper = mountAttached(WorkspaceView, { props: { account: accounts[0], accounts, providers: [], dual: false } })
    await flushPromises()
    expect(wrapper.findAll('.fileitem')).toHaveLength(1)
    expect(wrapper.text()).toContain('webdav:one.txt')
    await wrapper.get('.search-quick').setValue('不存在的文件')
    await new Promise(resolve => setTimeout(resolve, 140))
    expect(wrapper.findAll('.fileitem')).toHaveLength(0)
    await wrapper.setProps({ dual: true })
    await flushPromises()
    expect(wrapper.findAll('.fileitem')).toHaveLength(1) // 左栏筛选不影响右栏
    await wrapper.setProps({ dual: false, account: accounts[1] })
    await flushPromises()
    expect(wrapper.findAll('.fileitem')).toHaveLength(1)
    expect(wrapper.text()).toContain('webdav:two.txt')
    expect(wrapper.get('.search-quick').element.value).toBe('')
  })

  it('重新点击当前账号会主动刷新左侧真实文件列表', async () => {
    vi.stubGlobal('matchMedia', vi.fn(() => ({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() })))
    const account = { user_id: 'webdav:reload', drive_id: 'drive' }
    const other = { user_id: 'webdav:other', drive_id: 'other' }
    api.listAccounts.mockResolvedValue([account, other])
    api.listProviders.mockResolvedValue([])
    api.GetSettings.mockResolvedValue({ theme: 'light', autoUpdate: false })
    api.capsOf.mockReturnValue({})
    api.listDir.mockResolvedValue([])
    const wrapper = mountAttached(App)
    await flushPromises()
    api.listDir.mockResolvedValue([{ file_id: 'new', name: '重新加载的文件.txt', size: 1, isDir: false }])
    const calls = api.listDir.mock.calls.length
    await wrapper.get('.rail-item').trigger('click')
    await flushPromises()
    expect(api.listDir.mock.calls.length).toBeGreaterThan(calls)
    expect(wrapper.text()).toContain('重新加载的文件.txt')
    api.listDir.mockResolvedValue([{ file_id: 'other-file', name: '另一个账号的文件.txt', size: 1, isDir: false }])
    await wrapper.findAll('.rail-item')[1].trigger('click')
    await flushPromises()
    expect(api.listDir).toHaveBeenLastCalledWith(other.user_id, other.drive_id, 'root')
    expect(wrapper.text()).toContain('另一个账号的文件.txt')
    expect(wrapper.text()).not.toContain('重新加载的文件.txt')
    vi.unstubAllGlobals()
  })
  it('账号登录失效后提示本地清理，但不自动打开重新登录窗口', async () => {
    vi.stubGlobal('matchMedia', vi.fn(() => ({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() })))
    const handlers = new Map()
    api.onEvent.mockImplementation((name, fn) => { handlers.set(name, fn); return () => handlers.delete(name) })
    api.listAccounts.mockResolvedValue([{ user_id: 'dropbox_expired', drive_id: 'dropbox:expired' }])
    api.listProviders.mockResolvedValue([
      { ID: 'pikpak', Meta: { label: 'PikPak' }, Login: { fields: [] } },
      { ID: 'dropbox', Meta: { label: 'Dropbox' }, Login: { type: 'oauth', fields: [] } },
    ])
    api.GetSettings.mockResolvedValue({ theme: 'light', autoUpdate: false })
    api.listDir.mockResolvedValue([])
    const wrapper = mountAttached(App, { global: { stubs: { PanView: true, AccountAvatar: true } } })
    await flushPromises()

    handlers.get('account:expired')({ userId: 'dropbox_expired', provider: 'dropbox', accountName: '工作盘' })
    await flushPromises()

    expect(document.body.textContent).toContain('Dropbox')
    expect(document.body.textContent).toContain('已从本机移除')
    expect(document.body.textContent).toContain('云端文件不会被删除')
    expect(wrapper.findComponent(LoginModal).exists()).toBe(false)
    vi.unstubAllGlobals()
  })
  it('手动检查无更新时明确显示当前版本，不自动关闭', async () => {
    api.GetUpdateStatus.mockResolvedValue({ revision: 0, phase: 'idle', info: { currentVersion: '0.3.0' } })
    api.CheckUpdate.mockResolvedValue({ available: false, currentVersion: '0.3.0' })
    api.getSettings.mockResolvedValue({})
    const wrapper = mountAttached(UpdateModal)
    await flushPromises()
    expect(document.body.textContent).toContain('已是最新正式版')
    expect(document.body.textContent).toContain('0.3.0')
    expect(wrapper.emitted('close')).toBeUndefined()
  })

  it('重新打开更新窗口恢复下载状态，忽略过期事件，并能取消下载', async () => {
    const handlers = new Map()
    api.onEvent.mockImplementation((name, fn) => { handlers.set(name, fn); return () => handlers.delete(name) })
    const state = { revision: 4, phase: 'downloading', downloaded: 50, total: 100, info: { available: true, version: 'v0.3.0', canInstall: true } }
    api.GetUpdateStatus.mockResolvedValue(state)
    api.getSettings.mockResolvedValue({})
    api.CancelUpdate.mockResolvedValue(true)
    try {
      mountAttached(UpdateModal)
      await flushPromises()
      expect(document.body.textContent).toContain('50%')
      expect(api.CheckUpdate).not.toHaveBeenCalled()
      handlers.get('update:state')({ ...state, revision: 3, phase: 'done' })
      await nextTick()
      expect(document.body.textContent).toContain('正在下载更新')
      api.GetUpdateStatus.mockResolvedValue({ ...state, revision: 5, phase: 'canceled' })
      ;[...document.querySelectorAll('.modal button')].find(b => b.textContent === '取消下载').click()
      await flushPromises()
      expect(api.CancelUpdate).toHaveBeenCalledOnce()
      expect(document.body.textContent).toContain('下载已取消')
      expect(api.ApplyUpdate).not.toHaveBeenCalled()
    } finally { api.onEvent.mockImplementation(() => () => {}) }
  })

  it('安装启动失败后可重试已验证安装包，无需重新下载', async () => {
    const status = { revision: 2, phase: 'done', path: 'C:/updates/setup.exe', info: { available: true, canInstall: true } }
    api.GetUpdateStatus.mockResolvedValue(status)
    api.getSettings.mockResolvedValue({ confirmUpdate: false })
    api.ApplyUpdate.mockRejectedValueOnce(new Error('用户取消了系统授权'))
    mountAttached(UpdateModal)
    await flushPromises()
    ;[...document.querySelectorAll('.modal button')].find(b => b.textContent === '安装并重启').click()
    await flushPromises()
    const retry = [...document.querySelectorAll('.modal button')].find(b => b.textContent === '重试安装')
    expect(retry).toBeTruthy()
    api.ApplyUpdate.mockResolvedValueOnce(undefined)
    retry.click()
    await flushPromises()
    expect(api.ApplyUpdate).toHaveBeenLastCalledWith(status.path)
    expect(api.DownloadUpdate).not.toHaveBeenCalled()
  })

  it('非 Windows 更新显示手动安装入口，发布说明不执行 HTML', async () => {
    api.GetUpdateStatus.mockResolvedValue({ revision: 2, phase: 'done', path: '/tmp/update.tar.gz', info: { currentVersion: '0.3.0', canInstall: false, notes: '<img src=x onerror=alert(1)>' } })
    api.getSettings.mockResolvedValue({})
    api.RevealInFolder.mockResolvedValue(undefined)
    mountAttached(UpdateModal)
    await flushPromises()
    expect(document.body.textContent).toContain('手动安装')
    expect(document.body.textContent).not.toContain('安装并重启')
    expect(document.querySelector('.upd-notes img')).toBeNull()
    ;[...document.querySelectorAll('.modal button')].find(b => b.textContent === '打开目录').click()
    await flushPromises()
    expect(api.RevealInFolder).toHaveBeenCalledWith('/tmp/update.tar.gz')
  })

  it('Markdown 代码块保留原始代码并转义 HTML', async () => {
    api.openKindOf.mockReturnValue('text')
    api.PinFileSnapshot.mockResolvedValue(undefined)
    api.PreviewURL.mockResolvedValue('http://127.0.0.1/document')
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: true, arrayBuffer: async () => new TextEncoder().encode('```html\n<img src=x onerror=alert(1)>\n```').buffer }))
    try {
      mountAttached(PreviewModal, { props: { file: { file_id: 'md', name: 'readme.md' }, account: { user_id: 'webdav:one', drive_id: 'drive' } } })
      await flushPromises()
      expect(document.querySelector('.md-code-block pre code')).not.toBeNull()
      expect(document.querySelector('.md-code-block pre code').textContent).toBe('<img src=x onerror=alert(1)>')
      expect(document.querySelector('.pv-markdown-view img')).toBeNull()
    } finally { vi.unstubAllGlobals(); api.openKindOf.mockImplementation(file => file.name.endsWith('.wav') ? 'audio' : 'image') }
  })
  it('账号右键可打开图标名称编辑页，保存后更新所有页面使用的账号', async () => {
    vi.stubGlobal('matchMedia', vi.fn(() => ({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() })))
    api.listAccounts.mockResolvedValue([{ user_id: 'webdav:one', drive_id: 'drive' }])
    api.listProviders.mockResolvedValue([{ ID: 'webdav', Meta: { label: 'WebDAV' } }])
    api.GetSettings.mockResolvedValue({ theme: 'light', autoUpdate: false })
    api.setAccountCustomMeta.mockResolvedValue(undefined)
    const wrapper = mountAttached(App, { global: { stubs: { PanView: true, AccountAvatar: true } } })
    await flushPromises()
    expect(wrapper.find('.workspace-controls').exists()).toBe(false)
    expect(wrapper.findAll('header button').every(button => button.text().trim() === '')).toBe(true)
    await wrapper.get('button[aria-label="切换双栏"]').trigger('click')
    expect(wrapper.get('button[aria-label="切换单栏"]').attributes('aria-pressed')).toBe('true')
    expect(wrapper.findAll('.workspace-pane')).toHaveLength(2)
    await wrapper.get('button[aria-label="切换单栏"]').trigger('click')
    expect(wrapper.findAll('.workspace-pane')).toHaveLength(1)
    expect(wrapper.find('.workspace-controls').exists()).toBe(false)
    await wrapper.get('.rail-item').trigger('contextmenu', { clientX: 50, clientY: 60 })
    await flushPromises()
    const action = [...document.querySelectorAll('.ctx-item')].find(button => button.textContent.includes('自定义'))
    expect(action).toBeTruthy()
    action.click()
    await flushPromises()
    expect(document.body.textContent).toContain('自定义')
    expect(document.querySelector('.preset-icons-grid')).not.toBeNull()
    await setDomInput(document.querySelector('input[placeholder="留空则使用默认账号名称"]'), '我的资料库')
    document.querySelector('.preset-icon-chip[title="坚果云"]').click()
    await nextTick()
    const save = [...document.querySelectorAll('.modal button')].find(button => button.textContent.trim() === '保存')
    save.click()
    await flushPromises()
    expect(api.setAccountCustomMeta).toHaveBeenCalledWith('webdav:one', '我的资料库', 'jianguoyun.svg')
    expect(wrapper.findComponent({ name: 'PanView' }).props('accounts')[0]).toMatchObject({ custom_name: '我的资料库', custom_icon: 'jianguoyun.svg' })
    expect(document.querySelector('.custom-acc-form')).toBeNull()
    vi.unstubAllGlobals()
  })

  it('账号排序偏好改变后侧栏即时更新，新账号保留在末尾', async () => {
    const accounts = [{ user_id: 'webdav:one' }, { user_id: 'webdav:two' }, { user_id: 'webdav:new' }]
    const wrapper = mountAttached(AccountRail, { props: { accounts } })
    appearance.setPref('accountOrder', ['webdav:two', 'deleted', 'webdav:two', 'webdav:one'])
    await nextTick()
    const items = wrapper.findAll('.rail-item')
    expect(items.map(item => item.attributes('aria-label'))).toEqual(['webdav · webdav:two', 'webdav · webdav:one', 'webdav · webdav:new'])
  })

  it('焦点在其他输入框时方向键不会展开下拉框', async () => {
    mountAttached(UiSelect, { props: { options: [{ value: 'a', label: 'A' }] } })
    const input = document.createElement('input')
    document.body.appendChild(input)
    input.focus()
    input.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true }))
    await nextTick()
    expect(document.querySelector('.uiselect-drop')).toBeNull()
  })
  it('鼠标切换账号后移出侧栏，即使按钮仍有焦点也能收起', async () => {
    vi.useFakeTimers()
    const wrapper = mountAttached(AccountRail, { props: { accounts: [{ user_id: 'pikpak:one' }] } })
    await wrapper.get('.account-rail').trigger('mouseenter')
    await vi.advanceTimersByTimeAsync(250)
    const item = wrapper.get('.rail-item')
    await item.trigger('pointerdown', { button: 0 })
    item.element.focus()
    window.dispatchEvent(new MouseEvent('pointerup'))
    await item.trigger('click')
    await wrapper.get('.account-rail').trigger('mouseleave')
    await vi.advanceTimersByTimeAsync(250)
    expect(wrapper.get('.account-rail').classes()).not.toContain('expanded')
    expect(wrapper.emitted('select')).toHaveLength(1)
  })

  it('没有兼容账号时分享页仍展示导入入口和说明', async () => {
    api.ListShareHistory.mockResolvedValue([])
    const wrapper = mountAttached(ShareView)
    await flushPromises()
    expect(wrapper.text()).toContain('还没有分享记录')
    const entry = wrapper.findAll('button').find(button => button.text().includes('导入分享'))
    expect(entry).toBeTruthy()
    await entry.trigger('click')
    await flushPromises()
    expect(document.body.textContent).toContain('请先添加支持分享导入的网盘账号')
  })
  it('OLED 背景默认关闭，可即时切换并在重新打开设置后保留', async () => {
    api.GetSettings.mockResolvedValue({ theme: 'dark' })
    api.GetLogPath.mockResolvedValue('')
    appearance.applyAppearance('dark')
    const wrapper = mountAttached(SettingsView)
    await flushPromises()
    const toggle = wrapper.get('[role="switch"][aria-labelledby="oled-background-label"]')
    expect(toggle.attributes('aria-checked')).toBe('false')
    await toggle.trigger('click')
    expect(appearance.getPrefs().oledBackground).toBe(true)
    expect(document.documentElement.matches('html.dark.oled')).toBe(true)
    appearance.applyAppearance('light')
    expect(document.documentElement.matches('html.dark.oled')).toBe(false)
    appearance.applyAppearance('dark')
    expect(document.documentElement.matches('html.dark.oled')).toBe(true)
    const reopened = mountAttached(SettingsView)
    await flushPromises()
    const restored = reopened.get('[role="switch"][aria-labelledby="oled-background-label"]')
    expect(restored.attributes('aria-checked')).toBe('true')
    await restored.trigger('click')
    expect(appearance.getPrefs().oledBackground).toBe(false)
    expect(document.documentElement.classList.contains('oled')).toBe(false)
    document.documentElement.classList.remove('dark')
  })
  it('设置读取失败时禁止保存默认值，重试成功后恢复保存', async () => {
    api.GetSettings.mockRejectedValueOnce(new Error('读取失败')).mockResolvedValue({ proxy: 'http://localhost:7890' })
    api.GetLogPath.mockResolvedValue('')
    api.SaveSettings.mockResolvedValue(undefined)
    const wrapper = mountAttached(SettingsView)
    await flushPromises()
    await wrapper.vm.save(true)
    expect(api.SaveSettings).not.toHaveBeenCalled()
    expect(document.body.textContent).toContain('重试')
    await wrapper.vm.loadSettings()
    await wrapper.vm.save(true)
    expect(api.SaveSettings).toHaveBeenCalledWith(expect.objectContaining({ proxy: 'http://localhost:7890' }))
  })
  it('日志路径读取失败不重置已加载的设置', async () => {
    api.GetSettings.mockResolvedValue({ proxy: 'http://localhost:7890', maxConcurrentDownloads: 7, maxDownloadSpeed: 2048 })
    api.GetLogPath.mockRejectedValue(new Error('日志路径不可用'))
    const wrapper = mountAttached(SettingsView)
    await flushPromises()
    expect(wrapper.vm.settings.proxy).toBe('http://localhost:7890')
    expect(wrapper.vm.settings.maxConcurrentDownloads).toBe(7)
    expect(wrapper.vm.settings.maxDownloadSpeed).toBe(2)
  })
  it('旧同步任务保存完成后不关闭新打开的编辑表单', async () => {
    api.ListSyncConfigs.mockResolvedValue([])
    api.ListRunningSyncIDs.mockResolvedValue([])
    let finishSave
    api.SaveSyncConfig.mockImplementation(() => new Promise(resolve => { finishSave = resolve }))
    const wrapper = mountAttached(SyncView, { props: { accounts: [{ user_id: 'user', drive_id: 'drive' }] } })
    wrapper.vm.openEdit({ id: 'first', name: '旧任务', user_id: 'user', drive_id: 'drive', local_dir: 'D:/first' })
    const saving = wrapper.vm.save()
    wrapper.vm.showEdit = false
    wrapper.vm.openEdit({ id: 'second', name: '新任务', user_id: 'user', drive_id: 'drive', local_dir: 'D:/second' })
    finishSave()
    await saving
    expect(wrapper.vm.showEdit).toBe(true)
    expect(wrapper.vm.form.name).toBe('新任务')
  })
  it('同步初始化的旧运行列表不能清除刚启动的任务', async () => {
    api.ListSyncConfigs.mockResolvedValue([])
    let resolveInitial, resolveRun
    api.ListRunningSyncIDs.mockImplementation(() => new Promise(resolve => { resolveInitial = resolve }))
    api.RunSync.mockImplementation(() => new Promise(resolve => { resolveRun = resolve }))
    const wrapper = mountAttached(SyncView)
    const task = wrapper.vm.run({ id: 'sync-new', name: '任务' })
    resolveInitial([])
    await flushPromises()
    expect(wrapper.vm.running.has('sync-new')).toBe(true)
    resolveRun()
    await task
    expect(wrapper.vm.running.has('sync-new')).toBe(false)
  })
  it('同步任务刷新乱序时保留最新列表', async () => {
    const pending = []
    api.ListSyncConfigs.mockImplementation(() => new Promise(resolve => pending.push(resolve)))
    api.ListRunningSyncIDs.mockResolvedValue([])
    const wrapper = mountAttached(SyncView)
    wrapper.vm.refresh()
    pending[1]([{ id: 'new', name: '新任务' }])
    await flushPromises()
    pending[0]([{ id: 'old', name: '旧任务' }])
    await flushPromises()
    expect(wrapper.vm.jobs.map(job => job.id)).toEqual(['new'])
  })
  it('分享解析期间目标账号被移除时丢弃旧会话', async () => {
    api.ListShareHistory.mockResolvedValue([])
    api.capsOf.mockReturnValue({ importShare: true })
    let resolveImport
    api.importShare.mockImplementation(() => new Promise(resolve => { resolveImport = resolve }))
    const first = { user_id: 'first', drive_id: 'drive' }
    const second = { user_id: 'second', drive_id: 'drive' }
    const wrapper = mountAttached(ShareView, { props: { accounts: [first, second] } })
    wrapper.vm.openImport()
    wrapper.vm.importForm.url = 'https://example.test/share'
    const parsing = wrapper.vm.parseImport()
    await wrapper.setProps({ accounts: [second] })
    resolveImport({ files: [{ fileId: 'old-file' }] })
    await parsing
    expect(wrapper.vm.importSession).toBeNull()
    expect(wrapper.vm.importStep).toBe('form')
    expect(wrapper.vm.importBusy).toBe(false)
  })
  it('音频播放结束时清除旧的续播位置', async () => {
    api.PinFileSnapshot.mockResolvedValue(undefined)
    api.getPlayCursor.mockResolvedValue(45)
    api.savePlayCursor.mockResolvedValue(undefined)
    api.PreviewURL.mockResolvedValue('https://example.test/song.wav')
    const wrapper = mountAttached(PreviewModal, { props: { account: { user_id: 'test', drive_id: 'drive' }, file: { file_id: 'song', name: 'song.wav' } } })
    await flushPromises()
    const audio = document.querySelector('audio')
    audio.currentTime = 120
    wrapper.vm.audioDur = 120
    audio.dispatchEvent(new Event('ended'))
    await flushPromises()
    expect(api.savePlayCursor).toHaveBeenCalledWith('test', 'drive', 'song', 0)
  })
  it('视频切换账号时保存旧账号进度并重新获取播放地址', async () => {
    vi.spyOn(HTMLMediaElement.prototype, 'load').mockImplementation(() => {})
    vi.spyOn(HTMLMediaElement.prototype, 'pause').mockImplementation(() => {})
    api.pinFileSnapshot.mockResolvedValue(undefined)
    api.getSettings.mockResolvedValue({ playbackResume: false })
    api.savePlayCursor.mockResolvedValue(undefined)
    api.playVideo.mockImplementation(async user => ({ url: `https://example.test/${user}.mp4`, stream_type: 'mp4', qualities: [] }))
    const wrapper = mountAttached(PlayerPanel, { props: { account: { user_id: 'first', drive_id: 'drive' }, file: { file_id: 'video', name: 'video.mp4' } } })
    await flushPromises()
    document.querySelector('video').currentTime = 42
    await wrapper.setProps({ account: { user_id: 'second', drive_id: 'drive' } })
    await flushPromises()
    expect(api.savePlayCursor).toHaveBeenCalledWith('first', 'drive', 'video', 42)
    expect(api.playVideo).toHaveBeenLastCalledWith('second', 'drive', 'video')
    expect(wrapper.vm.src).toBe('https://example.test/second.mp4')
  })
  it('连续切换视频清晰度时只采用最后一次选择', async () => {
    vi.spyOn(HTMLMediaElement.prototype, 'load').mockImplementation(() => {})
    vi.spyOn(HTMLMediaElement.prototype, 'pause').mockImplementation(() => {})
    api.pinFileSnapshot.mockResolvedValue(undefined)
    api.getSettings.mockResolvedValue({ playbackResume: false })
    api.playVideo.mockResolvedValue({ url: 'https://example.test/original.mp4', stream_type: 'mp4', qualities: [] })
    const pending = []
    api.playVideoQuality.mockImplementation(() => new Promise(resolve => pending.push(resolve)))
    const wrapper = mountAttached(PlayerPanel, { props: { account: { user_id: 'test', drive_id: 'test' }, file: { file_id: 'video', name: 'video.mp4' } } })
    await flushPromises()
    const first = wrapper.vm.switchQuality('720p')
    const last = wrapper.vm.switchQuality('1080p')
    pending[1]({ url: 'https://example.test/1080.mp4', stream_type: 'mp4', qualities: [] })
    await last
    pending[0]({ url: 'https://example.test/720.mp4', stream_type: 'mp4', qualities: [] })
    await first
    expect(wrapper.vm.src).toBe('https://example.test/1080.mp4')
  })
  it('不同账号使用相同文件 ID 时重新加载预览', async () => {
    api.PinFileSnapshot.mockResolvedValue(undefined)
    api.getPlayCursor.mockResolvedValue(0)
    api.savePlayCursor.mockResolvedValue(undefined)
    api.PreviewURL.mockImplementation(async (user) => `https://example.test/${user}.wav`)
    const wrapper = mountAttached(PreviewModal, { props: { account: { user_id: 'first', drive_id: 'drive' }, file: { file_id: '/song.wav', name: 'song.wav' } } })
    await flushPromises()
    expect(wrapper.vm.url).toBe('https://example.test/first.wav')
    await wrapper.setProps({ account: { user_id: 'second', drive_id: 'drive' } })
    await flushPromises()
    expect(api.PreviewURL).toHaveBeenLastCalledWith('second', 'drive', '/song.wav')
    expect(wrapper.vm.url).toBe('https://example.test/second.wav')
  })
  it('切换文本文件后旧响应不得覆盖新内容，关闭时取消读取', async () => {
    vi.spyOn(api, 'openKindOf').mockReturnValue('text')
    api.PreviewURL.mockImplementation(async (_user, _drive, id) => `https://example.test/${id}`)
    const pending = []
    vi.spyOn(globalThis, 'fetch').mockImplementation((url, options) => Promise.resolve({
      ok: true,
      arrayBuffer: () => new Promise(resolve => pending.push({ url, options, resolve })),
    }))
    const wrapper = mountAttached(PreviewModal, { props: { account: { user_id: 'test', drive_id: 'test' }, file: { file_id: 'old', name: 'old.txt' } } })
    await flushPromises()
    await wrapper.setProps({ file: { file_id: 'new', name: 'new.txt' } })
    await flushPromises()
    pending[1].resolve(new TextEncoder().encode('new content').buffer)
    await flushPromises()
    pending[0].resolve(new TextEncoder().encode('old content').buffer)
    await flushPromises()
    expect(wrapper.vm.text).toBe('new content')
    expect(pending[0].options.signal.aborted).toBe(true)
    wrapper.unmount()
    expect(pending[1].options.signal.aborted).toBe(true)
  })
  it('文本工具栏实际显示，修改后请求关闭先确认', async () => {
    vi.spyOn(api, 'openKindOf').mockReturnValue('text')
    api.PinFileSnapshot.mockResolvedValue(undefined)
    api.PreviewURL.mockResolvedValue('http://127.0.0.1/document')
    vi.spyOn(globalThis, 'fetch').mockResolvedValue({ ok: true, arrayBuffer: async () => new TextEncoder().encode('hello').buffer })
    const wrapper = mountAttached(PreviewModal, { props: { account: { user_id: 'test', drive_id: 'test' }, file: { file_id: 'text', name: 'test.txt' } } })
    await flushPromises()
    const toolbar = document.querySelector('.pv-toolbar')
    expect(toolbar.querySelector('template')).toBeNull()
    expect(toolbar.textContent).toContain('在线编辑')
    ;[...toolbar.querySelectorAll('button')].find(b => b.textContent.includes('在线编辑')).click()
    await nextTick()
    await setDomInput(document.querySelector('textarea'), 'changed')
    wrapper.vm.requestClose()
    await nextTick()
    expect(wrapper.emitted('close')).toBeUndefined()
    expect(document.body.textContent).toContain('未保存')
  })
  it('窗口播放和暂停时闲置都会隐藏上下控制区，移动鼠标恢复', async () => {
    vi.spyOn(HTMLMediaElement.prototype, 'load').mockImplementation(() => {})
    vi.spyOn(HTMLMediaElement.prototype, 'pause').mockImplementation(() => {})
    api.pinFileSnapshot.mockResolvedValue(undefined)
    api.getSettings.mockResolvedValue({ playbackResume: false })
    api.playVideo.mockResolvedValue({ url: 'http://127.0.0.1/video.mp4', stream_type: 'mp4' })
    mountAttached(PlayerPanel, { props: { account: { user_id: 'test', drive_id: 'test' }, file: { file_id: 'video', name: 'video.mp4' } } })
    await flushPromises()
    vi.useFakeTimers()
    try {
      const panel = document.querySelector('.player-panel')
      const video = document.querySelector('video')
      for (const event of ['play', 'pause']) {
        video.dispatchEvent(new Event(event))
        await vi.advanceTimersByTimeAsync(2700)
        expect(document.querySelector('.pp-topbar').classList.contains('hidden')).toBe(true)
        expect(document.querySelector('.pp-bottom').classList.contains('hidden')).toBe(true)
        panel.dispatchEvent(new MouseEvent('mousemove', { bubbles: true }))
        await nextTick()
        expect(document.querySelector('.pp-topbar').classList.contains('hidden')).toBe(false)
        expect(document.querySelector('.pp-bottom').classList.contains('hidden')).toBe(false)
      }
    } finally { vi.useRealTimers() }
  })
  it('TS 转码流跳转会重新定位、取消旧请求并在关闭时释放播放器', async () => {
    vi.spyOn(HTMLMediaElement.prototype, 'load').mockImplementation(() => {})
    vi.spyOn(HTMLMediaElement.prototype, 'pause').mockImplementation(() => {})
    vi.spyOn(HTMLMediaElement.prototype, 'play').mockResolvedValue(undefined)
    api.pinFileSnapshot.mockResolvedValue(undefined)
    api.getSettings.mockResolvedValue({ playbackResume: false })
    api.playVideo.mockResolvedValue({ url: 'http://127.0.0.1/stream/test', stream_type: 'ts', duration: 120, qualities: [] })
    const player = { on: vi.fn(), attachMediaElement: vi.fn(), load: vi.fn(), destroy: vi.fn() }
    tsMock.createPlayer.mockReturnValue(player)
    const pending = []
    const fetchSpy = vi.spyOn(globalThis, 'fetch').mockImplementation((url, options) => new Promise(resolve => pending.push({ url, options, resolve })))
    const wrapper = mountAttached(PlayerPanel, { props: { account: { user_id: 'test', drive_id: 'test' }, file: { file_id: 'video', name: 'video.mkv' } } })
    await flushPromises()
    const video = document.querySelector('video')
    expect(video).not.toBeNull()
    expect(player.attachMediaElement).toHaveBeenCalledWith(video)
    expect(player.load).toHaveBeenCalledOnce()
    const progress = document.querySelector('.pp-progress input')
    expect(progress).not.toBeNull()
    await setDomInput(progress, 90)
    expect(video.currentTime).toBe(0)
    expect(fetchSpy).toHaveBeenCalledOnce()
    await setDomInput(progress, 45)
    expect(pending[0].options.signal.aborted).toBe(true)
    pending[0].resolve({ ok: true, json: async () => ({ url: '/stream/test?offset=900', start: 88 }) })
    pending[1].resolve({ ok: true, json: async () => ({ url: '/stream/test?offset=450', start: 43 }) })
    await flushPromises()
    expect(tsMock.createPlayer).toHaveBeenCalledTimes(2)
    expect(tsMock.createPlayer.mock.calls[1][0].url).toContain('offset=450')
    wrapper.unmount()
    expect(player.destroy).toHaveBeenCalledTimes(2)
  })
  it.each(['image', 'audio'])('%s 预览加载中和失败后始终保留窗口关闭按钮', async (kind) => {
    let rejectPreview
    api.PreviewURL.mockImplementation(() => new Promise((resolve, reject) => { rejectPreview = reject }))
    const wrapper = mountAttached(PreviewModal, { props: { account: { user_id: 'test', drive_id: 'test' }, file: { file_id: 'test', name: kind === 'audio' ? 'test.wav' : 'test.png' } } })
    await flushPromises()
    expect(document.querySelectorAll('.pv-window-btn')).toHaveLength(3)
    rejectPreview(new Error('资源加载失败'))
    await flushPromises()
    expect(document.body.textContent).toContain('资源加载失败')
    document.querySelector('.pv-window-close').click()
    expect(wrapper.emitted('close')).toHaveLength(1)
  })

  it('音频滑杆保留原生方向键行为，不触发全局音量快捷键', async () => {
    api.PinFileSnapshot.mockResolvedValue(undefined)
    api.getPlayCursor.mockResolvedValue(0)
    api.savePlayCursor.mockResolvedValue(undefined)
    api.PreviewURL.mockResolvedValue('https://example.test/audio.wav')
    const wrapper = mountAttached(PreviewModal, { props: { account: { user_id: 'test', drive_id: 'test' }, file: { file_id: 'test', name: 'test.wav' } } })
    await flushPromises()
    const before = wrapper.vm.audioVolume
    expect(document.querySelector('.pv-audio-vol-range'), document.body.textContent).not.toBeNull()
    document.querySelector('.pv-audio-vol-range').dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowUp', bubbles: true, cancelable: true }))
    expect(wrapper.vm.audioVolume).toBe(before)
  })

  it('账号侧栏键盘焦点到达添加按钮后仍可返回账号', async () => {
    const wrapper = mountAttached(AccountRail, { props: { accounts: [{ user_id: 'pikpak:one' }] } })
    const account = wrapper.get('.rail-item')
    account.element.focus()
    await account.trigger('keydown', { key: 'ArrowDown' })
    expect(document.activeElement).toBe(wrapper.get('.rail-add').element)
    await wrapper.get('.rail-add').trigger('keydown', { key: 'ArrowUp' })
    expect(document.activeElement).toBe(account.element)
  })

  it('鼠标仍在账号侧栏内时焦点移出不会收起侧栏', async () => {
    vi.useFakeTimers()
    const wrapper = mountAttached(AccountRail, { props: { accounts: [{ user_id: 'pikpak:one' }] } })
    const outside = document.createElement('button')
    document.body.appendChild(outside)
    await wrapper.get('.account-rail').trigger('mouseenter')
    await vi.advanceTimersByTimeAsync(250)
    wrapper.get('.rail-item').element.focus()
    outside.focus()
    await vi.advanceTimersByTimeAsync(250)
    expect(wrapper.get('.account-rail').classes()).toContain('expanded')
    await wrapper.get('.account-rail').trigger('mouseleave')
    await vi.advanceTimersByTimeAsync(250)
    expect(wrapper.get('.account-rail').classes()).not.toContain('expanded')
  })

  it('账号侧栏右键菜单打开时保持宽度，不因鼠标移入菜单而收起', async () => {
    vi.useFakeTimers()
    const account = { user_id: 'pikpak:one' }
    const wrapper = mountAttached(AccountRail, { props: { accounts: [account] } })
    await wrapper.get('.account-rail').trigger('mouseenter')
    await vi.advanceTimersByTimeAsync(250)
    expect(wrapper.get('.account-rail').classes()).toContain('expanded')
    await wrapper.get('.rail-item').trigger('contextmenu', { clientX: 80, clientY: 100 })
    await wrapper.get('.account-rail').trigger('mouseleave')
    await vi.advanceTimersByTimeAsync(250)
    expect(wrapper.get('.account-rail').classes()).toContain('expanded')
    expect(wrapper.emitted('select')).toBeUndefined()
  })

  it('账号拖拽取消或组件卸载时不保存临时顺序并清理拖动状态', async () => {
    const save = vi.spyOn(appearance, 'setPref')
    const accounts = [{ user_id: 'pikpak:one' }, { user_id: 'pikpak:two' }]
    const wrapper = mountAttached(AccountRail, { props: { accounts } })
    await wrapper.findAll('.rail-item')[0].trigger('pointerdown', { button: 0, clientX: 10, clientY: 10 })
    window.dispatchEvent(new MouseEvent('pointermove', { clientX: 10, clientY: 30 }))
    expect(document.body.classList.contains('rail-drag-active')).toBe(true)
    window.dispatchEvent(new MouseEvent('pointercancel'))
    expect(save).not.toHaveBeenCalled()
    expect(document.body.classList.contains('rail-drag-active')).toBe(false)
    await wrapper.findAll('.rail-item')[0].trigger('pointerdown', { button: 0, clientX: 10, clientY: 10 })
    window.dispatchEvent(new MouseEvent('pointermove', { clientX: 10, clientY: 30 }))
    wrapper.unmount()
    wrappers.splice(wrappers.indexOf(wrapper), 1)
    expect(document.body.classList.contains('rail-drag-active')).toBe(false)
    window.dispatchEvent(new MouseEvent('pointerup'))
    expect(save).not.toHaveBeenCalled()
  })

  it('目录树点击已展开目录只导航，箭头才折叠', async () => {
    const node = { file_id: 'folder', name: '文件夹' }
    const wrapper = mountAttached(TreeNode, { props: { node, tree: {}, expanded: { folder: true } } })
    await wrapper.get('.tn-label').trigger('click')
    expect(wrapper.emitted('select')).toEqual([[node]])
    expect(wrapper.emitted('toggle')).toBeUndefined()
    await wrapper.get('.tn-arrow').trigger('click')
    expect(wrapper.emitted('toggle')).toEqual([[node]])
    expect(wrapper.emitted('select')).toHaveLength(1)
  })

  it('右键菜单按实际尺寸避让窗口边缘', async () => {
    const bounds = vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(function () {
      return this.classList.contains('ctx-menu') ? { width: 340, height: 120 } : { width: 0, height: 0 }
    })
    try {
      mountAttached(ContextMenu, { props: { x: window.innerWidth - 10, y: window.innerHeight - 10, items: [{ label: '较长的操作名称', action: 'open' }] } })
      await flushPromises()
      const menu = document.querySelector('.ctx-menu')
      expect(parseFloat(menu.style.left) + 340).toBeLessThanOrEqual(window.innerWidth - 8)
      expect(parseFloat(menu.style.top) + 120).toBeLessThanOrEqual(window.innerHeight - 8)
    } finally {
      bounds.mockRestore()
    }
  })

  it('菜单鼠标焦点与键盘确认一致，更新项目后重置选项', async () => {
    const wrapper = mountAttached(ContextMenu, { props: { x: 10, y: 10, items: [
      { label: '打开', action: 'open' }, { label: '删除', action: 'delete' },
    ] } })
    await flushPromises()
    const buttons = document.querySelectorAll('.ctx-item')
    buttons[1].focus()
    buttons[1].dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true }))
    expect(wrapper.emitted('select')).toEqual([['delete']])
    await wrapper.setProps({ items: [{ label: '刷新', action: 'refresh' }] })
    await flushPromises()
    expect(document.activeElement.textContent).toContain('刷新')
  })

  it('菜单外部滚动关闭，内部滚动不关闭，Escape 恢复触发位置焦点', async () => {
    const opener = document.createElement('button')
    document.body.appendChild(opener)
    opener.focus()
    const wrapper = mountAttached(ContextMenu, { props: { x: 10, y: 10, items: [{ label: '打开', action: 'open' }] } })
    await flushPromises()
    const menu = document.querySelector('.ctx-menu')
    menu.dispatchEvent(new Event('scroll'))
    expect(wrapper.emitted('close')).toBeUndefined()
    document.activeElement.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }))
    expect(wrapper.emitted('close')).toHaveLength(1)
    wrapper.unmount()
    wrappers.splice(wrappers.indexOf(wrapper), 1)
    expect(document.activeElement).toBe(opener)
    const second = mountAttached(ContextMenu, { props: { x: 10, y: 10, items: [{ label: '刷新' }] } })
    await flushPromises()
    window.dispatchEvent(new Event('scroll'))
    expect(second.emitted('close')).toHaveLength(1)
  })

  it('PikPak 独立窗口使用原会话，提前到达的最终 token 只自动登录一次', async () => {
    localStorage.setItem('login_provider', 'pikpak')
    let complete
    api.onEvent.mockImplementation((name, callback) => {
      if (name === 'pikpak:captcha:completed') complete = callback
      return () => {}
    })
    api.login.mockReset()
    api.login.mockRejectedValueOnce(new Error('pikpak: captcha_required\nurl=https://user.mypikpak.com/challenge\ntoken=initial-token\nsession=current-session'))
      .mockResolvedValueOnce(undefined)
    let opened
    api.ShowPikPakCaptcha.mockImplementation(() => new Promise(resolve => { opened = resolve }))
    const wrapper = mountAttached(LoginModal, {
      props: { providers: [{ ID: 'pikpak', Meta: { label: 'PikPak' }, Login: { fields: [
        { key: 'username', type: 'text', label: '账号', required: true },
        { key: 'password', type: 'password', label: '密码', required: true },
      ] } }] },
      global: { stubs: { UiIcon: true } },
    })
    await nextTick()
    const inputs = [...document.body.querySelectorAll('input')]
    await setDomInput(inputs.find(input => input.type === 'text'), 'test@example.test')
    await setDomInput(inputs.find(input => input.type === 'password'), 'password')
    document.body.querySelector('form').dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await flushPromises()
    expect(api.ShowPikPakCaptcha).toHaveBeenCalledWith('current-session', 'https://user.mypikpak.com/challenge')
    expect(document.body.querySelector('iframe')).toBeNull()
    complete({ session_id: 'stale-session', captcha_token: 'wrong' })
    complete({ session_id: 'current-session', captcha_token: 'final-token' })
    expect(api.login).toHaveBeenCalledTimes(1)
    opened(true)
    await flushPromises()
    expect(api.login).toHaveBeenCalledTimes(2)
    expect(api.login.mock.calls[1][1]).toMatchObject({ captcha_token: 'final-token', captcha_verified: 'true' })
    complete({ session_id: 'current-session', captcha_token: 'final-token' })
    await flushPromises()
    expect(api.login).toHaveBeenCalledTimes(2)
    expect(wrapper.emitted('close')).toHaveLength(1)
    api.onEvent.mockImplementation(() => () => {})
  })

  it('弹窗声明对话框语义，并由 Escape 请求关闭和恢复焦点', async () => {
    const opener = document.createElement('button')
    document.body.appendChild(opener)
    opener.focus()

    const wrapper = mountAttached(Modal, {
      props: { title: '删除文件' },
      slots: { default: '<button type="button">确认</button>' },
    })
    await nextTick()

    const dialog = document.querySelector('[role="dialog"]')
    expect(dialog).not.toBeNull()
    expect(dialog.getAttribute('aria-modal')).toBe('true')
    expect(dialog.getAttribute('aria-label')).toBe('删除文件')

    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    expect(wrapper.emitted('close')).toHaveLength(1)

    wrapper.unmount()
    wrappers.splice(wrappers.indexOf(wrapper), 1)
    await nextTick()
    expect(document.activeElement).toBe(opener)
  })

  it('普通弹窗统一提供经典三键，沉浸式弹窗不渲染实体标题栏', async () => {
    const standard = mountAttached(Modal, { props: { title: '窗口控制' } })
    await nextTick()

    const controls = [...document.querySelectorAll('.modal-window-btn')]
    expect(controls).toHaveLength(3)
    expect(controls.map((button) => button.getAttribute('aria-label'))).toEqual(['最小化窗口', '最大化窗口', '关闭对话框'])

    standard.unmount()
    wrappers.splice(wrappers.indexOf(standard), 1)
    await nextTick()

    mountAttached(Modal, { props: { title: '图片预览', hideHead: true } })
    await nextTick()
    expect(document.querySelector('.modal-head')).toBeNull()
    expect(document.querySelectorAll('.modal-window-btn')).toHaveLength(0)
  })

  it('下拉框以键盘跳过禁用项并提交当前可选项', async () => {
    const wrapper = mountAttached(UiSelect, {
      props: {
        modelValue: 'alpha',
        placeholder: '选择供应商',
        options: [
          { value: 'alpha', label: 'Alpha' },
          { value: 'blocked', label: '不可用', disabled: true },
          { value: 'beta', label: 'Beta' },
        ],
      },
    })
    const trigger = wrapper.get('[role="combobox"]')

    await trigger.trigger('keydown', { key: 'ArrowDown' })
    await nextTick()
    expect(trigger.attributes('aria-expanded')).toBe('true')
    expect(document.querySelector('[role="listbox"]')).not.toBeNull()

    await trigger.trigger('keydown', { key: 'Enter' })
    expect(wrapper.emitted('update:modelValue')).toEqual([['beta']])
    expect(wrapper.emitted('change')).toEqual([['beta']])
    expect(trigger.attributes('aria-expanded')).toBe('false')
  })

  it('WebDAV 预设会同时填写连接名称和地址，且密码只有一个自定义切换按钮', async () => {
    localStorage.setItem('login_provider', 'webdav')
    const wrapper = mountAttached(LoginModal, {
      props: {
        providers: [
          { ID: 'webdav', Meta: { label: 'WebDAV' }, Login: { fields: [] } },
        ],
      },
      global: { stubs: { UiIcon: true } },
    })
    await nextTick()
    const preset = wrapper.findAllComponents(UiSelect)[0]
    await preset.vm.$emit('update:modelValue', 'jianguoyun')
    await preset.vm.$emit('change', 'jianguoyun')
    await nextTick()

    const inputs = [...document.body.querySelectorAll('input')]
    expect(inputs.find((input) => input.getAttribute('placeholder') === '我的 WebDAV / S3').value).toBe('坚果云')
    expect(inputs.find((input) => input.getAttribute('placeholder') === 'https://dav.example.com').value).toBe('https://dav.jianguoyun.com/dav/')
    expect(document.body.querySelectorAll('.password-toggle')).toHaveLength(1)
    expect(document.body.querySelector('[role="switch"]').getAttribute('aria-checked')).toBe('false')
  })

  it('普通蓝奏使用内置选择项，天翼云不再要求选择云空间', async () => {
    localStorage.setItem('login_provider', 'lanzou')
    const lanzou = mountAttached(LoginModal, {
      props: {
        providers: [{
          ID: 'lanzou', Meta: { label: '蓝奏云' }, Login: { fields: [
            { key: 'username', type: 'text', label: '账号', required: true },
            { key: 'password', type: 'password', label: '密码', required: true },
            { key: 'upload_tier', type: 'select', label: '会员等级', options: [
              { value: 'v0', label: 'V0（100 MB）' },
              { value: 'v3', label: 'V3（550 MB）' },
            ] },
          ] },
        }],
      },
      global: { stubs: { UiIcon: true } },
    })
    await nextTick()
    const tier = lanzou.findComponent(UiSelect)
    expect(tier.props('modelValue')).toBe('v0')
    expect(tier.props('options')).toContainEqual({ value: 'v3', label: 'V3（550 MB）' })

    lanzou.unmount()
    wrappers.splice(wrappers.indexOf(lanzou), 1)
    localStorage.setItem('login_provider', 'pan189')
    const pan189 = mountAttached(LoginModal, {
      props: {
        providers: [{
          ID: 'pan189', Meta: { label: '天翼云盘' }, Login: { fields: [
            { key: 'username', type: 'text', label: '账号', required: true },
            { key: 'password', type: 'password', label: '密码', required: true },
          ] },
        }],
      },
      global: { stubs: { UiIcon: true } },
    })
    await nextTick()
    expect(pan189.findAllComponents(UiSelect)).toHaveLength(0)
  })

  it.each(['pan139', 'pan189'])('%s 可主动选择短信登录且不要求密码', async (id) => {
    localStorage.setItem('login_provider', id)
    api.login.mockResolvedValue(undefined)
    api.SendPan139SMS.mockResolvedValue(undefined)
    api.SendPan189SMS.mockResolvedValue('')
    if (id === 'pan189') api.SendPan189SMS.mockResolvedValueOnce('data:image/png;base64,dGVzdA==')
    const wrapper = mountAttached(LoginModal, {
      props: { providers: [{ ID: id, Meta: { label: id }, Login: { fields: [
        { key: 'login_mode', type: 'select', label: '登录方式', required: true, options: [{ value: 'password', label: '账号密码' }, { value: 'sms', label: '短信验证码' }] },
        { key: 'username', type: 'text', label: '手机号', required: true },
        { key: 'password', type: 'password', label: '密码', required: true },
        { key: 'sms_code', type: 'text', label: '短信验证码' },
        { key: 'validate_code', type: 'text', label: '图形验证码' },
      ] } }] }, global: { stubs: { UiIcon: true } },
    })
    const mode = wrapper.findComponent(UiSelect)
    mode.vm.$emit('update:modelValue', 'sms')
    await nextTick()
    const field = (label) => [...document.body.querySelectorAll('.login-field')].find((f) => f.querySelector('label')?.textContent === label)
    expect(field('密码').style.display).toBe('none')
    expect(field('短信验证码').style.display).not.toBe('none')
    await setDomInput(field('手机号').querySelector('input'), '13800138000')
    await field('短信验证码').querySelector('button').click()
    await flushPromises()
    const send = id === 'pan139' ? api.SendPan139SMS : api.SendPan189SMS
    expect(send.mock.calls[0][0]).toBe('13800138000')
    if (id === 'pan189') {
      expect(document.body.querySelector('.captcha-image-row img')?.getAttribute('src')).toBe('data:image/png;base64,dGVzdA==')
      expect(field('图形验证码').style.display).not.toBe('none')
      expect(field('短信验证码').querySelector('button').disabled).toBe(false)
      await setDomInput(field('图形验证码').querySelector('input'), 'ABCD')
      await field('短信验证码').querySelector('button').click()
      await flushPromises()
      expect(api.SendPan189SMS).toHaveBeenLastCalledWith('13800138000', 'ABCD')
    }
    await setDomInput(field('短信验证码').querySelector('input'), '123456')
    document.body.querySelector('form').dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await flushPromises()
    expect(api.login).toHaveBeenCalledWith(id, expect.objectContaining({ login_mode: 'sms', username: '13800138000', sms_code: '123456' }))
    expect(api.login.mock.calls[0][1].password).toBeUndefined()
  })

  it.each(['139 登录需要短信安全校验', '139 登录失败：S305 尚未设置移动认证账号密码'])('139 账密失败后显示原因并切换短信：%s', async (reason) => {
    localStorage.setItem('login_provider', 'pan139')
    api.login
      .mockRejectedValueOnce(new Error(`pan139_sms_required\n${reason}`))
      .mockResolvedValueOnce(undefined)
    api.SendPan139SMS.mockResolvedValue(undefined)
    const wrapper = mountAttached(LoginModal, {
      props: {
        providers: [{
          ID: 'pan139', Meta: { label: '移动云盘' }, Login: { fields: [
            { key: 'login_mode', type: 'select', label: '登录方式', required: true, options: [] },
            { key: 'username', type: 'text', label: '手机号/账号', required: true },
            { key: 'password', type: 'password', label: '密码', required: false },
            { key: 'sms_code', type: 'text', label: '短信验证码', required: false },
          ] },
        }],
      },
      global: { stubs: { UiIcon: true } },
    })
    await nextTick()

    const inputForLabel = (label) => [...document.body.querySelectorAll('.login-field')]
      .find((field) => field.querySelector('label')?.textContent === label)
      ?.querySelector('input')
    await setDomInput(inputForLabel('手机号/账号'), '13800138000')
    await setDomInput(inputForLabel('密码'), 'password')
    document.body.querySelector('form').dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await Promise.resolve()
    await nextTick()

    expect(api.login).toHaveBeenCalledTimes(1)
    expect(document.body.querySelector('.form-error')?.textContent).toContain('请获取并填写短信验证码')
    expect(document.body.querySelector('.form-error')?.textContent).toContain(reason)
    const smsField = [...document.body.querySelectorAll('.login-field')]
      .find((field) => field.querySelector('label')?.textContent === '短信验证码')
    expect(smsField.style.display).not.toBe('none')
    expect([...document.body.querySelectorAll('.login-field')]
      .find((field) => field.querySelector('label')?.textContent === '密码').style.display).toBe('none')

    const sendButton = [...smsField.querySelectorAll('button')].find((button) => button.textContent.includes('获取验证码'))
    await sendButton.click()
    await Promise.resolve()
    expect(api.SendPan139SMS).toHaveBeenCalledWith('13800138000')

    await setDomInput(inputForLabel('短信验证码'), '123456')
    document.body.querySelector('form').dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await Promise.resolve()
    await nextTick()
    expect(api.login).toHaveBeenCalledTimes(2)
    expect(api.login.mock.calls[1][0]).toBe('pan139')
    expect(api.login.mock.calls[1][1]).toMatchObject({ login_mode: 'sms', username: '13800138000', sms_code: '123456' })
  })

  it('S3 默认不做写入验证和内网预览授权，保存时只提交一次连接配置', async () => {
    api.saveMounted.mockResolvedValue(undefined)
    localStorage.setItem('login_provider', 's3')
    const wrapper = mountAttached(LoginModal, {
      props: {
        providers: [
          { ID: 'webdav', Meta: { label: 'WebDAV' }, Login: { fields: [] } },
          { ID: 's3', Meta: { label: 'S3' }, Login: { fields: [] } },
        ],
      },
      global: { stubs: { UiIcon: true } },
    })
    await nextTick()

    const values = { endpoint: 'https://s3.example.test', username: 'access-key', password: 'secret-key', bucket: 'mnemo' }
    const inputs = [...document.body.querySelectorAll('input')]
    await setDomInput(inputs.find((input) => input.placeholder === 's3.us-east-1.amazonaws.com (可选，默认 AWS)'), values.endpoint)
    const inputForLabel = (label) => [...document.body.querySelectorAll('.login-field')]
      .find((field) => field.querySelector('label')?.textContent.startsWith(label))
      ?.querySelector('input')
    await setDomInput(inputForLabel('Access Key ID'), values.username)
    await setDomInput(inputForLabel('Secret Access Key'), values.password)
    await setDomInput(inputForLabel('Bucket'), values.bucket)

    const switches = [...document.body.querySelectorAll('[role="switch"]')]
    expect(switches).toHaveLength(3)
    expect(switches[0].getAttribute('aria-checked')).toBe('false')
    expect(switches[1].getAttribute('aria-checked')).toBe('true')
    expect(switches[2].getAttribute('aria-checked')).toBe('false')
    document.body.querySelector('form').dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await nextTick()

    expect(api.validateMountedWrite).not.toHaveBeenCalled()
    expect(api.saveMounted).toHaveBeenCalledTimes(1)
    expect(api.saveMounted.mock.calls[0][0]).toBe('s3')
    expect(api.saveMounted.mock.calls[0][1]).toMatchObject({ name: 'S3', endpoint: values.endpoint, username: values.username, password: values.password, bucket: values.bucket, allowPrivateNetwork: false })
    expect(api.saveMounted.mock.calls[0][1]).not.toHaveProperty('verifyWrite')
  })

  it('登录请求期间锁定服务商，避免异步结果串到另一个网盘', async () => {
    localStorage.setItem('login_provider', 'dropbox')
    let resolveLogin
    api.login.mockImplementation(() => new Promise((resolve) => { resolveLogin = resolve }))
    const wrapper = mountAttached(LoginModal, {
      props: {
        providers: [
          { ID: 'dropbox', Meta: { label: 'Dropbox' }, Login: { fields: [{ key: 'oauth', type: 'oauth', label: '浏览器授权' }] } },
          { ID: 'pikpak', Meta: { label: 'PikPak' }, Login: { fields: [{ key: 'username', type: 'text', label: '账号', required: true }] } },
        ],
      },
      global: { stubs: { UiIcon: true } },
    })
    await nextTick()
    document.body.querySelector('form').dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    await nextTick()

    expect(api.login).toHaveBeenCalledWith('dropbox', {})
    const providerButtons = [...document.body.querySelectorAll('.lp-item')]
    const pikpakButton = providerButtons.find((button) => button.textContent.includes('PikPak'))
    expect(pikpakButton.disabled).toBe(true)
    expect(api.login).toHaveBeenCalledTimes(1)
    expect(providerButtons.find((button) => button.classList.contains('active')).textContent).toContain('Dropbox')

    resolveLogin()
    await Promise.resolve()
    await nextTick()
  })

  it('账号容量在启动同步，并支持右上角手动同步', async () => {
    vi.useFakeTimers()
    api.refreshAccountSilently.mockResolvedValue({ user_id: 'quota-dedupe', token: {}, usage: { size: 100, used: 20 } })
    api.refreshAccountNow.mockResolvedValue({ user_id: 'quota-dedupe', token: {}, usage: { size: 100, used: 20 } })
    const account = { user_id: 'quota-dedupe', token: {}, usage: null }
    const wrapper = mountAttached(AccountAvatar, { props: { account, providers: [] }, global: { stubs: { UiIcon: true } } })

    await vi.runAllTicks()
    expect(api.refreshAccountSilently).toHaveBeenCalledTimes(1)
    expect(api.refreshAccountNow).not.toHaveBeenCalled()
    await wrapper.get('.acc-ava').trigger('mouseenter')
    await vi.advanceTimersByTimeAsync(120)
    const refreshButton = document.querySelector('.ap-refresh')
    expect(refreshButton).not.toBeNull()
    refreshButton.click()
    await vi.runAllTicks()
    expect(api.refreshAccountNow).toHaveBeenCalledTimes(1)
  })
})
