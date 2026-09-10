import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { nextTick } from 'vue'
import Modal from './Modal.vue'
import UiSelect from './UiSelect.vue'
import ContextMenu from './ContextMenu.vue'
import TreeNode from './TreeNode.vue'
import AccountRail from './AccountRail.vue'
import PreviewModal from './PreviewModal.vue'
import PlayerPanel from './PlayerPanel.vue'
import * as appearance from '../appearance'

const api = vi.hoisted(() => ({
  login: vi.fn(),
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
  refreshAccount: vi.fn(),
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

  it('普通蓝奏和天翼云使用内置选择项及安全默认值', async () => {
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
            { key: 'cloud_type', type: 'select', label: '云空间', options: [
              { value: 'personal', label: '个人云' },
              { value: 'family', label: '家庭云' },
            ] },
          ] },
        }],
      },
      global: { stubs: { UiIcon: true } },
    })
    await nextTick()
    const cloudType = pan189.findComponent(UiSelect)
    expect(cloudType.props('modelValue')).toBe('personal')
    expect(cloudType.props('options')).toContainEqual({ value: 'family', label: '家庭云' })
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

  it('139 账密触发安全校验后复用登录会话完成短信验证', async () => {
    localStorage.setItem('login_provider', 'pan139')
    api.login
      .mockRejectedValueOnce(new Error('pan139_sms_required\n139 登录需要短信安全校验'))
      .mockResolvedValueOnce(undefined)
    api.SendPan139SMS.mockResolvedValue(undefined)
    const wrapper = mountAttached(LoginModal, {
      props: {
        providers: [{
          ID: 'pan139', Meta: { label: '139 云盘' }, Login: { fields: [
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
    api.refreshAccountNow.mockResolvedValue({ user_id: 'quota-dedupe', token: {}, usage: { size: 100, used: 20 } })
    const account = { user_id: 'quota-dedupe', token: {}, usage: null }
    const wrapper = mountAttached(AccountAvatar, { props: { account, providers: [] }, global: { stubs: { UiIcon: true } } })

    await vi.runAllTicks()
    expect(api.refreshAccountNow).toHaveBeenCalledTimes(1)
    await wrapper.get('.acc-ava').trigger('mouseenter')
    await vi.advanceTimersByTimeAsync(120)
    const refreshButton = document.querySelector('.ap-refresh')
    expect(refreshButton).not.toBeNull()
    refreshButton.click()
    await vi.runAllTicks()
    expect(api.refreshAccountNow).toHaveBeenCalledTimes(2)
  })
})
