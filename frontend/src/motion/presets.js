// Motion 的统一入口：只服务结构变化，基础 hover/focus 仍由 CSS Token 驱动。
// `user` 会遵从 prefers-reduced-motion，自动关闭位移与布局动画。
export const reducedMotion = 'user'

export const panelReveal = Object.freeze({
  initial: { opacity: 0, y: 8, scale: 0.985 },
  animate: { opacity: 1, y: 0, scale: 1 },
  transition: { duration: 0.22, ease: [0.16, 1, 0.3, 1] },
})

export const compactReveal = Object.freeze({
  initial: { opacity: 0, y: 5 },
  animate: { opacity: 1, y: 0 },
  transition: { duration: 0.18, ease: [0.16, 1, 0.3, 1] },
})
