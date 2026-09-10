import { mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { nextTick } from 'vue'

const api = vi.hoisted(() => ({
  listDir: vi.fn(),
  mkdir: vi.fn(),
  providerMetaOf: vi.fn(),
  providerIconUrl: vi.fn(() => ''),
  accountName: vi.fn(account => account.user_id),
}))

vi.mock('../api', () => api)

import SelectDirModal from './SelectDirModal.vue'

const wrapperOptions = {
  props: {
    account: { user_id: 'webdav:test', drive_id: 'webdav' },
    providers: [],
  },
  global: {
    stubs: {
      Modal: { template: '<section><slot /><slot name="actions" /></section>' },
      UiIcon: true,
    },
  },
}

let wrapper

afterEach(() => {
  wrapper?.unmount()
  wrapper = null
  vi.clearAllMocks()
})

describe('选择目录弹窗', () => {
  it('新建目录返回业务错误时保留输入并展示失败，不伪报成功', async () => {
    api.providerMetaOf.mockReturnValue({ rootKey: 'root', rootTitle: '根目录' })
    api.listDir.mockResolvedValue([])
    api.mkdir.mockResolvedValue({ error: 'context canceled' })
    wrapper = mount(SelectDirModal, wrapperOptions)
    await nextTick()
    await Promise.resolve()

    await wrapper.get('input[placeholder="新建文件夹..."]').setValue('临时目录')
    await wrapper.get('.new-folder-row .btn').trigger('click')
    await Promise.resolve()

    expect(api.mkdir).toHaveBeenCalledWith('webdav:test', 'webdav', 'root', '临时目录')
    expect(wrapper.emitted('toast')).toEqual([['Error: context canceled', 'error']])
    expect(wrapper.emitted('toast')).not.toContainEqual(['文件夹已创建', 'success'])
    expect(wrapper.get('input[placeholder="新建文件夹..."]').element.value).toBe('临时目录')
  })
})
