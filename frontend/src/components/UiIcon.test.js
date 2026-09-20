import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import UiIcon from './UiIcon.vue'

describe('UiIcon', () => {
  it('通过统一适配层渲染已注册图标，并保留调用方样式与无障碍属性', () => {
    const wrapper = mount(UiIcon, {
      props: { name: 'folder', size: 20 },
      attrs: { class: 'consumer-icon' },
    })

    expect(wrapper.get('svg').attributes()).toMatchObject({
      width: '20',
      height: '20',
      stroke: 'currentColor',
      'stroke-width': '1.8',
      'aria-hidden': 'true',
      focusable: 'false',
      'data-icon-name': 'folder',
    })
    expect(wrapper.classes()).toContain('consumer-icon')
  })

  it('对未知名称使用安全的 file 回退图标', () => {
    const wrapper = mount(UiIcon, { props: { name: 'not-registered' } })

    expect(wrapper.get('svg').attributes('data-icon-name')).toBe('file')
  })
})
