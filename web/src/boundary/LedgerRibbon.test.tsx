import { QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { createQueryClient } from '../data/queryClient'
import { copyFor } from '../i18n/copy'
import { useLanguageStore } from '../state/language'

import { LedgerRibbon } from './LedgerRibbon'
import { clockTimeOf } from './ledgerText'

const zh = copyFor('zh')

const event = (id: string, verdict: string) => ({
  id,
  type: 'request.decided',
  verdict,
  outcome: 'succeeded',
  service: 'github',
  agent_id: 'ag-1',
  created_at: new Date().toISOString(),
})

const json = (body: unknown, status = 200) =>
  Promise.resolve(
    new Response(JSON.stringify(body), {
      status,
      headers: { 'Content-Type': 'application/json; charset=utf-8' },
    }),
  )

function renderRibbon(respond: () => Promise<Response>) {
  vi.stubGlobal('fetch', respond)
  return render(
    <QueryClientProvider client={createQueryClient()}>
      <MemoryRouter>
        <LedgerRibbon />
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

describe('账本条', () => {
  beforeEach(() => {
    document.head.innerHTML = '<meta name="opendelo-session-token" content="test-session-token">'
    useLanguageStore.setState({ language: 'zh' })
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    document.head.innerHTML = ''
  })

  it('给出今日的通行与拒绝次数', async () => {
    renderRibbon(() => json({ items: [event('a', 'allow'), event('b', 'deny')] }))

    expect(await screen.findByText(zh.ledgerCounts(1, 1, false))).toBeInTheDocument()
  })

  it('通向完整账本的入口指向 /ledger', async () => {
    renderRibbon(() => json({ items: [event('a', 'allow')] }))

    expect(await screen.findByRole('link', { name: zh.ledgerSeeAll })).toHaveAttribute('href', '/ledger')
  })

  it('账本读不到时说清已记下的没有丢，而不是显示 0 次通行', async () => {
    // 显示 0 会让人以为今天什么都没发生。
    renderRibbon(() => Promise.reject(new Error('connection refused')))

    expect(await screen.findByText(zh.ledgerUnavailable)).toBeInTheDocument()
  })

  it('今天还没有记录时给一句安静的说明', async () => {
    renderRibbon(() => json({ items: [] }))

    expect(await screen.findByText(zh.ledgerEmpty)).toBeInTheDocument()
  })

  it('不使用任何图表（设计稿 §07）', async () => {
    const { container } = renderRibbon(() => json({ items: [event('a', 'allow')] }))
    await screen.findByText(zh.ledgerSeeAll)

    expect(container.querySelector('canvas')).toBeNull()
    expect(container.querySelectorAll('svg')).toHaveLength(0)
  })

  it('时间只留时分', () => {
    expect(clockTimeOf('2026-07-29T09:05:00.000Z')).toMatch(/^\d{2}:\d{2}$/)
    expect(clockTimeOf('not a time')).toBe('')
  })
})
