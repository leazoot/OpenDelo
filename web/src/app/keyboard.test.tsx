import { fireEvent, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { copyFor } from '../i18n/copy'
import { useLanguageStore } from '../state/language'
import { renderConsole, respondTo, settleRequests } from '../test/console'

/*
 * 不用鼠标走完一次审批（REQ-UI-009 AC1、REQ-APPROVAL-003）。
 *
 * 「查看待审批 → 打开 Folio → 允许 → 查看 Lease」这条路要么整条走得通，
 * 要么就是走不通 —— 逐个组件测快捷键看不出中间断在哪一环，因为断的往往是
 * 交界处：卡片选中了，但焦点落在一个 `↵` 被全局处理器截走的按钮上。
 *
 * **jsdom 不实现 Tab 的焦点移动，也不实现按钮的默认激活。** 这里补上后者
 * （见 `pressEnter`），前者只断言「这个控件确实在 Tab 序列里」；真正按 Tab
 * 走一遍留给 S7 的 Playwright。
 */

const zh = copyFor('zh')

/** 能被 Tab 走到的控件。`tabindex="-1"` 只能用脚本聚焦，不算在内。 */
const TAB_STOP = 'a[href], button:not(:disabled), input, select, textarea, [tabindex]:not([tabindex="-1"])'

function isTabStop(element: HTMLElement): boolean {
  return element.matches(TAB_STOP) && element.closest('[aria-hidden="true"]') === null
}

/**
 * 按下回车。
 *
 * 浏览器里回车落在按钮或链接上就是一次点击，除非有人 `preventDefault` 了它。
 * jsdom 不做这一步，于是照同样的规则补上 —— 少了它，「全局快捷键把回车抢走」
 * 这类缺陷在测试里看不出来，因为测试本来就是直接 `click` 的。
 */
function pressEnter(element: HTMLElement): void {
  const delivered = fireEvent.keyDown(element, { key: 'Enter' })
  if (delivered && element.matches('a[href], button')) {
    fireEvent.click(element)
  }
}

const calls: { path: string; method: string }[] = []

beforeEach(() => {
  document.head.innerHTML = '<meta name="opendelo-session-token" content="test-session-token">'
  useLanguageStore.setState({ language: 'zh' })
  calls.length = 0
  vi.stubGlobal('fetch', (path: string, init?: RequestInit) => {
    calls.push({ path, method: init?.method ?? 'GET' })
    return respondTo(path)
  })
})

afterEach(() => {
  vi.unstubAllGlobals()
  document.head.innerHTML = ''
})

describe('不用鼠标走完一次审批', () => {
  it('查看待审批 → 打开 Folio → 允许 → 查看 Lease', async () => {
    renderConsole('/gate')
    await settleRequests()

    // ① 缝前那一条在 Tab 序列里，回车把它选中。
    const card = screen.getByRole('button', { name: /writer-agent/ })
    expect(isTabStop(card)).toBe(true)
    card.focus()
    pressEnter(card)
    expect(card).toHaveAttribute('aria-pressed', 'true')

    // ② Inspector 里的入口同样在 Tab 序列里，回车展开卷宗。
    const open = screen.getByRole('link', { name: new RegExp(zh.inspectorOpenFolio) })
    expect(isTabStop(open)).toBe(true)
    open.focus()
    pressEnter(open)
    await settleRequests()
    expect(await screen.findByText(zh.folioArrival)).toBeInTheDocument()

    // ③ 在卷宗上按 A 放行。判断走的是与按钮同一条 `mayDecide`。
    fireEvent.keyDown(window, { key: 'a' })
    await settleRequests()
    expect(calls).toContainEqual({ path: '/v1/approvals/ap-1/allow-task', method: 'POST' })

    // ④ Esc 折回缝前，生效中的授权就在架子上，且可以用键盘收回。
    fireEvent.keyDown(window, { key: 'Escape' })
    await settleRequests()
    const lease = screen.getByLabelText(zh.leaseTabAria('cloudflare', '0s'))
    expect(isTabStop(lease)).toBe(true)
  })

  it('回车落在按钮上时归按钮自己，不被全局快捷键截走', () => {
    // 全局处理器一律 `preventDefault`，卡片上的回车因此什么也不会发生：
    // 选中还没有产生，`↵ 打开 Folio` 又找不到被选中的那一条。
    renderConsole('/gate')

    const card = screen.getByRole('button', { name: zh.moreAria })
    const delivered = fireEvent.keyDown(card, { key: 'Enter' })

    expect(delivered).toBe(true)
  })

  it('回车落在页面本身时仍然打开卷宗', async () => {
    renderConsole('/gate')
    await settleRequests()

    fireEvent.click(screen.getByRole('button', { name: /writer-agent/ }))
    fireEvent.keyDown(window, { key: 'Enter' })
    await settleRequests()

    expect(await screen.findByText(zh.folioArrival)).toBeInTheDocument()
  })

  it('折回缝前时焦点回到打开卷宗的那一条', async () => {
    // 卷宗是一条路由，折回时上一页整棵重建 —— 不接回去的话焦点掉在 <body>，
    // 键盘用户要从顶栏一路 Tab 回来。
    renderConsole('/gate')
    await settleRequests()

    const card = screen.getByRole('button', { name: /writer-agent/ })
    fireEvent.click(card)
    fireEvent.keyDown(window, { key: 'Enter' })
    await settleRequests()
    expect(await screen.findByText(zh.folioArrival)).toBeInTheDocument()

    fireEvent.keyDown(window, { key: 'Escape' })
    await settleRequests()

    expect(screen.getByRole('button', { name: /writer-agent/ })).toHaveFocus()
  })

  it('直接进入 Gate 时不抢焦点', async () => {
    // 焦点跟着「刚从卷宗回来」这一次导航走，不是 Gate 每次挂载都做的事。
    renderConsole('/gate')
    await settleRequests()

    expect(document.body).toHaveFocus()
  })

  it('缝前的动静播报给读屏', async () => {
    // 列表里多出一行、或最上面那条的结论变了，读屏用户都看不见。
    renderConsole('/gate')
    await settleRequests()

    const live = screen.getByText(
      zh.gateAnnounce('writer-agent · update_dns_record · cloudflare', zh.verdictWaiting),
    )
    expect(live).toHaveAttribute('aria-live', 'polite')
  })

  it('焦点在输入框里时字母键不再是决定', async () => {
    renderConsole('/gate/folio/rq-1')
    await settleRequests()

    const field = document.createElement('input')
    document.body.append(field)
    fireEvent.keyDown(field, { key: 'a' })
    await settleRequests()

    expect(calls.filter((call) => call.method === 'POST')).toEqual([])
    field.remove()
  })
})
