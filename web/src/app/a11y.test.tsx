import { fireEvent, screen } from '@testing-library/react'
import axe, { type Result } from 'axe-core'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { copyFor } from '../i18n/copy'
import { useLanguageStore } from '../state/language'
import { auditEvents, renderConsole, respondTo, settleRequests } from '../test/console'

/*
 * 自动化可访问性扫描（REQ-UI-009 AC3）。
 *
 * 扫的是七条路由而不是验收标准写的五个主页面：Access Folio 与 Rule Manuscript
 * 结构最复杂（一个是双页卷宗，一个是可编辑的行内槽位），把它们排除在外，
 * 扫描就恰好绕开了最容易出问题的两页。
 *
 * 门槛也比验收标准严：AC3 只要求没有 serious/critical，这里连 minor 都不放过。
 * 今天七条路由在全部等级上都是干净的，那就以「干净」为基线 —— 门槛定在
 * serious 上，等于给 moderate 留了一个不会有人回头看的口子。
 *
 * **jsdom 不做布局，颜色对比与目标尺寸这两类规则在这里一律得不出结论**
 * （axe 报 incomplete，不是 violation）。对比度由 `styles/contrast.test.ts` 从
 * 令牌与 CSS 静态核对，像素级的部分留给 S7 的 Playwright —— 那时同一套规则
 * 由 `@axe-core/playwright` 在真实浏览器里再跑一遍。
 */

const zh = copyFor('zh')

/** 七条路由（`app/routes.tsx`），带上具体的 id。 */
const ROUTES: readonly [string, string][] = [
  ['Gate', '/gate'],
  ['Access Folio', '/gate/folio/rq-1'],
  ['Identities', '/identities'],
  ['Automation', '/automation'],
  ['Rule Manuscript', '/automation/advanced/tm-1'],
  ['Ledger', '/ledger'],
  ['Preferences', '/preferences'],
]

/**
 * 一行一个问题，带上出问题的那个元素 —— 只报规则名的话，
 * 修的人还要自己把整棵树翻一遍。
 */
function describeAll(violations: readonly Result[]): string[] {
  return violations.flatMap((violation) =>
    violation.nodes.map((node) => `${violation.impact ?? 'unknown'} · ${violation.id} · ${node.html}`),
  )
}

async function scan(container: HTMLElement): Promise<string[]> {
  const outcome = await axe.run(container, {
    resultTypes: ['violations'],
    rules: {
      // jsdom 没有布局，这两类规则得不出结论；见文件头。
      'color-contrast': { enabled: false },
      'target-size': { enabled: false },
    },
  })
  return describeAll(outcome.violations)
}

beforeEach(() => {
  document.head.innerHTML = '<meta name="opendelo-session-token" content="test-session-token">'
  useLanguageStore.setState({ language: 'zh' })
  vi.stubGlobal('fetch', (path: string) => respondTo(path))
})

afterEach(() => {
  vi.unstubAllGlobals()
  document.head.innerHTML = ''
})

describe('七条路由的 axe 扫描', () => {
  it.each(ROUTES)('%s 没有任何等级的问题', async (_name, path) => {
    const { container } = renderConsole(path)
    await settleRequests()

    expect(await scan(container)).toEqual([])
  })

  it('英文界面同样干净', async () => {
    // 可及名称大多来自文案字典，换一种语言等于换一整套名称。
    useLanguageStore.setState({ language: 'en' })
    const { container } = renderConsole('/gate')
    await settleRequests()

    expect(await scan(container)).toEqual([])
  })
})

/*
 * 只在交互之后才存在的结构。
 *
 * 逐路由扫描看不见它们：菜单、抽屉、二次确认都是点开之后才出现在树里的，
 * 而 `role="menu"` 与 `role="alertdialog"` 恰恰是 ARIA 用错的高发处。
 */
describe('展开之后才出现的结构', () => {
  it('溢出菜单展开时', async () => {
    const { container } = renderConsole('/gate')
    await settleRequests()

    fireEvent.click(screen.getByLabelText(zh.moreAria))

    expect(await scan(container)).toEqual([])
  })

  it('选中一条请求、Inspector 装满时', async () => {
    const { container } = renderConsole('/gate')
    await settleRequests()

    fireEvent.click(screen.getByRole('button', { name: /writer-agent/ }))
    await settleRequests()

    expect(await scan(container)).toEqual([])
  })

  it('命令面板唤出时', async () => {
    const { container } = renderConsole('/gate')
    await settleRequests()

    fireEvent.keyDown(window, { key: 'k', metaKey: true })

    expect(await scan(container)).toEqual([])
  })

  it('撤销 Lease 的二次确认展开时', async () => {
    const { container } = renderConsole('/gate')
    await settleRequests()

    fireEvent.click(screen.getByLabelText(zh.leaseTabAria('cloudflare', '0s')))

    expect(await screen.findByRole('alertdialog')).toBeInTheDocument()
    expect(await scan(container)).toEqual([])
  })
})

/*
 * 颜色不是唯一的信息载体（REQ-UI-009 AC4）。
 *
 * Passage 与 Inspector 各自的用例已经核对过风险等级与结论的文字。剩下的是
 * 账本条：它把结论画成一个绿点或红点，而那个点是 `aria-hidden` 的装饰。
 * 分不出通行与拒绝的只能是同一行里的文字。
 */
describe('账本条不靠颜色说话', () => {
  /** 去掉装饰之后这一行还剩下什么。 */
  function meaningOf(row: HTMLElement): string {
    const copy = row.cloneNode(true)
    if (!(copy instanceof HTMLElement)) {
      return ''
    }
    for (const decoration of copy.querySelectorAll('[aria-hidden="true"]')) {
      decoration.remove()
    }
    return copy.textContent ?? ''
  }

  it.each([
    ['通行', 'allow', 'decision.auto_allowed'],
    ['拒绝', 'deny', 'decision.denied'],
  ])('%s 的那一行去掉圆点之后仍然认得出来', async (_name, verdict, type) => {
    vi.stubGlobal('fetch', (path: string) =>
      path.startsWith('/v1/audit-events')
        ? Promise.resolve(
            new Response(JSON.stringify({ items: [{ ...auditEvents.items[0], verdict, type }], next_cursor: '' }), {
              status: 200,
              headers: { 'Content-Type': 'application/json; charset=utf-8' },
            }),
          )
        : respondTo(path),
    )

    renderConsole('/gate')
    await settleRequests()

    const rows = screen.getAllByRole('listitem').filter((item) => item.textContent?.includes('cloudflare · '))
    const row = rows[0]
    expect(row).toBeDefined()
    expect(meaningOf(row ?? document.createElement('li'))).toContain(type)
  })
})
