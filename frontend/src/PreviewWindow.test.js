import { shallowMount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import PreviewWindow from './PreviewWindow.vue'

const runtime = vi.hoisted(() => ({
  EventsOn: vi.fn(() => () => {}),
  WindowHide: vi.fn(),
}))
vi.mock('../wailsjs/runtime/runtime', () => runtime)

const mounted = []
afterEach(() => {
  mounted.splice(0).forEach(wrapper => wrapper.unmount())
  runtime.WindowHide.mockClear()
  delete window.__mnemoPreviewHasPendingSave
  delete window.__mnemoPreviewDrain
  delete window.go
})

function preview(kind = 'image') {
  const wrapper = shallowMount(PreviewWindow, {
    props: { seed: { kind, file: { file_id: 'file-1', name: 'file.jpg' }, account: {}, files: [], capabilities: {} } },
  })
  mounted.push(wrapper)
  return wrapper
}

describe('预览窗口关闭', () => {
  it('图片等只读预览立即退出，不等待网络请求', async () => {
    const close = vi.fn()
    const drain = vi.fn()
    window.go = { app: { PreviewHost: { Close: close } } }
    window.__mnemoPreviewHasPendingSave = () => false
    window.__mnemoPreviewDrain = drain
    const wrapper = preview()
    await wrapper.vm.close()
    expect(runtime.WindowHide).toHaveBeenCalledOnce()
    expect(close).toHaveBeenCalledOnce()
    expect(drain).not.toHaveBeenCalled()
  })

  it('有云端文本保存时等保存完成后退出', async () => {
    const close = vi.fn()
    let finishSave
    window.go = { app: { PreviewHost: { Close: close } } }
    window.__mnemoPreviewHasPendingSave = () => true
    window.__mnemoPreviewDrain = () => new Promise(resolve => { finishSave = resolve })
    const wrapper = preview('text')
    const closing = wrapper.vm.close()
    expect(runtime.WindowHide).toHaveBeenCalledOnce()
    expect(close).not.toHaveBeenCalled()
    finishSave()
    await closing
    expect(close).toHaveBeenCalledOnce()
  })
})
