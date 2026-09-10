import { describe, expect, it } from 'vitest'
import {
  accountDetail,
  accountName,
  capsOf,
  extOf,
  formatBytes,
  openKindOf,
  providerOf,
} from './api'

const providers = [
  { ID: 'pikpak', Meta: { label: 'PikPak' }, Capabilities: { quota: true, upload: true } },
  { ID: 'webdav', Meta: { label: 'WebDAV' }, Capabilities: { upload: true } },
]

describe('api 纯逻辑辅助函数', () => {
  it('解析普通账号和挂载账号的 provider', () => {
    expect(providerOf('pikpak_user-1')).toBe('pikpak')
    expect(providerOf('webdav:mount-1')).toBe('webdav')
    expect(providerOf('s3:mount-1')).toBe('s3')
    expect(providerOf('')).toBe('')
  })

  it('优先使用账号显示名并清理供应商前缀', () => {
    const account = {
      user_id: 'pan189_user-1',
      token: { nick_name: '189 · 家庭云' },
    }
    expect(accountName(account)).toBe('189')
    expect(accountDetail(account, providers)).toBe('pan189 · 189')
  })

  it('返回 provider 能力并兼容大小写字段', () => {
    expect(capsOf({ user_id: 'pikpak_user-1' }, providers)).toEqual({ quota: true, upload: true })
    expect(capsOf({ user_id: 'webdav:mount-1' }, providers)).toEqual({ upload: true })
    expect(capsOf({ user_id: 'missing_1' }, providers)).toEqual({})
  })

  it('格式化容量和扩展名边界', () => {
    expect(formatBytes(0)).toBe('0 B')
    expect(formatBytes(1024)).toBe('1.0 KB')
    expect(formatBytes(1024 * 1024)).toBe('1.0 MB')
    expect(extOf('README')).toBe('')
    expect(extOf('photo.JPG')).toBe('jpg')
  })

  it('按文件类型选择打开方式，目录优先', () => {
    expect(openKindOf({ isDir: true, name: 'movie.mp4' })).toBe('dir')
		 expect(openKindOf({ name: 'movie.mp4' })).toBe('video')
		expect(openKindOf({ name: 'stream.m3u8' })).toBe('video')
		expect(openKindOf({ name: 'movie.mkv' })).toBe('download')
		expect(openKindOf({ name: 'movie.mkv' }, { cloudVideoPreview: true })).toBe('video')
		expect(openKindOf({ name: 'source.ts' }, { cloudVideoPreview: true })).toBe('text')
		expect(openKindOf({ name: 'stream.ts', mime_type: 'video/mp2t' }, { cloudVideoPreview: true })).toBe('video')
		expect(openKindOf({ name: 'cover.png' })).toBe('image')
		expect(openKindOf({ name: 'animated.GIF' })).toBe('image')
		expect(openKindOf({ name: 'cover.heic' })).toBe('download')
		expect(openKindOf({ name: 'notes.md' })).toBe('text')
		expect(openKindOf({ name: 'manual.pdf' })).toBe('pdf')
		expect(openKindOf({ name: 'manual.bin', mime_type: 'application/pdf' })).toBe('pdf')
		expect(openKindOf({ name: 'archive.zip' })).toBe('download')
	})
})
