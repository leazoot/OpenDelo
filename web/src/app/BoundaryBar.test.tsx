import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import { fireEvent, render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { beforeEach, describe, expect, it } from 'vitest'

import { copyFor } from '../i18n/copy'
import { useLanguageStore } from '../state/language'

import { BoundaryBar } from './BoundaryBar'

const zh = copyFor('zh')
const en = copyFor('en')

const barCss = readFileSync(resolve(process.cwd(), 'src/app/BoundaryBar.module.css'), 'utf8')

/** 顶栏叫过几次命令面板。面板本身挂在外壳上，这里只看它有没有被叫。 */
const openings: number[] = []
const opened = () => openings.push(1)

function renderBar(pending: number, path = '/gate') {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <BoundaryBar connection="connected" status={null} pending={pending} onOpenCommands={opened} />
    </MemoryRouter>,
  )
}

describe('Boundary Bar', () => {
  beforeEach(() => {
    useLanguageStore.setState({ language: 'zh' })
    localStorage.clear()
  })

  it('导航是顶栏里的水平一行，不存在侧栏布局', () => {
    // REQ-UI-001 AC1。侧栏会把缝挤到一边。
    renderBar(0)

    const nav = screen.getByRole('navigation', { name: zh.navLabel })
    expect(nav.closest('header')).not.toBeNull()

    const bar = /\.bar\s*\{([^}]*)\}/.exec(barCss)?.[1] ?? ''
    expect(bar).toContain('display: flex')
    // 竖排就是侧栏。这一条与「缝居中」是同一件事的两面。
    expect(bar).not.toContain('flex-direction: column')
  })

  it('四个导航项都是链接，指向自己的路由', () => {
    renderBar(0)

    const items: [string, string][] = [
      ['Gate', '/gate'],
      ['Identities', '/identities'],
      ['Automation', '/automation'],
      ['Ledger', '/ledger'],
    ]
    for (const [label, href] of items) {
      expect(screen.getByRole('link', { name: new RegExp(`^${label}`) })).toHaveAttribute('href', href)
    }
  })

  it('当前页面的导航项标为 active', () => {
    renderBar(0, '/ledger')

    const ledger = screen.getByRole('link', { name: 'Ledger' })
    expect(ledger.className).toContain('navItemActive')
    expect(screen.getByRole('link', { name: /^Gate/ }).className).not.toContain('navItemActive')
  })

  it('Gateway 选择器在导航与控件之间的弹性槽里居中', () => {
    // REQ-UI-001 AC3。jsdom 不做布局，因此断言的是使它居中的那两条声明。
    const slot = /\.gatewaySlot\s*\{([^}]*)\}/.exec(barCss)?.[1] ?? ''
    expect(slot).toContain('flex: 1')
    expect(slot).toContain('justify-content: center')
  })

  it('有待审批时导航上出现角标，数字本身就是文字', () => {
    renderBar(2)

    const badge = screen.getByLabelText(zh.pendingBadgeAria(2))
    expect(badge).toHaveTextContent('2')
  })

  it('没有待审批时不显示角标，而不是显示一个 0', () => {
    renderBar(0)

    expect(screen.queryByLabelText(/待审批/)).not.toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Gate' })).toHaveTextContent('Gate')
  })

  it('切换语言之后顶栏文案随之变化并落盘', () => {
    renderBar(0)
    expect(screen.getByText(zh.gatewayDevice)).toBeInTheDocument()

    fireEvent.click(screen.getByLabelText(zh.languageAria))

    expect(screen.getByText(en.gatewayDevice)).toBeInTheDocument()
    expect(localStorage.getItem('opendelo.language')).toBe('en')
  })

  it('主题按钮的标签说明当前是哪一档，不只是一个图标', () => {
    renderBar(0)

    expect(screen.getByLabelText(zh.themeAria(zh.themeSystem))).toBeInTheDocument()
  })

  it('⌘K 是有名字的按钮，键盘用户知道它是什么', () => {
    renderBar(0)

    expect(screen.getByRole('button', { name: zh.commandAria })).toBeInTheDocument()
  })

  it('导航图标不进无障碍树，宽屏上也不挂多余的 tooltip', () => {
    // 名字看得见的时候再挂一个同样的 title 是噪音；图标形态下的 tooltip
    // 由 breakpoints 用例守着。
    const { container } = renderBar(0)

    for (const label of ['Gate', 'Identities', 'Automation', 'Ledger']) {
      expect(screen.getByRole('link', { name: new RegExp(`^${label}`) })).not.toHaveAttribute('title')
    }
    for (const icon of container.querySelectorAll('nav svg')) {
      expect(icon).toHaveAttribute('aria-hidden', 'true')
    }
  })
})

describe('溢出菜单', () => {
  beforeEach(() => {
    useLanguageStore.setState({ language: 'zh' })
    localStorage.clear()
  })

  it('收起时菜单不在 DOM 里，展开状态写在按钮上', () => {
    renderBar(0)

    const trigger = screen.getByLabelText(zh.moreAria)
    expect(trigger).toHaveAttribute('aria-expanded', 'false')
    expect(screen.queryByRole('menu')).not.toBeInTheDocument()

    fireEvent.click(trigger)

    expect(trigger).toHaveAttribute('aria-expanded', 'true')
    expect(screen.getByRole('menu', { name: zh.moreAria })).toBeInTheDocument()
  })

  it('Esc 收回菜单并把焦点还给触发它的按钮', () => {
    renderBar(0)

    const trigger = screen.getByLabelText(zh.moreAria)
    fireEvent.click(trigger)
    fireEvent.keyDown(screen.getByRole('menu'), { key: 'Escape' })

    expect(screen.queryByRole('menu')).not.toBeInTheDocument()
    expect(document.activeElement).toBe(trigger)
  })

  it('选完一项菜单就收起，焦点回到按钮', () => {
    renderBar(0)

    fireEvent.click(screen.getByLabelText(zh.moreAria))
    fireEvent.click(screen.getByRole('menuitem', { name: zh.morePreferences }))

    expect(screen.queryByRole('menu')).not.toBeInTheDocument()
    expect(document.activeElement).toBe(screen.getByLabelText(zh.moreAria))
  })

  it('宽屏上菜单里只有偏好 —— 语言与主题还在顶栏上', () => {
    renderBar(0)

    fireEvent.click(screen.getByLabelText(zh.moreAria))

    expect(screen.getAllByRole('menuitem')).toHaveLength(1)
    expect(screen.getByRole('menuitem', { name: zh.morePreferences })).toBeInTheDocument()
  })
})
