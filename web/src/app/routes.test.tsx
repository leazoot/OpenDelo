import { QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import { createMemoryRouter, RouterProvider } from 'react-router'
import { beforeEach, describe, expect, it } from 'vitest'

import { createQueryClient } from '../data/queryClient'
import { copyFor } from '../i18n/copy'
import { useLanguageStore } from '../state/language'

import { PAGES, pageTitleOf, routes } from './routes'

/*
 * 七条路由（REQ-UI-001 AC2）。
 *
 * 没有令牌，因此每个查询都在发请求之前就失败 —— 页面必须照常渲染出来。
 * 「Gateway 连不上时界面仍然可用」本身就是要守住的行为（REQ-GATEWAY-003 AC2）。
 */

function renderAt(path: string) {
  const router = createMemoryRouter(routes, { initialEntries: [path] })
  return render(
    <QueryClientProvider client={createQueryClient()}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  )
}

const zh = copyFor('zh')

describe('路由', () => {
  beforeEach(() => {
    useLanguageStore.setState({ language: 'zh' })
  })

  it.each([
    ['/gate', 'Outside · 代理侧'],
    // 卷宗要去 Gateway 取请求详情，没有令牌就停在加载态 —— 这一页仍然要出现。
    ['/gate/folio/01JABC', zh.folioLoading],
    ['/identities', zh.identitiesOutside],
    ['/automation', zh.automationLoading],
    ['/automation/advanced/notes-write', zh.manuscriptLoading],
    ['/ledger', zh.ledgerPageLoading],
    ['/preferences', zh.prefsBlurb],
  ])('直接访问 %s 能渲染出这一页', (path, marker) => {
    renderAt(path)

    expect(screen.getByText(new RegExp(marker.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')))).toBeInTheDocument()
  })

  it('根路径落到 Gate', () => {
    renderAt('/')

    expect(screen.getByText('Outside · 代理侧')).toBeInTheDocument()
  })

  it('认不出的路径给出说明与回到 Gate 的出口，而不是白屏', () => {
    renderAt('/nowhere')

    expect(screen.getByText(zh.pageNotFound)).toBeInTheDocument()
    expect(screen.getByRole('link', { name: zh.backToGate })).toHaveAttribute('href', '/gate')
  })

  it('每条路由都在顶栏之下，顶栏因此在每一页都在', () => {
    renderAt('/preferences')

    expect(screen.getByRole('navigation', { name: zh.navLabel })).toBeInTheDocument()
  })
})

describe('页面标题', () => {
  it('每一条路由都有自己的标题，没有一条落到「未找到」', () => {
    // 加了路由却忘了给它标题时，标签页会说这一页不存在。
    for (const page of PAGES) {
      const pathname = page.path.replace(/:[^/]+/g, 'sample')
      expect(pageTitleOf(pathname, zh)).not.toBe(zh.pageNotFound)
      expect(pageTitleOf(pathname, zh)).toBe(page.title(zh))
    }
  })

  it('Folio 的标题不会被 Gate 抢走', () => {
    expect(pageTitleOf('/gate/folio/01JABC', zh)).toBe(zh.pageFolio)
    expect(pageTitleOf('/gate', zh)).toBe(zh.pageGate)
  })

  it('认不出的路径用「未找到」的标题', () => {
    expect(pageTitleOf('/nowhere', zh)).toBe(zh.pageNotFound)
  })
})
