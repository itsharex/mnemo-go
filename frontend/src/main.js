import { createApp } from 'vue'
import App from './App.vue'
import './styles/design-tokens.css'
import './styles/main.css'

async function start() {
  const host = window.go?.app?.PreviewHost
  if (host) {
    const seed = await host.Config()
    const pending = new Set()
    window.go.app.App = new Proxy({}, { get: (_, method) => (...args) => {
      const request = host.Invoke(method, args)
      pending.add(request)
      request.then(() => pending.delete(request), () => pending.delete(request))
      return request
    } })
    window.__mnemoPreviewDrain = () => Promise.allSettled([...pending])
    window.__mnemoPreviewPrefs = seed.preferences || {}
    document.documentElement.classList.add('dark', 'preview-window')
    const { default: PreviewWindow } = await import('./PreviewWindow.vue')
    createApp(PreviewWindow, { seed }).mount('#app')
  } else {
    createApp(App).mount('#app')
  }
}
start().catch(() => { document.getElementById('app').textContent = '预览窗口启动失败，请关闭后重新打开。' })
