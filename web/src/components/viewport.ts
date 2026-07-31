import { useSyncExternalStore } from 'react'

/*
 * 视口断点（REQ-UI-004）。
 *
 * 这里只放**改变结构**的两个断点。纯视觉的收窄（Inspector 300→232、Passage 326→272、
 * 账本四列折成一行）留在 CSS 里 —— 那些不需要 React 知道，写成 JS 只会让同一条
 * 断点有两个来源。
 */

const COMPACT_QUERY = '(width < 1280px)'
const NARROW_QUERY = '(width < 1024px)'

function matches(query: string): boolean {
  return globalThis.matchMedia(query).matches
}

function subscribeTo(query: string): (onChange: () => void) => () => void {
  return (onChange) => {
    const list = globalThis.matchMedia(query)
    list.addEventListener('change', onChange)
    return () => {
      list.removeEventListener('change', onChange)
    }
  }
}

/** 用 matchMedia 而不是 innerWidth：后者要自己监听 resize。服务端渲染时按最宽算。 */
function useMediaQuery(query: string): boolean {
  return useSyncExternalStore(
    subscribeTo(query),
    () => matches(query),
    () => false,
  )
}

/** 1024–1279：顶栏的次级控件收进溢出菜单，Inspector 变抽屉。 */
export function useIsCompact(): boolean {
  return useMediaQuery(COMPACT_QUERY)
}

/** 小于 1024：只提供审批、Lease、History、紧急撤销（PRD §24）。 */
export function useIsNarrow(): boolean {
  return useMediaQuery(NARROW_QUERY)
}
