# 网盘功能支持矩阵（Provider Status）

> 数据来源：当前仓库 `internal/drive/providers/*`、统一能力注册表和本地自动化测试（最近更新：2026-09-10）。分享创建已补齐协议级回归；未使用真实网盘账号创建公开链接。
> 废弃说明：`gofile`、`gdrive` 已按需求移除；`encryption`（加密文件名/加密流）不再支持，相关能力位不再纳入矩阵。
> 图示：✅ 已实现 · ⚠️ 部分/有差距 · ❌ 缺失/不支持 · ➖ 设计上不适用

---

## 一、在役网盘（13 个）

| # | Provider | 登录方式 | 能力实现 | 当前自动验证范围 | 已知限制 |
|---|----------|---------|:--------:|------------------|----------|
| 1 | pikpak | 账密 + 验证码 | ✅ 已注册 | 包内单测 + mock/e2e | 登录可能触发服务端风控（见 docs/KNOWN_ISSUES.md）；必须遵守冷却 |
| 2 | aliopen（阿里云盘） | refresh_token | ✅ 已注册 | mock/e2e | 包内覆盖仍不足 |
| 3 | pan123（123 云盘） | 账密 | ✅ 已注册 | 包内单测 + mock/e2e | 真实服务未在本轮验证 |
| 4 | pan189（天翼云盘） | 账密 + 验证码 | ✅ 已注册 | 包内单测 + mock/e2e | 个人云可读取容量；家庭云容量与个人云独立，当前不猜测展示 |
| 5 | pan139（139 云盘） | 手机号/邮箱 + 密码 / Authorization | ✅ 已注册 | 包内配额单测 + mock/e2e | 仅个人云：单次 `getDiskInfo` 容量读取；家庭云需独立协议，当前不暴露 |
| 6 | lanzou（蓝奏云） | Cookie / 账密 | ✅ 已注册 | 包内单测 + mock/e2e | 无限空间；按 V0–V3 前后端拦截单文件大小和后缀 |
| 7 | ilanzou（优享版蓝奏云） | 账密 | ✅ 已注册 | 包内单测 + mock/e2e | 从 account/map 读取基础、会员、奖励及已用容量 |
| 8 | onedrive | OAuth PKCE | ✅ 已注册 | 包内单测 + mock/e2e | 真实 OAuth/服务未在本轮验证 |
| 9 | dropbox | OAuth PKCE | ✅ 已注册 | 包内单测 + mock/e2e | 国内直连受限需配置网络代理（见 docs/KNOWN_ISSUES.md） |
| 10 | yike（一刻相册） | BDUSS / Cookie | ✅ 已注册 | mock/e2e | 按无限空间展示，不参与跨盘秒传目标 |
| 11 | guangya（光鸭云盘） | 手机号 + 短信 / refresh_token | ✅ 已注册 | mock/e2e（含 `/assets/v1/get_assets` 容量） | 包内覆盖仍不足 |
| 12 | webdav | URL + 账密 / Bearer Token | ✅ 已注册 | 本地 HTTP e2e（Basic、Digest、动态下载鉴权） | 支持自动 Basic/Digest 协商和显式 Bearer；客户端证书、NTLM 等不支持；配额取决于 RFC 4331 支持；支持各类预设定制图标 |
| 13 | s3 | endpoint + AK/SK | ✅ 已注册 | mock/e2e | 无通用总容量接口；连接校验不证明写权限 |

“已注册/已实现”只表示统一驱动入口和对应代码路径存在，不代表所有真实服务商版本、权限模型和风控条件均已验证；可靠性应以测试范围和已知限制判断，不使用主观完成度百分比。

---

## 二、基础功能矩阵

### 2.1 登录与鉴权

| Provider | 账密 | OAuth PKCE | Cookie/Token | 短信验证码 | 验证码挑战 | Token 自动刷新 | RefreshAccount | 配额刷新 |
|----------|:----:|:----------:|:------------:|:----------:|:----------:|:--------------:|:--------------:|:--------:|
| pikpak | ✅ | ➖ | refresh_token | ➖ | ✅ | ✅ | ✅ | ✅ |
| aliopen | ➖ | ➖ | refresh_token | ➖ | ➖ | ✅ | ✅ | ✅ |
| pan123 | ✅ | ➖ | refresh_token | ➖ | ➖ | ✅(401重登) | ✅ | ✅ |
| pan189 | ✅ | ➖ | session | ✅ | ✅ | ✅ | ✅ | ✅（个人云；家庭云未知） |
| pan139 | ✅ | ➖ | Authorization | ✅ | ➖ | ✅ | ✅ | ✅（个人云） |
| lanzou | ✅ | ➖ | ✅ | ➖ | ➖ | ✅(内联) | ✅ | 无限空间 |
| ilanzou | ✅ | ➖ | session | ➖ | ➖ | ✅(内联) | ✅ | ✅ |
| onedrive | ➖ | ✅ | refresh_token | ➖ | ➖ | ✅ | ✅ | ✅ |
| dropbox | ➖ | ✅ | refresh_token | ➖ | ➖ | ✅ | ✅ | ✅ |
| yike | ➖ | ➖ | BDUSS | ➖ | ➖ | ➖ | ✅ | 无限空间 |
| guangya | ➖ | ➖ | refresh_token | ✅ | ➖ | ✅ | ✅ | ✅ |
| webdav | ➖ | ➖ | 账密 | ➖ | ➖ | ➖ | ✅(可选配额) | ✅(RFC 4331) |
| s3 | ➖ | ➖ | AK/SK | ➖ | ➖ | ➖ | ➖ | ➖ |

> ✅ onedrive/dropbox 的 `RefreshAccount` 已实现：token 自动刷新 + 账号信息/配额拉取。

---

### 2.2 文件列表与搜索

| Provider | List | ListPaged | 游标分页 | 分页防环 | Search(云端) | 本地搜索索引 |
|----------|:----:|:---------:|:--------:|:--------:|:------------:|:------------:|
| pikpak | ✅ | ✅ | ✅ | ✅ | ❌(设计) | 由 ops 兜底 |
| aliopen | ✅ | ✅ | ✅ | ✅ | ✅ | — |
| pan123 | ✅ | ✅ | ❌(数字页码) | ✅ | ✅ | — |
| pan189 | ✅ | ✅ | ✅ | ➖ | ❌(设计) | — |
| pan139 | ✅ | ✅ | ✅ | ❌ | ❌(设计) | — |
| lanzou | ✅ | ➖ | ➖ | ➖ | ❌(设计) | — |
| ilanzou | ✅ | ➖ | ➖ | ➖ | ❌(设计) | — |
| onedrive | ✅ | ✅ | ✅ | ✅ | ✅ | — |
| dropbox | ✅ | ✅ | ✅ | ✅ | ✅ | — |
| yike | ✅ | ➖ | ✅ | ➖ | ❌(设计) | — |
| guangya | ✅ | ➖ | page翻页 | ➖ | ❌(设计) | — |
| webdav | ✅ | ➖ | ➖ | ➖ | ❌(设计) | — |
| s3 | ✅ | ✅ | ✅ | ✅ | ❌(设计) | — |

---

### 2.3 下载与视频预览

| Provider | GetDownloadURL | 下载模式 | 并发 | 视频预览 | 转码清晰度 | VIP/会员检测 | 链接过期检测 |
|----------|:--------------:|:--------:|:----:|:--------:|:----------:|:------------:|:------------:|
| pikpak | ✅ | redirect | ➖ | ✅ | ✅ | ✅(10m缓存) | ✅(提前60s) |
| aliopen | ✅ | redirect | ➖ | ✅(原画/Live Photo流) | ❌(设计未接转码) | ➖ | ✅(API expiration) |
| pan123 | ✅ | proxy | 1 | ✅ | ➖ | ➖ | ✅ |
| pan189 | ✅ | proxy | ➖ | ✅(伪预览) | ❌ | ➖ | ✅ |
| pan139 | ✅ | proxy | 1 | ✅ | ➖ | ➖ | ➖ |
| lanzou | ✅ | proxy | 1 | ✅ | ➖ | ➖ | ➖ |
| ilanzou | ✅ | proxy | 1 | ✅ | ➖ | ➖ | ➖ |
| onedrive | ✅ | redirect | ➖ | ✅(新增) | ➖ | ➖ | ➖ |
| dropbox | ✅ | redirect | ➖ | ✅(新增) | ➖ | ➖ | ✅(4h) |
| yike | ✅ | proxy | ➖ | ➖ | ➖ | ➖ | ➖ |
| guangya | ✅ | proxy | ➖ | ✅ | ➖ | ➖ | ➖ |
| webdav | ✅ | 普通连接默认 / Digest proxy | 普通连接随设置 / Digest 1 | ➖ | ➖ | ➖ | ➖ |
| s3 | ✅ | redirect | ➖ | ✅(原画/网页播放器) | ➖ | ➖ | ✅(4h预签名) |

---

### 2.4 上传

| Provider | UploadOneFile | 分片上传 | 整包上传 | 断点续传 | 秒传 | 冲突策略 | 进度回调 | 上传模式 |
|----------|:-------------:|:--------:|:--------:|:--------:|:----:|:--------:|:--------:|:--------:|
| pikpak | ✅ | ➖ | ✅(OSS PUT) | ✅ | ✅(GCID) | ✅(refuse/skip/rename/overwrite) | ✅ | queue |
| aliopen | ✅ | ✅(动态20MiB-5GiB) | ➖ | ✅ | ✅(SHA1/pre_hash) | ✅(refuse/rename/skip/overwrite) | ✅ | queue |
| pan123 | ✅ | ✅(16MB) | ➖ | ✅ | ✅(MD5) | ✅(1/2 映射) | ✅ | queue |
| pan189 | ✅ | ✅(10/20MB) | ➖ | ✅ | ✅(MD5) | ➖ | ✅ | queue |
| pan139 | ✅ | ✅(100/200MB预签名) | ➖ | ✅ | ✅(SHA-256) | ⚠️(服务端auto_rename) | ✅ | queue |
| lanzou | ✅ | ➖ | ✅（V0–V3：100/200/300/550MiB；固定后缀白名单） | ➖ | ➖ | ➖ | ➖ | queue |
| ilanzou | ✅ | ✅(8MB) | ✅(≤8MB) | ➖ | ✅(MD5) | ➖ | ✅ | queue |
| onedrive | ✅ | ✅(10MB session) | ✅(≤4MB) | ✅ | ➖ | ❌(固定rename) | ✅ | queue |
| dropbox | ✅ | ✅(8MB session) | ✅(≤150MB) | ➖ | ➖ | ❌(固定add) | ✅ | queue |
| yike | ✅ | ✅(4MB) | ➖ | ➖ | ✅ | ➖ | ✅ | queue |
| guangya | ✅ | ✅(OSS multipart) | ➖ | ➖ | ✅ | ➖ | ✅ | queue |
| webdav | ✅ | ➖ | ✅(PUT) | ➖ | ➖ | ✅ | ✅ | direct |
| s3 | ✅ | ✅(16MB multipart) | ✅(<64MiB PUT) | ➖ | ➖ | ✅ | ✅ | direct |

> ✅ aliopen/pan123/pan189/onedrive 已持久化上传会话；Dropbox 也保存远端 session 与已确认偏移。
> ✅ webdav/s3 的冲突策略与进度回调已实现（ConflictPolicy + ProgressReader）；S3 64MiB 以上上传自动使用 multipart，重名策略包含 `skip`。

> 2026-09-10 回归：Dropbox 列表/RPC 与上传分片支持 `expired_access_token` 后续期一次再重试；ILanzou 保留大整数 ID、修正下载参数并解析 JSON 下载地址；移动云盘修正账号字段读取及 CDN 开关，并补充短信风控登录分支。上述变更通过自动回归，真实账号成功率尚待实测。天翼 Session HTTP 错误已保留业务错误码，用户报告的 HTTP 400 根因尚未确认。

> 同日补充：移动与天翼均开放独立短信登录入口，不要求先提交密码。天翼按官网 PC 登录脚本使用 `epd` 提交加密凭据，短信发送前按需显示图形验证码；短信码只用于本次认证，不写入账号密码。新增前后端回归通过，仍待真实账号验证。

---

### 2.5 文件操作

| Provider | Mkdir | Rename | Move | Copy | 批量任务轮询 |
|----------|:-----:|:------:|:----:|:----:|:------------:|
| pikpak | ✅ | ✅ | ✅ | ✅ | ✅(waitForTasks) |
| aliopen | ✅ | ✅ | ✅ | ✅ | ➖ |
| pan123 | ✅ | ✅ | ✅ | ❌(设计) | ➖ |
| pan189 | ✅ | ✅ | ✅ | ✅ | ✅ |
| pan139 | ✅ | ✅ | ✅ | ✅ | ➖ |
| lanzou | ✅ | ✅(文件) | ✅(仅文件) | ❌(设计) | ➖ |
| ilanzou | ✅ | ✅ | ✅ | ❌(设计) | ➖ |
| onedrive | ✅ | ✅ | ✅ | ✅ | ✅(monitor轮询) |
| dropbox | ✅ | ✅ | ✅ | ✅ | ➖ |
| yike | ✅(建相册) | ✅(仅相册) | ❌(设计) | ❌(设计) | ➖ |
| guangya | ✅ | ✅ | ✅ | ✅ | ➖ |
| webdav | ✅ | ✅ | ✅ | ✅ | ➖ |
| s3 | ✅ | ✅ | ✅ | ✅ | ➖ |

> ✅ s3 的 Rename/Move/Copy/Delete 均已实现递归处理（listAllUnder + 批量 DeleteObjects + copyRecursive）；大对象复制使用 multipart copy，CopySource 按路径段编码。

---

### 2.6 回收站

| Provider | Trash(移入) | Delete(永久) | Restore(恢复) | ListTrash(查看) | TrashPurge(清空) | TrashClear |
|----------|:-----------:|:------------:|:-------------:|:---------------:|:----------------:|:----------:|
| pikpak | ✅ | ✅ | ✅ | ✅ | ❌(声明已移除) | ❌(声明已移除) |
| aliopen | ✅ | ✅ | ❌(设计) | ❌(设计) | ➖ | ➖ |
| pan123 | ✅ | ✅ | ✅ | ✅ | ❌(声明已移除) | ❌(声明已移除) |
| pan189 | ✅ | ✅(清空回收站) | ❌**缺** | ❌(设计禁用) | ✅(Delete内联) | ✅(Delete内联) |
| pan139 | ✅ | ✅ | ➖(新版接口未验证) | ❌(设计) | ➖ | ➖ |
| lanzou | ➖ | ✅ | ➖ | ➖ | ➖ | ➖ |
| ilanzou | ➖ | ✅ | ➖ | ➖ | ➖ | ➖ |
| onedrive | ➖ | ✅(永久) | ➖ | ➖ | ➖ | ➖ |
| dropbox | ➖ | ✅ | ➖ | ➖ | ➖ | ➖ |
| yike | ➖ | ✅ | ➖ | ➖ | ➖ | ➖ |
| guangya | ➖ | ✅ | ➖ | ➖ | ➖ | ➖ |
| webdav | ➖ | ✅ | ➖ | ➖ | ➖ | ➖ |
| s3 | ➖ | ✅ | ➖ | ➖ | ➖ | ➖ |

> ✅ 能力声明已修正：pikpak/pan123 已将 trashPurge/trashClear 改为不声明。
> ⚠️ pan189 的 `recycleBin:true` 但只支持删除到回收站 + 清空回收站，无法查看/恢复。

---

### 2.7 分享

| Provider | CreateShare | 文件范围 | 有效期 | 密码 | 分享导入(转存) | 本地历史 |
|----------|:-----------:|:--------:|:------:|:----:|:--------------:|:--------:|
| pikpak | ✅ | 多项 | 服务端支持 | 自定义 | ✅ | ✅ |
| aliopen | ✅ | 多项 | 服务端支持 | 自定义 | ✅ | ✅ |
| pan123 | ✅ | 多项 | 服务端支持 | 自定义 | ✅ | ✅ |
| pan189 | ✅（仅个人云） | 单项 | 永久 / 1 / 7 天 | 服务端生成 | ➖ | ✅ |
| pan139 | ✅（仅个人云） | 多项 | 永久 / 1 / 7 天 | 服务端生成 | ➖ | ✅ |
| lanzou | ✅ | 单项 | ➖ | 服务端生成 | ➖ | ✅ |
| ilanzou | ❌ | — | — | — | ➖ | ➖ |
| onedrive | ✅ | 单项 | 受账号/策略限制 | 受账号/策略限制 | ➖ | ✅ |
| dropbox | ✅ | 单项 | 受套餐/策略限制 | 受套餐/策略限制 | ➖ | ✅ |
| yike | ❌ | — | — | — | ➖ | ➖ |
| guangya | ✅ | 多项 | 永久 / 1 / 7 / 30 天 | 自定义 | ➖ | ✅ |
| webdav | ❌ | — | — | — | ➖ | ➖ |
| s3 | ✅（临时预签名） | 单项 | 1 / 7 天 | 不支持 | ➖ | ❌ |

> “本地历史”是本应用保存的已创建记录，不等于服务商提供分享管理 API。S3 链接包含临时签名，不写入历史。
> ✅ pikpak/aliopen/pan123 三个盘均已实现分享导入（ImportShareSession + SaveShare）。
> ✅ 分享页已接入导入入口：仅展示声明 importShare 的账号，解析会话、文件选择和保存目录均绑定目标账号；不支持导入的网盘不会显示入口。
> ✅ dropbox 密码分享的 `requested_visibility` 已修复为 `password`。

---

### 2.8 云离线下载

| Provider | 能力声明 | 提交任务 | 任务列表 | 进度查询 | 任务删除 |
|----------|:--------:|:--------:|:--------:|:--------:|:--------:|
| pikpak | ✅ | ✅ | ✅ | ✅(RefreshOffline) | ✅(DeleteOffline) |
| 其余 12 盘 | ❌(设计) | ➖ | ➖ | ➖ | ➖ |

> ✅ pikpak 的离线下载已实现进度查询和任务删除（OfflineFind + OfflineDelete）；列表使用 `limit=10000`、phase 过滤和 `page_token` 分页，不会因固定 100 条上限漏查任务。

---

### 2.9 收藏与哈希

| Provider | Favorite | ProvideHashes | RapidUploadHashes | ResolveTransferHash |
|----------|:--------:|:-------------:|:-----------------:|:-------------------:|
| pikpak | ✅ | ❌(未声明) | ❌(未声明) | ➖ |
| aliopen | ✅ 云端 | ✅(sha1) | ✅(sha1) | ✅ |
| pan123 | ✅ 云端 | ✅(md5) | ✅(md5) | ✅ |
| pan189 | ➖ | ✅(md5) | ✅(md5) | ✅ |
| pan139 | ➖ | ✅(sha256) | ✅(sha256) | ✅ |
| lanzou | ➖ | ❌ | ❌ | ➖ |
| ilanzou | ➖ | ✅(md5) | ✅(md5) | ✅ |
| onedrive | ✅ 工作/学校；个人为本地 | ✅(sha1/quickXor) | ➖ | ➖ |
| dropbox | ➖ | ✅(dropbox) | ➖ | ➖ |
| yike | ➖ | ✅(md5) | ➖ | ➖ |
| guangya | ➖ | ✅(md5) | ➖ | ✅ |
| webdav | ➖ | ➖ | ➖ | ➖ |
| s3 | ➖ | ➖ | ➖ | ➖ |

> ✅ ilanzou 已实现 `RapidUploadByHash` + `ResolveTransferHash`，并与 onedrive/dropbox 的哈希声明一起纳入跨盘秒传能力；pikpak 通过 GCID 实现秒传但未声明 hash 类型。

#### 收藏逐盘核查（2026-09-10）

上表 `Favorite` 表示原生接口能力，➖ 不表示 UI 无收藏：未接入原生收藏的网盘统一使用本地 `favorites.json`。本轮依据官方接口文档、官方网页公开脚本和本仓库驱动核查；测试使用模拟 HTTP，没有用真实账号进行云端写入验证。

| 网盘 | 当前收藏方式 | 接口依据与适用范围 |
|---|---|---|
| PikPak | 云端 | 全局文件列表 `parent_id=*` + `starred` 筛选，`files:star` / `files:unstar`；读取全部分页。 |
| OneDrive | 工作/学校云端；个人本地 | Graph `driveType` 判断账号类型；`following` / `follow` / `unfollow`。仅显示当前挂载空间可寻址的收藏，排除其他共享空间条目。未知类型、权限或网络错误直接提示，不当作“不支持”。[官方权限与接口](https://learn.microsoft.com/en-us/graph/api/driveitem-follow?view=graph-rest-1.0)。 |
| Dropbox | 本地 | Dropbox 有产品星标，但公开 API 未提供对应读写能力。[官方开发者支持答复](https://www.dropboxforum.com/discussions/101000014/how-can-i-get-the-starred-files-via-the-api/483041)。 |
| 123 云盘 | 云端 | 官方网页 `restful/goapi/v1/file/starred/list` 与 `restful/goapi/v1/file/starred`；`starredStatus=255/1`；按 `page`、`next` 读取全部分页，保留大整数文件 ID。[官方网页](https://yun.123pan.cn/)。 |
| 蓝奏云 | 本地 | 当前驱动及公开网页未找到可完整读写的原生文件收藏接口；不推测私有端点。[官网](https://www.lanzou.com/)。 |
| 蓝奏优享 | 本地 | 当前接口与网页未找到原生文件收藏读写路径。[官网](https://www.ilanzou.com/)。 |
| 移动云盘 | 本地 | 当前个人云接口与网页未找到完整文件收藏读写路径；`star` 字段、“我的应用收藏”不单独作为原生收藏依据。[官方网页](https://yun.139.com/w/)。 |
| 天翼云盘 | 本地 | 个人云与家庭云当前接入接口均未确认原生文件收藏读写能力。[官方网页](https://cloud.189.cn/web/main/)。 |
| 一刻相册 | 本地，暂不适配云端 | 按用户要求，本轮不核查或接入云端收藏。 |
| 阿里云盘 Open | 云端 | 官方 `openFile/starredList` + `openFile/update` 的 `starred` 参数；备份盘与资源库分别分页读取，以 `b:`/`r:` 保持文件身份。[列表文档](https://www.yuque.com/aliyundrive/zpfszx/zqkqp6)、[更新文档](https://www.yuque.com/aliyundrive/zpfszx/dp9gn443hh8oksgd)。 |
| 光鸭云盘 | 本地 | 当前文件接口与网页未找到原生文件收藏读写能力；最近文件、云收藏转存任务不等同文件收藏。[官方网页](https://www.guangyapan.com/)。 |
| WebDAV | 本地 | 通用 WebDAV 没有跨服务器统一的用户收藏语义；不写私有扩展属性。 |
| S3 | 本地 | 通用 S3 没有统一文件收藏语义；对象标签不作为收藏，不改写对象元数据。 |

统一行为：

- 前端只调用 `AddFavorite` / `RemoveFavorite`，后端通过 `drive.RemoteFavorites` 判断支持情况，不添加中央 provider 分支。
- 云端操作失败会返回错误，保留原有本地记录；分页失败、重复游标、异常列表不能替换完整快照。不同账号与空间的记录隔离。
- 原生收藏与旧本地收藏合并展示，相同文件以云端为准；云端取消后会清除相应云端快照。旧本地收藏及备份导入项继续保留，移除纯本地项不触发云端写操作。
- 保留收藏时间和文件大小、父目录、文件类型等信息，剔除下载和缩略图临时链接。侧边栏提示该条目是云端还是本地收藏。
- 偏好导出读取本地快照，不要求所有账号在线；`RestoreFavorite` 将备份合并为本地收藏，不覆盖已有记录、不回放云端写操作。
- 回归覆盖原生接口请求、分页、双盘作用域、所有本地回退、存储隔离、备份兼容、前端重复调用和账号切换。

---

## 三、关键差距汇总（按优先级）

### 🔴 P0 — 影响功能正确性

| # | 差距 | 影响范围 | 状态 | 详情 |
|---|------|---------|:----:|------|
| 1 | onedrive/dropbox `RefreshAccount` 完全缺失 | 2 盘 | ✅已修复 | 已实现 token 刷新 + 账号信息/配额；缺省 `expires_in` 时保留旧值 |
| 2 | 分享导入 `importShare` 声明未实现 | 3 盘(pikpak/aliopen/pan123) | ✅已修复 | ShareImportDriver 接口 + 三个盘完整实现 |
| 3 | pikpak API captcha token 续接不完整 | 1 盘 | ✅已修复 | 按设备/账号/action 缓存 token，失败时 previousToken 换发并自动重试 |
| 4 | aliopen `CompleteUpload` 传空 `upload_id` | 1 盘 | ✅已修复 | 传真实 upload_id |
| 5 | s3 目录递归操作完全缺失 | 1 盘 | ✅已修复 | listAllUnder + 批量 DeleteObjects + copyRecursive |
| 6 | s3 `forcePathStyle` 硬编码不可配置 | 1 盘 | ✅已修复 | 改为 *bool 可配置，支持 sessionToken |

### 🟡 P1 — 影响健壮性

| # | 差距 | 影响范围 | 状态 | 详情 |
|---|------|---------|:----:|------|
| 7 | 上传断点续传持久化缺失 | 4 盘 | ✅已修复 | aliopen/pan123/pan189/onedrive 已实现 session 存储 |
| 8 | webdav/s3 上传冲突策略完全缺失 | 2 盘 | ✅已修复 | refuse/rename/overwrite + ConflictPolicy 字段 |
| 9 | 能力声明与实现不符 | 多盘 | ✅已修复 | pikpak/pan123 的 trashPurge/trashClear/playbackHistory 改为 false |
| 10 | 哈希能力声明缺失 | 4 盘 | ✅已修复 | ilanzou/onedrive/dropbox 已声明 SetHashes |
| 11 | pikpak 离线下载进度/删除缺失 | 1 盘 | ✅已修复 | RefreshOfflineTasks + DeleteOfflineTask 绑定 |
| 12 | pan139 ListPage 分页参数未推进 | 1 盘 | ✅已修复 | 改用新版 `pageInfo.pageCursor`，并防重复游标 |
| 13 | onedrive/dropbox 搜索无分页 | 2 盘 | ✅已修复 | 跟随 nextLink/cursor 分页 |
| 14 | pikpak batch 操作无任务轮询 | 1 盘 | ✅已修复 | waitForTasks 60s 超时轮询 |

### 🟢 P2 — 体验优化

| # | 差距 | 影响范围 | 详情 |
|---|------|---------|------|
| 15 | 限速/重试缺失 | 多盘 | aliopen 已覆盖并发限速、401 刷新和 429 退避；pikpak 已覆盖登录/API 429 冷却识别；Dropbox 已支持 429/Retry-After |
| 16 | 版本历史缺失 | 2 盘 | onedrive/dropbox 当前驱动未提供版本历史/恢复能力 |
| 17 | 缩略图缺失 | 1 盘 | dropbox 当前 `mapItem` 未填充缩略图 |
| 18 | 上传进度回调已实现 | 2 盘 | ✅ webdav/s3 已实现 ProgressReader + 令牌桶限速（`progress.go:22`） |
| 19 | yike decryptYikeMd5 | 1 盘 | ✅已实现；yike 按需求不参与跨盘秒传能力路由 |

---

## 四、2026-09-10 最终回归与验收边界

- 13 个网盘均纳入驱动和端到端模拟测试回归；各盘测试深度不同，不等于所有账号、套餐、风控状态及全部互传组合已经真实验证。
- 跨盘互传补齐流式上传提前返回时的退出处理、下载中断错误透传、非完整下载响应拒绝和普通上传同名保护；覆盖完整内容传递、取消、恢复检查点及哈希能力选择。123 上传取消、缺少目标文件 ID 的普通上传和秒传响应均按失败处理。
- PikPak 使用真实账号只读验证下载和转码播放，并在真实浏览器验证跳转至 600、120、2000 秒后继续解码。上传内容与分享协议使用模拟服务验证，未向真实账号写入测试文件或创建公开分享。
- 移动短信登录修复错误码误分类、失败发送冷却和云盘 SSO 失败后的会话续接；天翼短信错误保留服务端信息，并补齐 refresh_token 续期。天翼用户报告的短信提交后“刷新页面后重试”仍未获得真实成功验证，不能标记为已解决。
- 前端回归覆盖预览异步结果隔离、清晰度切换、播放进度、分享导入目标账号、同步列表状态及设置读取失败保护。视频、图片和音频预览已调整；PDF、Markdown、文本预览的完整视觉重做尚未完成。
- 本次以代码审查、已发现缺陷修复和自动回归作为审查阶段结束标准；保留上述实测与设计限制，不作“所有功能零缺陷”的保证。

后续功能更新建议优先考虑传输完成后的可选内容校验、可脱敏导出的诊断报告和剩余预览界面统一。它们尚未作为本轮新功能实现。

## 五、文档导航

各网盘详细功能调研与 file:line 证据见：
- [docs/providers/pikpak.md](providers/pikpak.md)
- [docs/providers/aliopen.md](providers/aliopen.md)
- [docs/providers/pan123.md](providers/pan123.md)
- [docs/providers/pan189.md](providers/pan189.md)
- [docs/providers/pan139.md](providers/pan139.md)
- [docs/providers/lanzou.md](providers/lanzou.md)
- [docs/providers/ilanzou.md](providers/ilanzou.md)
- [docs/providers/onedrive.md](providers/onedrive.md)
- [docs/providers/dropbox.md](providers/dropbox.md)
- [docs/providers/yike.md](providers/yike.md)
- [docs/providers/guangya.md](providers/guangya.md)
- [docs/providers/webdav.md](providers/webdav.md)
- [docs/providers/s3.md](providers/s3.md)
- [分享能力汇总](SHARE_CAPABILITY_AUDIT_2026-08.md)
