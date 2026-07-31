import { readdirSync, readFileSync } from 'node:fs'
import { join, resolve } from 'node:path'

import { QueryClientProvider } from '@tanstack/react-query'
import { fireEvent, render, screen } from '@testing-library/react'
import { createMemoryRouter, RouterProvider } from 'react-router'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { vi } from 'vitest'

import { createQueryClient } from '../data/queryClient'
import { copyFor } from '../i18n/copy'
import { useLanguageStore } from '../state/language'

import { routes } from './routes'

/*
 * 四个断点的回归基线（REQ-UI-004 AC1）。
 *
 * jsdom 不做布局，因此这里不比像素，而是把每个 CSS Module 的媒体查询按宽度**解一遍**，
 * 断言解出来的声明就是设计稿给的那一组。改掉一个断点里的数值、把某条规则挪到别的
 * 断点、或者顺手给缝加一条断点分支，都会让对应宽度的用例失败。
 * 真实浏览器里的像素级对照留给 S7 的 Playwright。
 *
 * 结构性的降级（导航形态、溢出菜单、路由拦截）用真实渲染来测：那几条不是样式，
 * 是这个宽度上还剩下哪些东西。
 */

const CSS_ROOT = resolve(process.cwd(), 'src')

/** 注释先去掉：它可能出现在选择器前面，留着会让整段规则认不出来。 */
const stripComments = (css: string) => css.replace(/\/\*[\s\S]*?\*\//g, '')

const readCss = (relativePath: string) => stripComments(readFileSync(resolve(CSS_ROOT, relativePath), 'utf8'))

interface Segment {
  /** 这一段生效的最大宽度；无条件的部分为正无穷。 */
  readonly limit: number
  readonly body: string
}

/** 把一份 CSS 拆成「无条件的部分」与若干个 max-width 段，保持源码顺序。 */
function segmentsOf(css: string): Segment[] {
  const media: Segment[] = []
  let base = ''
  let index = 0

  while (index < css.length) {
    const start = css.indexOf('@media', index)
    if (start === -1) {
      base += css.slice(index)
      break
    }
    base += css.slice(index, start)

    const open = css.indexOf('{', start)
    // 与宽度无关的查询（prefers-reduced-motion 一类）取负无穷：任何宽度都轮不到它。
    const limit = /max-width:\s*(\d+)px/.exec(css.slice(start, open))?.[1] ?? '-Infinity'

    let depth = 1
    let cursor = open + 1
    while (depth > 0 && cursor < css.length) {
      const char = css.charAt(cursor)
      depth += char === '{' ? 1 : char === '}' ? -1 : 0
      cursor += 1
    }
    media.push({ limit: Number(limit), body: css.slice(open + 1, cursor - 1) })
    index = cursor
  }

  return [{ limit: Number.POSITIVE_INFINITY, body: base }, ...media]
}

interface Block {
  readonly selectors: readonly string[]
  readonly declarations: string
}

function blocksOf(body: string): Block[] {
  const blocks: Block[] = []
  const pattern = /([^{}]+)\{([^}]*)\}/g
  let match = pattern.exec(body)
  while (match !== null) {
    blocks.push({
      selectors: (match[1] ?? '').split(',').map((one) => one.trim()),
      declarations: match[2] ?? '',
    })
    match = pattern.exec(body)
  }
  return blocks
}

/** 某个选择器在给定宽度下解出来的声明。后面的段覆盖前面的，与浏览器一致。 */
function resolved(css: string, selector: string, width: number): Record<string, string> {
  const out: Record<string, string> = {}
  for (const segment of segmentsOf(css)) {
    if (width > segment.limit) {
      continue
    }
    for (const block of blocksOf(segment.body)) {
      if (!block.selectors.includes(selector)) {
        continue
      }
      for (const declaration of block.declarations.split(';')) {
        const colon = declaration.indexOf(':')
        if (colon !== -1) {
          out[declaration.slice(0, colon).trim()] = declaration.slice(colon + 1).trim()
        }
      }
    }
  }
  return out
}

const barCss = readCss('app/BoundaryBar.module.css')
const inspectorCss = readCss('boundary/Inspector.module.css')
const passageCss = readCss('boundary/Passage.module.css')
const ribbonCss = readCss('boundary/LedgerRibbon.module.css')
const rackCss = readCss('boundary/LeaseRack.module.css')
const identitiesCss = readCss('pages/identities/IdentitiesPage.module.css')
const automationCss = readCss('pages/automation/AutomationPage.module.css')

describe('1440 · 完整 Gatehouse', () => {
  const width = 1440

  it('Inspector 常驻 300，统计块在场', () => {
    expect(resolved(inspectorCss, '.inspector', width)['flex']).toBe('0 0 300px')
    expect(resolved(inspectorCss, '.stats', width)['display']).toBeUndefined()
  })

  it('Passage 卡片 326，账本是四列网格', () => {
    expect(resolved(passageCss, '.card', width)['width']).toBe('326px')
    expect(resolved(ribbonCss, '.entries', width)['gap']).toBe('0')
    expect(resolved(ribbonCss, '.entry', width)['border-left']).toBe('1px solid var(--line)')
  })

  it('导航写的是名字，图标不出现', () => {
    expect(resolved(barCss, '.navIcon', width)['display']).toBe('none')
    expect(resolved(barCss, '.navLabel', width)['position']).toBeUndefined()
  })
})

describe('1280 · 三项收窄（REQ-UI-004 AC2）', () => {
  const width = 1280

  it('Inspector 收到 232 且统计块让出去', () => {
    expect(resolved(inspectorCss, '.inspector', width)['flex']).toBe('0 0 232px')
    expect(resolved(inspectorCss, '.stats', width)['display']).toBe('none')
  })

  it('Passage 卡片收到 272', () => {
    expect(resolved(passageCss, '.card', width)['width']).toBe('272px')
  })

  it('账本四列网格折成单行横向条', () => {
    expect(resolved(ribbonCss, '.entries', width)['gap']).toBe('18px')
    expect(resolved(ribbonCss, '.entry', width)['border-left']).toBe('0')
    expect(resolved(ribbonCss, '.entry:not(:first-child)', width)['display']).toBeUndefined()
  })

  it('Inspector 仍是一列，还没有变成抽屉', () => {
    expect(resolved(inspectorCss, '.inspector', width)['position']).toBeUndefined()
  })
})

describe('1024 · 四条降级规则（REQ-UI-004 ①②③④）', () => {
  const width = 1024

  it('① 导航文字换成图标，名字留给读屏，待审批收成角点', () => {
    expect(resolved(barCss, '.navIcon', width)['display']).toBe('block')
    expect(resolved(barCss, '.navItem', width)['width']).toBe('30px')

    const pending = resolved(barCss, '.pending', width)
    expect(pending['position']).toBe('absolute')
    expect(pending['width']).toBe('5px')
    expect(pending['border']).toBe('0')
  })

  it.each(['.navLabel', '.pendingCount'])('%s 是裁掉的，不是 display:none', (selector) => {
    // 这两处文字在 1024 上都不再占位置，但它们仍然是可及名称的来源：
    // 导航项的名字与「几个待审批」。改成 display:none 会把这两句一起从
    // 无障碍树里拿掉，图标导航就成了一排没有名字的方块。
    const hidden = resolved(barCss, selector, width)

    expect(hidden['clip-path']).toBe('inset(50%)')
    expect(hidden['width']).toBe('1px')
    expect(hidden['display']).not.toBe('none')
  })

  it('② Inspector 变抽屉，且是移进移出而不是改宽度', () => {
    const inspector = resolved(inspectorCss, '.inspector', width)
    expect(inspector['position']).toBe('absolute')
    expect(inspector['transform']).toBe('translateX(100%)')
    expect(resolved(inspectorCss, '.drawerOpen', width)['transform']).toBe('translateX(0)')
  })

  it('③ 账本只留最近一条', () => {
    expect(resolved(ribbonCss, '.entry:not(:first-child)', width)['display']).toBe('none')
  })

  it('④ 顶栏收起 host，次级控件交给溢出菜单', () => {
    expect(resolved(barCss, '.gatewayHost', width)['display']).toBe('none')
    expect(resolved(barCss, '.menu', width)['position']).toBe('absolute')
  })

  it('Lease 架改为横向滚动，Automation 单栏，Identities 卡片折成两层（PRD §24）', () => {
    const rack = resolved(rackCss, '.list', width)
    expect(rack['flex-direction']).toBe('row')
    expect(rack['overflow-x']).toBe('auto')

    expect(resolved(automationCss, '.grid', width)['grid-template-columns']).toBe('minmax(0, 1fr)')
    expect(resolved(identitiesCss, '.tail', width)['flex']).toBe('1 0 100%')
  })
})

describe('全站只有 REQ-UI-004 的这两条分界线', () => {
  it('每一条宽度相关的媒体查询都落在 1279 或 1439 上', () => {
    // 断点写成 1024 或 1280 时，1100px 与 1300px 这两段宽度上什么也不会降级 ——
    // REQ-UI-004 的区间是 1024–1279 与 1280–1439，边界值是区间的上沿。
    for (const file of allCss()) {
      const queries = stripComments(readFileSync(file, 'utf8')).match(/@media[^{]*/g) ?? []
      for (const query of queries) {
        if (!/width/.test(query)) {
          continue
        }
        expect(query.trim(), file).toMatch(/^@media \(max-width: (1279|1439)px\)$/)
      }
    }
  })
})

describe('缝在四个断点上都不动（REQ-UI-002）', () => {
  it('没有任何一个断点分支去碰缝的几何', () => {
    // 缝居中靠 left:50% 与 margin-left:calc(宽度 / -2)。任何一条媒体查询里
    // 出现这些属性，都意味着某个宽度上的缝心与别处不在一条线上。
    const geometry = ['--seam-width', '--seam-glow-width', 'left: 50%', 'margin-left: calc(var(--seam']
    for (const file of cssModules()) {
      const css = stripComments(readFileSync(file, 'utf8'))
      for (const segment of segmentsOf(css)) {
        if (segment.limit === Number.POSITIVE_INFINITY) {
          continue
        }
        for (const property of geometry) {
          expect(segment.body, `${file} 的 ${String(segment.limit)}px 分支`).not.toContain(property)
        }
      }
    }
  })
})

describe('顶栏在两种窗口态下同高（REQ-UI-004 AC4）', () => {
  it.each([1440, 1280, 1024, 900])('%i px 下高度是一个定值，且不做粘顶动画', (width) => {
    const bar = resolved(barCss, '.bar', width)

    expect(bar['height']).toMatch(/^\d+px$/)
    // 全屏与窗口态的差别只在有没有浏览器 chrome。粘顶、随滚动收起、
    // 高度过渡都会让缝在两种状态下落在不同的位置。
    expect(bar['position']).toBe('relative')
    expect(bar['transition']).toBeUndefined()
    expect(bar['flex']).toBe('0 0 auto')
  })
})

function cssFiles(suffix: string): string[] {
  const found: string[] = []
  const walk = (directory: string) => {
    for (const entry of readdirSync(directory, { withFileTypes: true })) {
      const path = join(directory, entry.name)
      if (entry.isDirectory()) {
        walk(path)
      } else if (entry.name.endsWith(suffix)) {
        found.push(path)
      }
    }
  }
  walk(CSS_ROOT)
  return found
}

const cssModules = () => cssFiles('.module.css')
const allCss = () => cssFiles('.css')

/* ── 结构性降级：这个宽度上还剩下哪些东西 ── */

const zh = copyFor('zh')

function setViewport(width: number) {
  vi.stubGlobal('matchMedia', (query: string) => ({
    matches: widthMatches(query, width),
    media: query,
    addEventListener: () => undefined,
    removeEventListener: () => undefined,
  }))
}

/** 只解视口宽度的查询；prefers-color-scheme 一类一律按不匹配处理。 */
function widthMatches(query: string, width: number): boolean {
  const limit = /\(width < (\d+)px\)/.exec(query)?.[1]
  return limit !== undefined && width < Number(limit)
}

function renderAt(path: string) {
  const router = createMemoryRouter(routes, { initialEntries: [path] })
  return render(
    <QueryClientProvider client={createQueryClient()}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  )
}

describe('顶栏控件随宽度收拢（REQ-UI-004 ④）', () => {
  beforeEach(() => {
    useLanguageStore.setState({ language: 'zh' })
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it.each([1440, 1280])('%i px 下语言与主题就在顶栏上', (width) => {
    setViewport(width)
    renderAt('/gate')

    expect(screen.getByLabelText(zh.languageAria)).toBeInTheDocument()
    expect(screen.getByLabelText(zh.themeAria(zh.themeSystem))).toBeInTheDocument()
  })

  it('1024 下每个导航项都带 tooltip —— 那时名字已经看不见了', () => {
    setViewport(1024)
    renderAt('/gate')

    for (const label of ['Gate', 'Identities', 'Automation', 'Ledger']) {
      expect(screen.getByRole('link', { name: new RegExp(`^${label}`) })).toHaveAttribute('title', label)
    }
  })

  it('1024 下语言与主题离开顶栏，进到溢出菜单里', () => {
    setViewport(1024)
    renderAt('/gate')

    expect(screen.queryByLabelText(zh.languageAria)).not.toBeInTheDocument()
    expect(screen.queryByLabelText(zh.themeAria(zh.themeSystem))).not.toBeInTheDocument()

    fireEvent.click(screen.getByLabelText(zh.moreAria))

    expect(screen.getByRole('menuitem', { name: new RegExp(zh.moreLanguage) })).toBeInTheDocument()
    expect(screen.getByRole('menuitem', { name: new RegExp(zh.moreTheme) })).toBeInTheDocument()
  })

  it('Preferences 在任何宽度上都只从溢出菜单进入（REQ-UI-001）', () => {
    setViewport(1440)
    renderAt('/gate')

    expect(screen.queryByRole('link', { name: zh.pagePreferences })).not.toBeInTheDocument()

    fireEvent.click(screen.getByLabelText(zh.moreAria))
    fireEvent.click(screen.getByRole('menuitem', { name: zh.morePreferences }))

    expect(screen.getByText(zh.prefsBlurb)).toBeInTheDocument()
  })
})

describe('小于 1024 只剩审批那一侧（PRD §24、REQ-UI-004 AC3）', () => {
  beforeEach(() => {
    useLanguageStore.setState({ language: 'zh' })
    setViewport(900)
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('导航只留 Gate 与 Ledger', () => {
    renderAt('/gate')

    expect(screen.getByRole('link', { name: 'Gate' })).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Ledger' })).toBeInTheDocument()
    expect(screen.queryByRole('link', { name: 'Identities' })).not.toBeInTheDocument()
    expect(screen.queryByRole('link', { name: 'Automation' })).not.toBeInTheDocument()
  })

  it.each([
    ['/identities', zh.narrowTitleIdentities, zh.identitiesOutside],
    ['/automation', zh.narrowTitleAutomation, zh.automationLoading],
    ['/automation/advanced/notes-write', zh.narrowTitleAutomation, zh.manuscriptLoading],
    ['/preferences', zh.narrowTitlePreferences, zh.prefsBlurb],
  ])('直接访问 %s 会被拦下，页面本身不渲染', (path, title, marker) => {
    renderAt(path)

    expect(screen.getByText(title)).toBeInTheDocument()
    expect(screen.queryByText(new RegExp(marker.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')))).not.toBeInTheDocument()
    expect(screen.getByRole('link', { name: zh.narrowBack })).toHaveAttribute('href', '/gate')
  })

  it('Gate 与 Ledger 照常渲染 —— 窄屏不压缩审批', () => {
    renderAt('/gate')
    expect(screen.getByText(zh.outside)).toBeInTheDocument()

    renderAt('/ledger')
    expect(screen.getAllByText(new RegExp(zh.ledgerPageLoading)).length).toBeGreaterThan(0)
  })
})
