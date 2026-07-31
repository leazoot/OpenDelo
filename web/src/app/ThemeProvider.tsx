import { useEffect, type ReactNode } from 'react'

import { useResolvedTheme } from '../state/theme'

/**
 * 把主题偏好写到根元素的 `data-theme` 上，令牌表据此切换。
 * 组件树本身不感知主题，只消费 CSS 变量。
 */
export function ThemeProvider({ children }: { children: ReactNode }) {
  const theme = useResolvedTheme()

  useEffect(() => {
    document.documentElement.dataset.theme = theme
  }, [theme])

  return children
}
