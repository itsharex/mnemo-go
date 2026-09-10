import { describe, it, expect } from 'vitest'
import { parseShareText, cleanBackupPrefs, recordAccountHealth, accountHealth } from './workspace'

describe('工作区辅助逻辑', () => {
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
})
