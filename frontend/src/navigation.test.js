import { describe, expect, it } from 'vitest'
import { createNavigationHistory, installMouseNavigation } from './navigation'

describe('鼠标导航', () => {
  it('保留真实访问顺序，后退后新导航会清除前进分支', () => {
    const history = createNavigationHistory()
    history.record({ page: 'pan', path: ['a'] })
    history.record({ page: 'share' })
    history.record({ page: 'settings' })
    expect(history.move(-1)).toEqual({ page: 'share' })
    expect(history.move(-1)).toEqual({ page: 'pan', path: ['a'] })
    expect(history.move(-1)).toBeNull()
    expect(history.move(1)).toEqual({ page: 'share' })
    history.record({ page: 'transfer' })
    expect(history.move(1)).toBeNull()
    expect(history.move(-1)).toEqual({ page: 'share' })
  })
  it('快照不受原对象变化影响，重复记录合并', () => {
    const history = createNavigationHistory(2)
    const state = { path: ['a'] }
    history.record(state)
    state.path.push('b')
    history.record(state)
    history.record(state)
    expect(history.move(-1)).toEqual({ path: ['a'] })
    history.reset({ path: [] })
    expect(history.move(-1)).toBeNull()
  })
  it('侧键只在释放时触发一次，弹窗阻止导航，卸载移除监听', () => {
    const target = new EventTarget()
    const moves = []
    let blocked = false
    const off = installMouseNavigation(target, direction => moves.push(direction), () => blocked)
    for (const type of ['mousedown', 'mouseup', 'auxclick']) {
      const event = new MouseEvent(type, { button: 3, cancelable: true })
      target.dispatchEvent(event)
      expect(event.defaultPrevented).toBe(true)
    }
    expect(moves).toEqual([-1])
    blocked = true
    target.dispatchEvent(new MouseEvent('mouseup', { button: 4 }))
    expect(moves).toEqual([-1])
    blocked = false
    target.dispatchEvent(new MouseEvent('mouseup', { button: 4 }))
    expect(moves).toEqual([-1, 1])
    off()
    target.dispatchEvent(new MouseEvent('mouseup', { button: 3 }))
    expect(moves).toEqual([-1, 1])
  })
})
