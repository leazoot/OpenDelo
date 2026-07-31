import { useSyncExternalStore } from 'react'
import { create } from 'zustand'

/** 用户可选的三种取值；`system` 表示跟随操作系统。 */
export type ThemePreference = 'dark' | 'light' | 'system'

/** 实际落到 DOM 上的两种取值。 */
export type ResolvedTheme = 'dark' | 'light'

const STORAGE_KEY = 'opendelo.theme'

interface ThemeState {
  preference: ThemePreference
  setPreference: (preference: ThemePreference) => void
}

function readStoredPreference(): ThemePreference {
  const stored = localStorage.getItem(STORAGE_KEY)
  return stored === 'dark' || stored === 'light' || stored === 'system' ? stored : 'system'
}

export const useThemeStore = create<ThemeState>((set) => ({
  preference: readStoredPreference(),
  setPreference: (preference) => {
    localStorage.setItem(STORAGE_KEY, preference)
    set({ preference })
  },
}))

/** 把偏好解析为实际主题；`system` 之外的取值直接返回，不查询媒体查询。 */
/**
 * 点一下之后轮到哪一档。
 *
 * 顶栏的图标按钮与命令面板走同一条环：两份表迟早会转向不同的方向。
 */
export const NEXT_PREFERENCE: Record<ThemePreference, ThemePreference> = {
  system: 'dark',
  dark: 'light',
  light: 'system',
}

export function resolveTheme(preference: ThemePreference, prefersDark: boolean): ResolvedTheme {
  if (preference === 'system') {
    return prefersDark ? 'dark' : 'light'
  }
  return preference
}

const DARK_QUERY = '(prefers-color-scheme: dark)'

function subscribeToSystemTheme(onChange: () => void): () => void {
  const query = window.matchMedia(DARK_QUERY)
  query.addEventListener('change', onChange)
  return () => {
    query.removeEventListener('change', onChange)
  }
}

/**
 * 当前实际生效的主题。
 *
 * 不止 ThemeProvider 要用：从令牌里取色的地方（favicon）必须在主题变化时重新取，
 * 否则会留着上一个主题的那一档颜色。让两处各自监听媒体查询会让「现在是哪个主题」
 * 有两个答案，所以解析只在这里做一次。
 */
export function useResolvedTheme(): ResolvedTheme {
  const preference = useThemeStore((state) => state.preference)
  const prefersDark = useSyncExternalStore(
    subscribeToSystemTheme,
    () => window.matchMedia(DARK_QUERY).matches,
    () => false,
  )
  return resolveTheme(preference, prefersDark)
}
