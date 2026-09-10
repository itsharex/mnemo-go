import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

export default defineConfig({
  plugins: [vue()],
  base: './',
  // jassub 的 libass worker 本身是 module worker，iife 打包不支持代码分割
  worker: { format: 'es' },
  build: {
    outDir: 'dist',
    assetsInlineLimit: 0,
  },
  server: {
    port: 5173,
    // Wails reads the actual URL from Vite output, so occupied ports may fall back.
    strictPort: false,
  },
  test: {
    environment: 'jsdom',
    clearMocks: true,
    restoreMocks: true,
  },
})
