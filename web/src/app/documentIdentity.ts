import { useEffect } from 'react'

import type { ConnectionState } from '../data/gatewayStatus'
import type { Copy } from '../i18n/copy'
import { HTML_LANG, type Language } from '../state/language'
import type { ResolvedTheme } from '../state/theme'

/*
 * 标签页的身份：标题与 favicon（REQ-UI-003 AC4）。
 *
 * 这两样是 Console 被切到后台之后仅剩的表达方式。审批在等人，而人正在别的标签里，
 * 这件事只能靠标签自己说。
 */

const PRODUCT = 'OpenDelo'

/** 状态优先级：连不上 > 有人在等 > 一切正常。 */
export type BoundaryState = 'disconnected' | 'pending' | 'connected' | 'connecting'

/**
 * favicon 的取色令牌。
 *
 * 与顶栏状态点用同一组语义色，标签与界面因此说的是同一句话。
 * `connecting` 用中性色而不是 `--wait`：那个色在这里的意思是「有人在等你」，
 * 借给「还在连」会让两件不同的事在标签上长得一样。
 */
const FAVICON_TOKEN: Record<BoundaryState, string> = {
  disconnected: '--block',
  pending: '--wait',
  connected: '--lease',
  connecting: '--text-4',
}

/**
 * 连不上时优先说连不上：那时待审批数是上一次读到的旧值，
 * 用一个旧数字盖住「现在联系不上 Gateway」会让人以为一切正常。
 */
export function boundaryStateOf(connection: ConnectionState, pending: number): BoundaryState {
  if (connection === 'disconnected') {
    return 'disconnected'
  }
  if (pending > 0) {
    return 'pending'
  }
  return connection
}

/** `OpenDelo — Gate · 2 待审批`；没有待审批时不带后半段。 */
export function documentTitleOf(page: string, pending: number, copy: Copy): string {
  const head = `${PRODUCT} — ${page}`
  return pending > 0 ? `${head} · ${copy.pendingInTitle(pending)}` : head
}

/**
 * favicon 是一段缝。
 *
 * 16 像素放不下品牌标记的四条竖线与中点，缩下去只剩一团噪点。缝本身既是这个产品的
 * 形状，也在这个尺寸下仍然认得出来，颜色则用来说明缝现在的状态。
 */
export function faviconOf(color: string): string {
  const svg = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16"><rect x="6.6" y="1" width="2.8" height="14" rx="1.4" fill="${color}"/></svg>`
  return `data:image/svg+xml,${encodeURIComponent(svg)}`
}

/** 从令牌表里取一个颜色；取不到时返回空串。 */
function readToken(name: string): string {
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim()
}

function faviconLink(): HTMLLinkElement {
  const existing = document.head.querySelector('link[rel="icon"]')
  if (existing instanceof HTMLLinkElement) {
    return existing
  }
  const created = document.createElement('link')
  created.rel = 'icon'
  document.head.append(created)
  return created
}

export interface DocumentIdentity {
  readonly page: string
  readonly pending: number
  readonly state: BoundaryState
  readonly language: Language
  readonly copy: Copy
  /** 当前主题。取色随主题变化，所以它必须是依赖之一。 */
  readonly theme: ResolvedTheme
  /** 覆盖取色，只在测试里用（jsdom 不加载令牌表）。 */
  readonly resolveColor?: (token: string) => string
}

export function useDocumentIdentity(identity: DocumentIdentity): void {
  const { page, pending, state, language, copy, theme } = identity
  const resolveColor = identity.resolveColor ?? readToken

  useEffect(() => {
    document.title = documentTitleOf(page, pending, copy)
    document.documentElement.lang = HTML_LANG[language]
  }, [page, pending, copy, language])

  useEffect(() => {
    const color = resolveColor(FAVICON_TOKEN[state])
    if (color === '') {
      // 令牌取不到就不动 favicon：画一个没有颜色的图标只会让标签变成一块空白，
      // 那比留着上一个状态的颜色更难看懂。
      return
    }
    faviconLink().href = faviconOf(color)
    // theme 进依赖但不出现在函数体里：同一个令牌在两个主题下是两个颜色，
    // 取色发生在这里，主题变了就得重取一次。
  }, [state, theme, resolveColor])
}
