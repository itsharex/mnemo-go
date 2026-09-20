# Mnemo-Go 前端 UI/UX 焕新计划

> 状态：首轮换新已完成（见「实施记录」）
> 适用范围：`frontend/` 以及与预览窗口直接相关的前端资源
> 目标：在保留桌面文件管理器效率、可访问性和性能的前提下，完成图标系统升级、高级交互动画融合，以及预览窗口的局部视觉焕新。

---

## 1. 背景与目标

当前前端已经具备完整的自研设计基础：

- Vue 3.5 + Vite 6；
- `design-tokens.css` 统一管理颜色、圆角、阴影和动效；
- `UiIcon.vue` 作为全局图标入口；
- Modal、Select、ContextMenu、Toast、TransitionGroup 等通用交互；
- 明暗主题和 OLED 模式；
- 文件列表/网格、虚拟化、双栏工作区、拖拽、播放器和图片预览；
- 键盘导航、ARIA 标记、焦点管理和 `prefers-reduced-motion` 支持。

本次焕新不采用“推倒重做”路线，而采用增量改造：

1. **全局图标必须完成升级**：以 Lucide 风格/组件为新的通用 UI 图标来源，保留 `UiIcon` 作为 Mnemo 的统一适配层；
2. **高级交互动画正式引入**：使用 Motion for Vue 处理布局变化、列表重排、跨区域移动和复杂状态编排；常规 hover/focus/颜色过渡仍使用 CSS；
3. **预览窗口重点焕新**：将 Aceternity 风格的局部视觉灵感应用到图片、视频、音频、代码/Markdown 等预览窗口，形成统一的沉浸式媒体体验；
4. **保持产品属性**：Mnemo 是桌面文件管理器，不是营销官网。动效和装饰必须服务于状态理解、空间层级和内容聚焦。

---

## 2. 非目标与约束

本计划明确不做以下事项：

- 不引入 React/Next.js 组件体系；
- 不为了 Aceternity UI 引入 Tailwind 并重写现有样式系统；
- 不直接把 Aceternity UI 当作基础组件库；
- 不将所有 CSS Transition 替换为 JS 动画；
- 不把网盘品牌 Logo 替换成 Lucide 图标；
- 不在文件列表、大规模传输列表或播放器高频更新区域堆叠动画；
- 不牺牲键盘操作、焦点管理、屏幕阅读器语义和 reduced-motion 支持；
- 不修改后端 API、Provider 能力模型或传输业务逻辑。

---

## 3. 目标技术方案

### 3.1 技术角色分工

| 技术 | 在项目中的角色 | 使用范围 |
|---|---|---|
| Lucide | 通用 UI 图标来源 | 工具栏、菜单、表单、状态、窗口控制、播放器控制 |
| `UiIcon.vue` | Mnemo 图标适配层 | 统一名称、尺寸、颜色、ARIA 和 fallback |
| Motion for Vue | 高级交互和布局动画 | 布局重排、列表变更、弹层编排、拖拽反馈、预览窗口过渡 |
| CSS Transition/Animation | 基础状态过渡 | hover、focus、pressed、颜色、透明度、轻量显隐 |
| Aceternity 风格 | 局部视觉参考 | 预览窗口、空状态、加载/解码反馈、媒体控制层 |
| `design-tokens.css` | 唯一设计事实源 | 颜色、表面、阴影、圆角、动效时长和曲线 |

### 3.2 依赖规划

建议依赖：

```json
{
  "dependencies": {
    "motion-v": "<锁定经过验证的兼容版本>",
    "@lucide/vue": "<锁定经过验证的兼容版本>"
  }
}
```

实际安装前必须确认：

- Vue 3.5 兼容性；
- Vite 6 构建兼容性；
- Wails WebView2 运行兼容性；
- 是否影响前端构建体积；
- Lucide 包名和当前稳定 API；
- Motion 的 reduced-motion 配置方式。

版本必须写入 `package-lock.json`，禁止使用未锁定的浮动依赖。

---

## 4. 图标系统翻新计划

### 4.1 总体策略

保留现有调用方式：

```vue
<UiIcon name="search" :size="16" />
```

不在业务模板中大面积改成：

```vue
<Search :size="16" />
```

原因：

- 避免 20+ 个业务文件同时迁移；
- 保留现有测试中的 `UiIcon` stub；
- 保留 Mnemo 自定义图标名称；
- 继续统一处理 `aria-hidden`、尺寸、stroke、fallback；
- 未来仍可替换图标实现。

### 4.2 图标分类

#### A. Lucide 通用图标

优先替换：

- `search`
- `refresh-cw`
- `settings`
- `plus`
- `upload`
- `download`
- `share-2`
- `copy`
- `move`
- `trash-2`
- `star`
- `folder`
- `file`
- `image`
- `video`
- `music`
- `archive`
- `chevron-*`
- `arrow-*`
- `check`
- `x`
- `info`
- `triangle-alert`
- `sun`
- `moon`
- `monitor`
- 窗口最小化、最大化、还原、关闭图标

#### B. Mnemo 自定义图标

保留或独立维护：

- Mnemo 品牌 Logo；
- 网盘 Provider Logo；
- 文件类型特殊图标；
- 播放器中具有产品语义的组合图标；
- Lucide 不覆盖的特定业务图标。

### 4.3 `UiIcon.vue` 改造要求

`UiIcon.vue` 应改造成图标注册表：

```js
const ICONS = {
  search: Search,
  folder: Folder,
  upload: Upload,
}
```

要求：

- 对外仍接受当前 `name` 和 `size`；
- 保持 `currentColor`；
- 保持 `aria-hidden="true"`；
- 标准图标统一 stroke width；
- 未注册名称继续使用安全 fallback；
- 支持业务层传入 class/style；
- 不改变网盘 Logo 的资源加载方式；
- 建立名称映射表，避免 Lucide 名称直接泄漏到业务层。

### 4.4 图标验收标准

- 所有通用 UI 图标风格统一；
- 不再出现 Emoji 图标；
- 图标在浅色、深色、OLED 下均有足够对比度；
- 16px、18px、20px、24px 下线条清晰；
- 图标不会挤压按钮文字或改变工具栏布局；
- 现有单元测试和工作区测试不因图标替换失败；
- 构建产物不包含未使用的大量图标模块；
- 网盘品牌 Logo 不被误替换。

---

## 5. 高级交互动画计划

### 5.1 动画分层

#### 第一层：CSS 基础过渡

继续使用 CSS：

- hover；
- focus-visible；
- pressed；
- 颜色变化；
- 边框和阴影变化；
- 轻量透明度变化；
- 普通按钮反馈。

#### 第二层：Motion 高级动画

使用 Motion：

- layout animation；
- 列表重排；
- 多元素编排；
- 跨面板移动；
- 拖拽目标反馈；
- 弹层和内容的联动进入；
- 预览窗口的舞台切换；
- 任务状态从进行中转为完成/失败的结构变化。

### 5.2 动效设计规则

- 动效必须表达状态、层级、方向或因果关系；
- 不为装饰而装饰；
- 页面切换一般控制在 180–360ms；
- 点击反馈一般控制在 100–180ms；
- 弹层进入一般控制在 220–420ms；
- 大列表禁止逐项长时间入场；
- 传输进度不使用逐帧 JS 动画；
- 播放器进度、音量和时间显示不接入复杂布局动画；
- 用户开启 reduced-motion 时，所有位移、缩放、光晕和级联动画必须关闭或降级为淡入淡出。

### 5.3 优先改造场景

#### P0：预览窗口

- 图片切换；
- 视频/音频控制层显隐；
- 预览窗口打开和关闭；
- 加载、解码、错误和重试状态；
- 缩略图选中；
- 播放列表切换；
- 预览内容与控制层之间的空间关系。

#### P1：QuickOpen 与 GlobalSearch

- 面板进入；
- 搜索结果替换；
- 键盘高亮项移动；
- 结果分组展开；
- 无结果到有结果的状态变化。

#### P1：传输任务

- 新任务插入；
- 任务状态变更；
- 完成/失败状态替换；
- 任务分组展开和折叠。

#### P2：双栏工作区

- 当前焦点栏强调；
- 拖拽文件跨栏移动；
- 目标栏/目标文件夹高亮；
- 复制、移动、迁移提交后的反馈。

#### P2：账号栏

- 保留当前已有的拖拽排序、FLIP 和果冻碰撞；
- Motion 只负责可验证的布局编排；
- 不重复实现现有成熟的 CSS 动效。

### 5.4 动画适配层

建议新增轻量工具或组件层，例如：

- `frontend/src/motion/variants.js`
- `frontend/src/motion/reducedMotion.js`
- `frontend/src/components/MotionPresence.vue`

要求：

- 统一动画时长和 easing；
- 统一读取 CSS Token 或对应 JS 常量；
- 统一 reduced-motion 判断；
- 业务组件不直接散落大量 Motion 参数；
- 不把动画状态写入后端或持久化偏好。

---

## 6. 预览窗口焕新计划

预览窗口是本次局部视觉升级的重点。目标不是增加装饰，而是让预览具备统一的沉浸式舞台、明确的加载状态和稳定的控制层。

涉及组件：

- `frontend/src/components/PreviewModal.vue`
- `frontend/src/components/PlayerPanel.vue`
- `frontend/src/PreviewWindow.vue`
- 预览相关 player 模块和样式

### 6.1 统一预览窗口视觉语言

统一采用：

- 深色内容舞台；
- 内容优先；
- 控制层渐变遮罩；
- 半透明浮层；
- 轻量紫色光晕，仅用于焦点和状态；
- 细边框和柔和阴影；
- 控件组采用统一间距和图标尺寸；
- 控制层闲置后自动弱化或隐藏；
- 错误和加载反馈具备明确语义。

### 6.2 图片预览窗口

重点优化：

- 打开/关闭时的舞台级淡入淡出；
- 图片必须解码完成后再切换主画面；
- 当前图、上一张、下一张的切换使用层叠 crossfade；
- 缩略图胶卷使用 Motion layout 高亮；
- 光标锚定缩放继续保留；
- 双击适配/200% 继续保留；
- 控制条进入采用轻微位移和透明度组合；
- 缩放、旋转、翻页不出现白屏或布局跳动；
- 小图不强制拉伸到模糊状态。

可借鉴的局部视觉效果：

- 控制条顶部的柔和高光；
- 当前缩略图的 gradient border；
- 加载阶段的细微光晕；
- 图片解码完成后的低强度 reveal。

禁止：

- 影响图片色彩判断的强背景渐变；
- 过度发光；
- 图片翻页时的 3D 旋转；
- 会导致高分辨率图片掉帧的滤镜链。

### 6.3 视频预览窗口

重点优化：

- 视频舞台与控制栏层级；
- 控制栏出现/隐藏的方向感；
- 播放、暂停、快进、快退的短促反馈；
- 清晰度、字幕、音轨和更多菜单的弹层过渡；
- 错误/加载/缓冲状态；
- 播放列表切换；
- 全屏和窗口模式切换。

规则：

- 视频元素本身不使用复杂滤镜动画；
- 控制层动画不能影响视频解码；
- 播放进度条使用 CSS 或原生高性能更新；
- 只对控制层、OSD 和菜单使用 Motion；
- 全屏时保留窗口控制逻辑和键盘快捷键。

### 6.4 音频预览窗口

重点优化：

- 专辑/文件封面舞台；
- 播放状态光晕；
- 频谱或均衡器视觉反馈；
- 播放列表切换；
- 播放/暂停和上一首/下一首。

规则：

- 音频可使用低频率、低幅度的状态呼吸效果；
- 不使用持续大面积粒子和高频背景动画；
- 无封面时使用统一的 Mnemo 音频占位视觉；
- reduced-motion 下静态显示播放状态。

### 6.5 代码和 Markdown 预览

重点优化：

- 顶部文件信息和语言标识；
- 复制、换行、主题切换等工具按钮；
- 加载、解析失败、超长内容提示；
- 内容切换时的轻量淡入；
- 保持文本选择、复制和滚动性能。

禁止：

- 对代码文本逐行入场；
- 对大文档使用复杂 blur/backdrop-filter；
- 改变现有文本选择行为。

### 6.6 预览窗口统一状态

所有预览类型统一支持：

- loading；
- ready；
- empty；
- error；
- retry；
- closing；
- reduced-motion。

建议抽象统一状态视觉，但不强制将所有预览组件合并成一个巨型组件。

---

## 7. 实施阶段

### 阶段 0：基线冻结与依赖验证

交付：

- 记录当前构建、测试和关键工作区行为；
- 确认 Motion for Vue 的实际包名和 Vue 3.5 兼容性；
- 确认 Lucide Vue 包的实际稳定 API；
- 确认 Wails WebView2 下的动画表现；
- 建立图标名称映射表；
- 确认现有未提交改动不被覆盖。

门槛：

- 不允许在基线不清楚时批量替换图标或重写预览样式。

### 阶段 1：Lucide 图标系统升级

主要文件：

- `frontend/package.json`
- `frontend/package-lock.json`
- `frontend/src/components/UiIcon.vue`
- 必要时新增图标映射测试

交付：

- 通用 UI 图标迁移至 Lucide；
- `UiIcon` 对外 API 不变；
- 品牌图标继续使用现有资源；
- 完成浅色/深色/OLED 视觉核验。

### 阶段 2：Motion 基础设施和高级交互

主要文件：

- `frontend/src/motion/`
- `frontend/src/components/QuickOpen.vue`
- `frontend/src/components/GlobalSearch.vue`
- `frontend/src/views/TransferView.vue`
- `frontend/src/views/WorkspaceView.vue`

交付：

- 建立统一动画配置；
- 完成 QuickOpen/搜索/传输任务的小范围迁移；
- 保留 CSS 作为基础交互；
- 完成 reduced-motion 降级。

### 阶段 3：预览窗口视觉焕新

主要文件：

- `frontend/src/components/PreviewModal.vue`
- `frontend/src/components/PlayerPanel.vue`
- `frontend/src/PreviewWindow.vue`
- 相关 player 样式和模块

交付：

- 图片预览舞台和控制层升级；
- 视频控制层和 OSD 升级；
- 音频预览状态升级；
- 代码/Markdown 预览工具栏统一；
- 所有预览窗口风格统一。

### 阶段 4：质量收口

交付：

- 清理失效图标映射；
- 清理重复动效；
- 检查大列表和播放器性能；
- 完成可访问性和 reduced-motion 验证；
- 更新 `docs/DESIGN.md` 与本计划的实际落地状态；
- 输出变更说明和已知限制。

---

## 7.1 实施记录（2026-09-20）

已完成：

- 锁定 `@lucide/vue@1.34.0`、`motion-v@2.4.4` 与兼容 CI Node 20 的 `@vueuse/core@14.1.0`，版本已写入 `package-lock.json`；
- `UiIcon.vue` 改为 Lucide 按需导入的内部映射，保留原有 `name` / `size` API、`currentColor`、ARIA 与未知名称 fallback；
- 增加 `UiIcon` 映射测试，覆盖标准图标、样式透传与 fallback；
- 建立 `frontend/src/motion/presets.js`，统一面板动效与 `reduced-motion` 配置；
- 完成登录、QuickOpen、全局搜索、传输中心和预览窗口提示的轻量入场/状态过渡；
- 完成双栏工作区的焦点强化、跨栏操作控件与拖放反馈；该主路径采用 CSS 过渡，避免将 Motion 运行时打入首屏；
- 登录服务栏移除宣传性说明，保留直接、高密度的服务选择；设置页和空状态移除非必要的次级说明；
- 传输中心导航改为语义化按钮/标签结构；任务行维持 CSS 基础反馈，不追加逐项 Motion 动画；
- 图标映射补齐传输错误提示使用的 `alert` 名称，避免回退为文件图标。

验证记录：

- `npm test -- --run`：7 个测试文件、93 项测试通过；
- `npm run build`：通过。HLS/DASH 媒体依赖仍有既有的大 chunk 提示，未由本次改造引入；
- 按用户要求，本轮不进行浏览器截图、DevTools 或浏览器工作区验收。

待后续增量处理：

- 真实 Wails 环境的人工视觉验收（需用户另行安排）；
- 视频播放器控制层的进一步细节收敛（当前已有沉浸式舞台、自动隐藏控制层、OSD、字幕、播放列表和错误重试，不重复叠加高频动画）。

---

## 8. 验收标准

### 8.1 功能验收

- 所有主页面可正常切换；
- 网盘列表、网格、双栏、拖拽、上传、下载、移动、复制不受影响；
- 快捷键和右键菜单不受影响；
- 弹窗 Esc、焦点恢复和 Tab 循环正常；
- 图片、视频、音频、代码和 Markdown 预览正常；
- Wails 窗口最小化、最大化、关闭正常。

### 8.2 视觉验收

- 通用 UI 图标采用统一 Lucide 风格；
- 网盘品牌 Logo 保持品牌识别；
- Light/Dark/OLED 三种模式视觉一致；
- 预览窗口具有统一的舞台、控制层和状态反馈；
- 不出现明显闪白、跳动、布局抖动或遮挡内容；
- Aceternity 风格只作为局部增强，不破坏信息密度。

### 8.3 动效验收

- 高级动画不会覆盖业务状态；
- 列表数量较大时滚动保持稳定；
- 播放器播放和拖动进度不掉帧；
- 窗口快速打开/关闭不会残留遮罩或事件监听；
- `prefers-reduced-motion: reduce` 下动画得到合理降级；
- 不存在无限循环且无意义的高频动画。

### 8.4 工程验收

- `npm run build` 通过；
- `npm test` 通过；
- 相关前端 LSP diagnostics 无新增错误；
- `git diff` 仅包含焕新范围内的文件；
- 依赖版本锁定；
- 不引入 React 或未必要的 Tailwind 体系；
- 不修改后端 Provider 和传输契约。

---

## 9. 风险与应对

| 风险 | 应对方案 |
|---|---|
| Lucide 图标与现有尺寸不一致 | 统一由 `UiIcon` 控制 size、stroke 和 alignment |
| Motion 与 Vue Transition 冲突 | 同一元素只保留一个主动画驱动方；建立迁移清单 |
| 大列表掉帧 | 禁止逐项复杂动画；只对可见项和结构容器做动画 |
| 播放器掉帧 | 不动画 video 元素和高频进度状态；只动画控制层 |
| 深色模式光晕过亮 | 所有视觉效果使用 Token 和主题专属上限 |
| reduced-motion 遗漏 | 建立全局 Motion 配置和手动验收矩阵 |
| Aceternity 风格过度 | 限制在预览窗口和少量状态/空状态区域 |
| Wails WebView 行为差异 | 浏览器预览和实际 Wails 窗口均验证 |
| 依赖包体积增加 | tree-shaking、按需导入、构建产物对比 |
| 现有测试 stub 失效 | 保留 `UiIcon` 组件边界，不改业务调用方式 |

---

## 10. 首批任务清单

1. 确认并锁定 Motion for Vue 和 Lucide Vue 的实际依赖版本；
2. 为 `UiIcon.vue` 建立通用图标名称映射表；
3. 统计当前所有 `UiIcon` 名称并标记标准图标、业务图标、品牌图标；
4. 完成 `UiIcon.vue` 的 Lucide 内部实现；
5. 建立 Motion 的 reduced-motion 和通用 transition 配置；
6. 选择 QuickOpen、TransferView、PreviewModal 作为首批 PoC；
7. 先完成图片预览窗口的舞台与控制层视觉方案；
8. 再迁移视频和音频预览窗口；
9. 对 Light/Dark/OLED、浏览器预览、Wails WebView2 分别验收；
10. 最后再扩展到双栏工作区和其他页面。

---

## 11. 最终设计原则

本次焕新最终遵循：

> **Lucide 负责统一图标，Motion 负责表达空间和状态，Aceternity 风格负责预览窗口的局部氛围，Mnemo 自有设计 Token 负责统一一切。**

最终效果应当是：

- 图标更一致；
- 操作反馈更明确；
- 页面转换更有空间感；
- 预览窗口更沉浸；
- 文件管理效率不下降；
- 长时间使用不疲劳；
- 动效有物理质感但不喧宾夺主。
