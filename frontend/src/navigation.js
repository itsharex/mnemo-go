// In-memory navigation only: never hand mouse side buttons to WebView history.
export function createNavigationHistory(limit = 100) {
  let entries = []
  let index = -1
  const clone = value => JSON.parse(JSON.stringify(value))
  return {
    record(value) {
      const snapshot = clone(value)
      if (JSON.stringify(entries[index]) === JSON.stringify(snapshot)) return
      entries = entries.slice(0, index + 1)
      entries.push(snapshot)
      if (entries.length > limit) entries.shift()
      index = entries.length - 1
    },
    move(direction) {
      const next = index + direction
      if (next < 0 || next >= entries.length) return null
      index = next
      return clone(entries[index])
    },
    reset(value) { entries = [clone(value)]; index = 0 },
  }
}

export function installMouseNavigation(target, navigate, blocked) {
  const handle = event => {
    if (event.button !== 3 && event.button !== 4) return
    event.preventDefault()
    event.stopImmediatePropagation()
    if (event.type === 'mouseup' && !blocked()) navigate(event.button === 3 ? -1 : 1)
  }
  const events = ['mousedown', 'mouseup', 'auxclick']
  events.forEach(name => target.addEventListener(name, handle, true))
  return () => events.forEach(name => target.removeEventListener(name, handle, true))
}
