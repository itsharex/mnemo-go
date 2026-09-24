import { createApp } from 'vue'
import App from './App.vue'
import { Environment } from '../wailsjs/runtime/runtime'
import './styles/design-tokens.css'
import './styles/main.css'

async function start() {
  try {
    const { platform } = await Environment()
    document.documentElement.dataset.platform = platform
  } catch { /* 浏览器预览默认保留自绘窗口按钮。 */ }
  const host = window.go?.app?.PreviewHost
  if (host) {
    const seed = await host.Config()
    const pendingSaves = new Set()
    window.go.app.App = new Proxy({}, { get: (_, method) => (...args) => {
      const request = host.Invoke(method, args)
      if (method === 'SaveCloudTextFile') {
        pendingSaves.add(request)
        request.then(() => pendingSaves.delete(request), () => pendingSaves.delete(request))
      }
      return request
    } })
    window.__mnemoPreviewDrain = () => Promise.allSettled([...pendingSaves])
    window.__mnemoPreviewHasPendingSave = () => pendingSaves.size > 0
    window.__mnemoPreviewPrefs = seed.preferences || {}
    document.documentElement.classList.add('dark', 'preview-window')
    document.documentElement.classList.toggle('oled', seed.preferences?.oledBackground === true)
    const { default: PreviewWindow } = await import('./PreviewWindow.vue')
    createApp(PreviewWindow, { seed }).mount('#app')
  } else {
    createApp(App).mount('#app')
  }
}
start().catch(() => { document.getElementById('app').textContent = '预览窗口启动失败，请关闭后重新打开。' })
