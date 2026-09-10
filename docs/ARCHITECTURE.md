# Mnemo-Go 架构

## 分层

```
frontend (Vue3, webview)
   │  Wails Binding (Go 方法直调 + Events.Emit 事件推送)
   ▼
internal/app      绑定层：账号/网盘/传输/播放/同步/设置
   ▼
internal/drive    ops 门面（唯一业务入口，禁止中央 if provider）
   ├─ registry / capabilities / meta
   └─ providers/  13 个插件包（pikpak/onedrive/.../webdav/s3）
internal/transfer  原生 Go 分段下载器 · 上传队列 · 跨盘迁移
internal/sync      双向同步
internal/preview   本地 Range/播放会话代理（鉴权流、HLS/DASH 与字幕）
internal/store     本地持久化（原子 JSON，多集合；读改写由 Store 互斥保护）
internal/netx      HTTP/上传/哈希/限速 工具
```

## 核心不变量

1. **插件化**：新增网盘 = 新增一个 `internal/drive/providers/<id>` 包，
   实现 `drive.Driver` 接口，`init()` 调用 `drive.Register(...)`；
   主程序只通过 blank import 装配。禁止在门面或 UI 里写 `if provider == ...`。
2. **能力裁剪**：每个 provider 声明 `Capabilities`（能力位），UI 菜单按位裁剪。
3. **统一模型**：所有盘输出 `model.File`；列表/搜索/回收站共用同一前端组件。
4. **认证分离**：`store` 只存账号凭据，刷新由各 provider `RefreshAccount` 负责，
   凭据以 `tokenfrom/user_id` 命名空间隔离。
5. **事件优先、限频轮询兜底**：任务进度和应用内状态变化优先通过 `Events.Emit` 推送；容量、云离线任务等无法稳定推送的外部状态使用去重、缓存和页面可见性保护后的低频轮询。

## 数据流

| 场景 | 路径 |
|------|------|
| 登录 | 前端 → app.Login* → provider.Auth → store.SaveAccount → 事件 account:changed |
| 列表 | app.ListDir → drive ops → driver.List → model.File[] |
| 下载 | app.Download → transfer/manager → driver.GetDownloadURL → dlengine 分段下载 |
| 上传 | app.Upload → transfer/upload → driver.UploadOneFile（queue/direct 按能力） |
| 播放 | app.PlayVideo → driver.GetVideoPreview → preview 播放会话代理 → HTML5 `<video>`；HLS/DASH 由按需加载的 HLS.js/dash.js 驱动 |
| 迁移 | app.Migrate → transfer/migrate（server/stream/spool 策略） |

## 依赖方向

`app → drive → netx/model/store`；`providers → drive/netx/model`；无反向依赖。
provider 之间互相独立，只能通过 `drive` 注册表暴露能力。

## 工作区增强

- `WorkspaceView.vue` 组合两个 `PanView` 实例，隔离目录位置、搜索输入、选中状态与键盘事件。原生拖入统一注册，通过落点路由到可见目标栏。盘内复制走 `drive.Copy`，跨账号复制进入迁移队列。
- `GlobalSearch.vue` 先检索持久化目录缓存，按需用两个并发请求调用支持搜索的账号。缓存结果保留账号、盘、父目录和更新时间；缓存结果最多 1000 项，在线每账号最多展示 500 项。停止搜索后丢弃过期结果，不继续排队请求。
- `workspace.js` 提供分享文案解析、账号状态和备份白名单。备份采用版本化 JSON，包含偏好与本地收藏；不读取或导出账号 token。恢复是逐项合并，失败会提示部分恢复并允许重试。
- `internal/sync/plan.go` 让预览与执行共用差异计划。执行前重新扫描并验证计划摘要，快照绑定账号、目录与方向；首次双向同步的同名文件按冲突处理。默认保留两份，也可选择本地或网盘。检测主要基于大小、时间与服务商提供的哈希元数据，不是全量内容扫描。
- 同步下载先写临时文件再替换，上传前后核对本地文件状态；删除数量超过有效快照一半时停止。旧快照没有作用范围信息时不会用于自动传播删除。
- 迁移 `items` 保存每个资源的结果与错误；目标接口返回明确文件 ID 时可按需比较目标大小和共同哈希，接口不返回 ID 时明确标为未校验。流式传输检查字节数，失败回退临时文件；校验操作不修改迁移检查点。

- 同步下载在替换前再次核对本地文件是否仍与计划一致，包括计划中不存在、下载期间新建的文件。远端哈希变化参与删除冲突判定；Windows 特殊路径及同步临时文件名不进入同步计划。
- 移动式迁移清理来源前重新读取元数据，目录必须已经清空；复制期间新增或修改的内容保留在来源，任务报告部分完成。恢复记录无法落盘时不删除来源。各网盘的元数据精度和条件删除能力不同，这些检查不能替代服务端事务锁。

## 工作区验证

前端运行 `npm test`，后端运行 `go test ./...`。`frontend/workspace.browser.cjs` 用 Edge 无头浏览器和本地 mock 验证 20 账号、10000 文件、双栏焦点、分享、同步、搜索和备份。需要可用的 Playwright 模块；通过 `PLAYWRIGHT_MODULE` 指定安装位置，`QA_URL` 指定 Vite 地址，`QA_OUTPUT` 指定截图目录。脚本不连接真实网盘。
