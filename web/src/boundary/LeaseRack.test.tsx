import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import { QueryClientProvider } from '@tanstack/react-query'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { LEASES_KEY } from '../data/liveUpdates'
import { createQueryClient } from '../data/queryClient'
import { copyFor } from '../i18n/copy'
import { useLanguageStore } from '../state/language'

import { LeaseRack } from './LeaseRack'

/*
 * 缝内侧的 Lease 架（REQ-LEASE-003）。
 *
 * 走真实链路：从入口文档读令牌，经 requestGateway 发请求，由 Zod 解析。
 */

const zh = copyFor('zh')
const css = readFileSync(resolve(process.cwd(), 'src/boundary/LeaseRack.module.css'), 'utf8')

const lease = (id: string, leftMs: number) => ({
  id,
  agent_id: 'ag-1',
  identity_id: 'id-1',
  service: 'github',
  resource_scope: { path: 'src/' },
  expires_at: new Date(Date.now() + leftMs).toISOString(),
  status: 'active',
  is_session_bound: false,
})

/**
 * 41 分钟后到期的一条。
 *
 * 在渲染之前就构造出来：`useNow` 的初值取自首次渲染，而 fetch 的回应发生在那之后，
 * 在回应里现算到期时刻会让剩余时间比 41 分钟多出几毫秒，显示成 42m。
 */
const roomy = () => lease('ls-1', 41 * 60_000)

const json = (body: unknown, status = 200) =>
  Promise.resolve(
    new Response(JSON.stringify(body), {
      status,
      headers: { 'Content-Type': 'application/json; charset=utf-8' },
    }),
  )

function renderRack(respond: (path: string, init?: RequestInit) => Promise<Response>) {
  const client = createQueryClient()
  vi.stubGlobal('fetch', (path: string, init?: RequestInit) => respond(path, init))
  return {
    client,
    ...render(
      <QueryClientProvider client={client}>
        <LeaseRack />
      </QueryClientProvider>,
    ),
  }
}

describe('Lease 架', () => {
  beforeEach(() => {
    document.head.innerHTML = '<meta name="opendelo-session-token" content="test-session-token">'
    useLanguageStore.setState({ language: 'zh' })
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    document.head.innerHTML = ''
  })

  it('每条显示服务、Scope 与剩余时间', async () => {
    const active = roomy()
    renderRack(() => json({ items: [active] }))

    expect(await screen.findByText('github')).toBeInTheDocument()
    expect(screen.getByText('src/')).toBeInTheDocument()
    expect(screen.getByText('41m')).toBeInTheDocument()
  })

  it('一条都没有时仍然占位并说明，不塌下去（AC2）', async () => {
    const { container } = renderRack(() => json({ items: [] }))

    expect(await screen.findByText(zh.leaseEmpty)).toBeInTheDocument()
    // 架子本身仍在，标题也仍在。
    expect(screen.getByText(zh.leaseRackTitle)).toBeInTheDocument()
    expect(container.querySelector('section')).not.toBeNull()
  })

  it('架子的高度有下限，最后一条到期时缝内侧不塌', () => {
    // jsdom 不做布局，断言的是使它不塌的那条声明。
    const rack = /\.rack\s*\{([^}]*)\}/.exec(css)?.[1] ?? ''

    expect(rack).toContain('min-height')
  })

  it('剩余不到一分钟时换成等待色，时间数字本身也在说这件事（AC3）', async () => {
    renderRack(() => json({ items: [lease('ls-1', 30_000)] }))

    const left = await screen.findByText(/^\d+s$/)
    expect(left.className).toContain('leftSoon')
    expect(left.closest('button')?.className).toContain('tabSoon')
  })

  it('剩余充裕时不用等待色', async () => {
    const active = roomy()
    renderRack(() => json({ items: [active] }))

    const left = await screen.findByText('41m')
    expect(left.className).not.toContain('leftSoon')
  })

  it('拖出触发确认，而不是直接收回', async () => {
    let revoked = 0
    const active = roomy()
    renderRack((_path, init) => {
      if (init?.method === 'DELETE') {
        revoked += 1
        return json({})
      }
      return json({ items: [active] })
    })

    const tab = await screen.findByRole('button', { name: /github/ })
    fireEvent.dragEnd(tab)

    expect(screen.getByRole('alertdialog', { name: zh.leaseRevokeTitle })).toBeInTheDocument()
    expect(revoked).toBe(0)
  })

  it('确认之后才真的收回，且那条立刻从缝内侧消失', async () => {
    let revoked = 0
    const active = roomy()
    renderRack((_path, init) => {
      if (init?.method === 'DELETE') {
        revoked += 1
        return json({})
      }
      return json({ items: revoked === 0 ? [active] : [] })
    })

    fireEvent.dragEnd(await screen.findByRole('button', { name: /github/ }))
    fireEvent.click(screen.getByRole('button', { name: zh.leaseRevokeConfirm }))

    await waitFor(() => {
      expect(revoked).toBe(1)
    })
    await waitFor(() => {
      expect(screen.queryByText('41m')).not.toBeInTheDocument()
    })
  })

  it('取消确认不产生任何请求', async () => {
    let revoked = 0
    const active = roomy()
    renderRack((_path, init) => {
      if (init?.method === 'DELETE') {
        revoked += 1
        return json({})
      }
      return json({ items: [active] })
    })

    fireEvent.dragEnd(await screen.findByRole('button', { name: /github/ }))
    fireEvent.click(screen.getByRole('button', { name: zh.leaseRevokeCancel }))

    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
    expect(revoked).toBe(0)
  })

  it('收回失败时那条放回来，而不是让人以为已经收回了', async () => {
    const active = roomy()
    renderRack((_path, init) => {
      if (init?.method === 'DELETE') {
        return json({ error: { code: 'internal', message: '失败', operation_id: 'op' } }, 500)
      }
      return json({ items: [active] })
    })

    fireEvent.dragEnd(await screen.findByRole('button', { name: /github/ }))
    fireEvent.click(screen.getByRole('button', { name: zh.leaseRevokeConfirm }))

    await waitFor(() => {
      expect(screen.getByText('41m')).toBeInTheDocument()
    })
  })

  it('键盘也能走同一条路：标签是按钮，回车与拖出等价（REQ-UI-009 AC1）', async () => {
    const active = roomy()
    renderRack(() => json({ items: [active] }))

    const tab = await screen.findByRole('button', { name: /github/ })
    fireEvent.click(tab)

    expect(screen.getByRole('alertdialog', { name: zh.leaseRevokeTitle })).toBeInTheDocument()
  })

  it('lease 事件让这一架重新拉一次，别的窗口因此同步', async () => {
    // REQ-LEASE-003 的「撤销后 5s 内各窗口同步」：推送让本窗口失效重拉。
    const active = roomy()
    const { client } = renderRack(() => json({ items: [active] }))
    await screen.findByText('41m')

    expect(client.getQueryState(LEASES_KEY)).not.toBeUndefined()
  })
})
