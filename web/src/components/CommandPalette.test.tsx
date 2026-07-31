import { fireEvent, screen, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { copyFor } from '../i18n/copy'
import { useLanguageStore } from '../state/language'
import { useThemeStore } from '../state/theme'
import { renderConsole, respondTo, settleRequests } from '../test/console'

/*
 * ⌘K 命令面板（REQ-UI-011、假设 A-10）。
 *
 * 从整个外壳挂起来测，不单挂组件：面板的价值就在于它是**别处已有能力**的
 * 另一个入口 —— 把路由和数据换成桩，AC1「每一项都对应一个已存在的 API 或路由」
 * 就没什么可验的了。
 */

const zh = copyFor('zh')
const calls: { path: string; method: string }[] = []

function openPalette() {
  fireEvent.keyDown(window, { key: 'k', metaKey: true })
}

function panel(): HTMLElement {
  return screen.getByRole('dialog', { name: zh.commandLabel })
}

/** 让某一条媒体查询命中。jsdom 不做布局，断点只能从 matchMedia 这一头给。 */
function matchWidth(matching: string) {
  vi.stubGlobal('matchMedia', (query: string) => ({
    matches: query === matching,
    media: query,
    onchange: null,
    addListener: () => undefined,
    removeListener: () => undefined,
    addEventListener: () => undefined,
    removeEventListener: () => undefined,
    dispatchEvent: () => false,
  }))
}

beforeEach(() => {
  document.head.innerHTML = '<meta name="opendelo-session-token" content="test-session-token">'
  useLanguageStore.setState({ language: 'zh' })
  useThemeStore.setState({ preference: 'system' })
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

describe('唤出与关闭', () => {
  it('⌘K 唤出，焦点落在输入框上', async () => {
    renderConsole('/gate')
    await settleRequests()

    openPalette()

    expect(panel()).toBeInTheDocument()
    expect(screen.getByRole('combobox', { name: zh.commandLabel })).toHaveFocus()
  })

  it('Ctrl+K 一样唤出 —— 这台机器不一定是 Mac', async () => {
    renderConsole('/gate')
    await settleRequests()

    fireEvent.keyDown(window, { key: 'k', ctrlKey: true })

    expect(panel()).toBeInTheDocument()
  })

  it('Esc 关闭并把焦点还给叫它的那个按钮（AC2）', async () => {
    renderConsole('/gate')
    await settleRequests()

    const trigger = screen.getByRole('button', { name: zh.commandAria })
    trigger.focus()
    fireEvent.click(trigger)
    expect(panel()).toBeInTheDocument()

    fireEvent.keyDown(screen.getByRole('combobox', { name: zh.commandLabel }), { key: 'Escape' })

    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(trigger).toHaveFocus()
  })

  it('面板开着时 Esc 不会顺手清掉缝前的选中', async () => {
    // 缝前的 Esc 处理器挂在 window 上。焦点在输入框里时它自己会让开，
    // 但面板里还有别的可聚焦控件 —— 从那里按 Esc，事件照样会冒到 window。
    renderConsole('/gate')
    await settleRequests()

    fireEvent.click(screen.getByRole('button', { name: /writer-agent/ }))
    openPalette()
    fireEvent.keyDown(screen.getByRole('button', { name: zh.commandClose }), { key: 'Escape' })

    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: /writer-agent/ })).toHaveAttribute('aria-pressed', 'true')
  })

  it('点空白处关闭，而那块空白本身是个能聚焦的按钮', () => {
    // 一个只挂 onClick 的 div 在键盘上根本不存在，读屏也念不出它是什么。
    renderConsole('/gate')
    openPalette()

    const scrim = screen.getByRole('button', { name: zh.commandClose })
    fireEvent.click(scrim)

    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })
})

describe('每一项都对应已有的路由或端点（AC1）', () => {
  it('待审批那一条通向它自己的卷宗', async () => {
    renderConsole('/gate')
    await settleRequests()
    openPalette()

    fireEvent.click(within(panel()).getByText(zh.commandOpenFolio('update_dns_record · example.com · www')))
    await settleRequests()

    expect(await screen.findByText(zh.folioArrival)).toBeInTheDocument()
  })

  it('页面那一组走的是路由表上的路径', async () => {
    renderConsole('/gate')
    await settleRequests()
    openPalette()

    fireEvent.click(within(panel()).getByText(zh.pageLedger))
    await settleRequests()

    expect(screen.getByText(zh.ledgerLocalOnly)).toBeInTheDocument()
  })

  it('收回授权先问一次，确认后才发 DELETE', async () => {
    renderConsole('/gate')
    await settleRequests()
    openPalette()

    fireEvent.click(within(panel()).getByText(zh.commandRevokeLease('cloudflare')))

    // 问过之前一个请求都不发 —— 这与缝内侧那一架用的是同一条规矩。
    expect(calls.filter((call) => call.method === 'DELETE')).toEqual([])
    expect(screen.getByRole('alert')).toHaveTextContent(zh.commandRevokeConfirm('cloudflare'))

    fireEvent.keyDown(screen.getByRole('combobox', { name: zh.commandLabel }), { key: 'Enter' })
    await settleRequests()

    expect(calls).toContainEqual({ path: '/v1/leases/ls-1', method: 'DELETE' })
  })

  it('确认态按 Esc 退回列表，不是关掉面板', async () => {
    renderConsole('/gate')
    await settleRequests()
    openPalette()

    fireEvent.click(within(panel()).getByText(zh.commandRevokeLease('cloudflare')))
    fireEvent.keyDown(screen.getByRole('combobox', { name: zh.commandLabel }), { key: 'Escape' })

    expect(panel()).toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    expect(calls.filter((call) => call.method === 'DELETE')).toEqual([])
  })

  it('窄屏上不列出被拦下的三页 —— 面板不是绕过拦截的后门', async () => {
    // 小于 1024 时策略编辑与身份管理是路由级拦截（REQ-UI-004 AC3）；
    // 面板照样列出来的话，点进去只会撞上拦截页。
    matchWidth('(width < 1024px)')
    renderConsole('/gate')
    await settleRequests()
    openPalette()

    const listed = within(panel()).getAllByRole('option').map((option) => option.textContent ?? '')

    expect(listed.some((text) => text.includes(zh.pageGate))).toBe(true)
    for (const page of [zh.pageIdentities, zh.pageAutomation, zh.pagePreferences]) {
      expect(listed.some((text) => text.includes(page)), page).toBe(false)
    }
  })

  it('1280 以下顶栏没有 ⌘K 按钮，入口收进溢出菜单', async () => {
    // 触屏上按不出这个快捷键，菜单是那些宽度上唯一的入口（REQ-UI-004 ④）。
    matchWidth('(width < 1280px)')
    renderConsole('/gate')
    await settleRequests()

    expect(screen.queryByRole('button', { name: zh.commandAria })).not.toBeInTheDocument()
    fireEvent.click(screen.getByLabelText(zh.moreAria))
    fireEvent.click(screen.getByRole('menuitem', { name: new RegExp(zh.commandLabel) }))

    expect(panel()).toBeInTheDocument()
  })

  it('没有「切换 Gateway」—— 本期只连本机一条缝', async () => {
    renderConsole('/gate')
    await settleRequests()
    openPalette()

    expect(within(panel()).queryByText(/Gateway/)).not.toBeInTheDocument()
  })
})

describe('键盘完全可用', () => {
  it('↑↓ 移动，↵ 执行选中的那一条', async () => {
    renderConsole('/gate')
    await settleRequests()
    openPalette()

    const query = screen.getByRole('combobox', { name: zh.commandLabel })
    const options = within(panel()).getAllByRole('option')
    expect(options[0]).toHaveAttribute('aria-selected', 'true')

    fireEvent.keyDown(query, { key: 'ArrowDown' })
    expect(within(panel()).getAllByRole('option')[1]).toHaveAttribute('aria-selected', 'true')

    fireEvent.keyDown(query, { key: 'ArrowUp' })
    fireEvent.keyDown(query, { key: 'ArrowUp' })
    // 到顶再往上回到末尾：一路 ↑ 也走得到最后一项。
    const wrapped = within(panel()).getAllByRole('option')
    expect(wrapped[wrapped.length - 1]).toHaveAttribute('aria-selected', 'true')
  })

  it('输入即筛选，一条都不匹配时说明面板里只有已有的事', async () => {
    renderConsole('/gate')
    await settleRequests()
    openPalette()

    const query = screen.getByRole('combobox', { name: zh.commandLabel })
    fireEvent.change(query, { target: { value: 'ledger' } })
    expect(within(panel()).getAllByRole('option')).toHaveLength(1)

    fireEvent.change(query, { target: { value: '删库' } })
    expect(screen.getByText(zh.commandEmpty)).toBeInTheDocument()
  })

  it('切换主题这一条真的换了主题', async () => {
    renderConsole('/gate')
    await settleRequests()
    openPalette()

    fireEvent.click(within(panel()).getByText(zh.commandSwitchTheme))

    expect(useThemeStore.getState().preference).toBe('dark')
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })
})
