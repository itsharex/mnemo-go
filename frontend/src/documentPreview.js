export const DOCUMENT_LIMITS = Object.freeze({
  pdf: 512 * 1024 * 1024,
  docx: 50 * 1024 * 1024,
  xlsx: 25 * 1024 * 1024,
  pptx: 100 * 1024 * 1024,
})

export async function readDocumentBytes(url, maxBytes, signal) {
  const response = await fetch(url, { signal })
  if (!response.ok) throw new Error(`文件加载失败：HTTP ${response.status}`)
  const declared = Number(response.headers.get('content-length'))
  if (declared > maxBytes) throw new Error('文件超过在线预览大小上限，请下载后查看')
  if (!response.body) {
    const buffer = await response.arrayBuffer()
    if (buffer.byteLength > maxBytes) throw new Error('文件超过在线预览大小上限，请下载后查看')
    return new Uint8Array(buffer)
  }
  const reader = response.body.getReader()
  const chunks = []
  let size = 0
  try {
    for (;;) {
      const { done, value } = await reader.read()
      if (done) break
      size += value.byteLength
      if (size > maxBytes) throw new Error('文件超过在线预览大小上限，请下载后查看')
      chunks.push(value)
    }
  } catch (error) {
    await reader.cancel().catch(() => {})
    throw error
  } finally {
    reader.releaseLock()
  }
  const bytes = new Uint8Array(size)
  let offset = 0
  for (const chunk of chunks) {
    bytes.set(chunk, offset)
    offset += chunk.byteLength
  }
  return bytes
}
