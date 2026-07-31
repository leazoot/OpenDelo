import '@testing-library/jest-dom/vitest'

import { cleanup } from '@testing-library/react'
import { afterEach } from 'vitest'

/**
 * jsdom 不实现 matchMedia，而主题解析必须查询 `prefers-color-scheme`。
 *
 * 默认不匹配任何查询（即「系统偏好浅色、不要求减弱动效」）。需要别的系统偏好的用例
 * 自己覆盖 window.matchMedia。
 */
window.matchMedia = (query: string): MediaQueryList => ({
  matches: false,
  media: query,
  onchange: null,
  addListener: () => undefined,
  removeListener: () => undefined,
  addEventListener: () => undefined,
  removeEventListener: () => undefined,
  dispatchEvent: () => false,
})

// 每个用例独立的 DOM，避免跨用例残留。
afterEach(() => {
  cleanup()
})
