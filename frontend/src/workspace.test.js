import { describe, it, expect } from 'vitest'
import { parseShareText, cleanBackupPrefs, recordAccountHealth, accountHealth, directoryCacheKey, driveErrorNotice } from './workspace'

describe('工作区辅助逻辑', () => {
  it('目录缓存键统一编码账号、存储和根目录标识', () => {
    expect(directoryCacheKey('webdav:home', 'webdav:home', 'list', '/', '')).toBe('webdav|webdav%3Ahome|webdav%3Ahome|list|%2F|')
    expect(directoryCacheKey('pikpak_user', 'drive', 'list', 'pikpak_root', '')).toBe('pikpak|pikpak_user|drive|list|pikpak_root|')
  })
  it('从分享文案提取链接、提取码和提供商，不接受仿冒域名', () => {
    expect(parseShareText('资料 https://www.alipan.com/s/abc 提取码：A1b2')).toEqual({url:'https://www.alipan.com/s/abc',password:'A1b2',provider:'aliopen'})
    expect(parseShareText('https://123pan.com/s/abc?pwd=1234').provider).toBe('pan123')
    expect(parseShareText('https://alipan.com.evil.test/s/a').provider).toBe('')
    expect(parseShareText('不是链接').url).toBe('')
  })
  it('备份白名单排除凭据并约束数据类型和数值范围', () => {
    const source = JSON.parse('{"token":"secret","proxy":"credential","defaultVolume":999,"downloadSound":"true","accountAliases":{"__proto__":"bad","one":"资料"},"accountOrder":["two","one","two"],"accountIcons":{"one":"https://example.test/image","two":"webdav.svg"}}')
    const clean = cleanBackupPrefs(source)
    expect(clean.token).toBeUndefined(); expect(clean.proxy).toBeUndefined()
    expect(clean.downloadSound).toBeUndefined(); expect(clean.defaultVolume).toBe(200)
    expect(clean.accountOrder).toEqual(['two','one'])
    expect(Object.hasOwn(clean.accountAliases,'__proto__')).toBe(false)
    expect(clean.accountIcons).toEqual({two:'webdav.svg'})
  })
  it('区分认证、网络、配额和风控错误，成功后清除错误状态', () => {
    for (const [message,status] of [['401 unauthorized','auth'],['network timeout','network'],['空间不足','quota'],['429 风控','limited']]) {
      recordAccountHealth('one',message); expect(accountHealth.one.status).toBe(status)
    }
    recordAccountHealth('one'); expect(accountHealth.one.status).toBe('ok'); expect(accountHealth.one.message).toBe('')
  })
  it('把网盘原始错误转换为可操作的统一提示，不暴露服务端详情', () => {
    expect(driveErrorNotice(new Error('http 401: invalid_grant secret-token'), '刷新账号')).toMatchObject({ category: 'auth', message: '网盘登录已失效，请重新登录' })
    expect(driveErrorNotice(new Error('dial tcp: i/o timeout'), '加载目录')).toMatchObject({ category: 'network', message: '加载目录失败，请检查网络或代理设置后重试' })
    expect(driveErrorNotice(new Error('HTTP 429 request_id=private'), '上传文件')).toMatchObject({ category: 'limited', message: '请求过于频繁，请稍后再试' })
    expect(driveErrorNotice(new Error('captcha_expired_189'), '登录网盘')).toMatchObject({ category: 'verification', message: '验证码不正确或已过期，请重新获取后再试' })
    expect(driveErrorNotice(new Error('HTTP 403 AccessDenied: internal-policy'), '打开文件')).toMatchObject({ category: 'permission', message: '没有权限完成此操作，请检查账号或文件权限' })
    expect(driveErrorNotice(new Error('upstream exploded: stack detail'), '创建文件夹').message).toBe('创建文件夹失败，请稍后重试')
  })
})
