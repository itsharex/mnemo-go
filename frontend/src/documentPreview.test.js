import { afterEach, expect, it, vi } from 'vitest'
import { readDocumentBytes } from './documentPreview'

afterEach(() => vi.unstubAllGlobals())

it('按实际流大小限制文档，不能只相信响应头', async () => {
  const stream = new ReadableStream({
    start(controller) {
      controller.enqueue(new Uint8Array([1, 2, 3]))
      controller.enqueue(new Uint8Array([4, 5, 6]))
      controller.close()
    },
  })
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(stream, { status: 200 })))
  await expect(readDocumentBytes('/file', 5)).rejects.toThrow('超过在线预览大小上限')
})

it('合并合法分块并保留原始字节', async () => {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(new Uint8Array([1, 2, 3]), { status: 200 })))
  await expect(readDocumentBytes('/file', 3)).resolves.toEqual(new Uint8Array([1, 2, 3]))
})
