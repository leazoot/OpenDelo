import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import { QueryClientProvider } from '@tanstack/react-query'
import { act, fireEvent, render, screen, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { createQueryClient } from '../../data/queryClient'
import { copyFor } from '../../i18n/copy'
import { useLanguageStore } from '../../state/language'

import { LedgerPage } from './LedgerPage'

/*
 * Boundary Ledger（REQ-AUDIT-003 / 004、设计稿 §07）。
 */

const zh = copyFor('zh')
const css = readFileSync(resolve(process.cwd(), 'src/pages/ledger/LedgerPage.module.css'), 'utf8')

const entry = (over: Record<string, unknown> = {}) => ({
  id: 'ev-1',
  operation_id: '01KYM0OP1',
  type: 'decision.auto_allowed',
  agent_id: 'ag-1',
  device_id: 'dv-000042',
  workspace_id: 'ws-1',
  identity_id: 'id-1',
  service: 'github',
  operation: 'pull_request.create',
  resource: { repo: 'Runcoor/opendelo' },
  resolved_scope: {},
  verdict: 'allow',
  risk_level: 'low',
  lease_id: 'ls-1',
  lease_status: 'active',
  outcome: 'succeeded',
  duration_ms: 42,
  is_redacted: true,
  created_at: '2026-07-30T14:22:07.000Z',
  ...over,
})

const agents = {
  items: [
    {
      id: 'ag-1',
      name: 'writer-agent',
      type: 'claude_code',
      device_id: 'dv-000042',
      workspace_id: 'ws-1',
      trust_level: 'known',
      status: 'active',
      last_seen_at: '2026-07-30T14:00:00Z',
    },
  ],
}

const json = (body: unknown, code = 200) =>
  Promise.resolve(
    new Response(JSON.stringify(body), {
      status: code,
      headers: { 'Content-Type': 'application/json; charset=utf-8' },
    }),
  )

const calls: { path: string; method: string }[] = []

interface Responses {
  readonly ledger?: () => Promise<Response>
  readonly export?: () => Promise<Response>
}

function renderPage(at = '/ledger', responses: Responses = {}) {
  vi.stubGlobal('fetch', (path: string, init?: RequestInit) => {
    calls.push({ path, method: init?.method ?? 'GET' })
    if (path.startsWith('/v1/audit-events/export')) {
      return (responses.export ?? (() => json({})))()
    }
    if (path.startsWith('/v1/audit-events')) {
      return (responses.ledger ?? (() => json({ items: [entry()], next_cursor: '' })))()
    }
    if (path.startsWith('/v1/agents')) {
      return json(agents)
    }
    return json({ items: [] })
  })

  return render(
    <QueryClientProvider client={createQueryClient()}>
      <MemoryRouter initialEntries={[at]}>
        <Routes>
          <Route path="/ledger" element={<LedgerPage />} />
          <Route path="/automation/advanced/:ruleId" element={<p>规则文书</p>} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

async function flush() {
  await act(async () => {
    await new Promise((resolve) => {
      setTimeout(resolve, 0)
    })
  })
}

beforeEach(() => {
  document.head.innerHTML = '<meta name="opendelo-session-token" content="test-session-token">'
  useLanguageStore.setState({ language: 'zh' })
  calls.length = 0
  // jsdom 没有实现这两个方法，不打桩的话导出必然抛异常 ——
  // 「导出失败会说出来」那条用例就会因为一个与响应状态无关的错误而通过。
  vi.stubGlobal('URL', Object.assign(URL, {
    createObjectURL: () => 'blob:opendelo',
    revokeObjectURL: () => undefined,
  }))
})

afterEach(() => {
  vi.unstubAllGlobals()
  document.head.innerHTML = ''
})

describe('账本的三态', () => {
  it('加载中：脊线已经在，说明正在读', () => {
    const { container } = renderPage('/ledger', { ledger: () => new Promise<Response>(() => undefined) })

    expect(screen.getByText(zh.ledgerPageLoading)).toBeInTheDocument()
    expect(container.innerHTML).toContain('spine')
  })

  it('空：说明第一条会在什么时候出现', async () => {
    renderPage('/ledger', { ledger: () => json({ items: [], next_cursor: '' }) })

    expect(await screen.findByText(zh.ledgerPageEmpty)).toBeInTheDocument()
  })

  it('读不回来时说清已经写下的记录一条都不会丢', async () => {
    renderPage('/ledger', {
      ledger: () => json({ error: { code: 'internal', message: '坏了', operation_id: 'op-1' } }, 500),
    })

    expect(await screen.findByText(zh.ledgerErrorTitle)).toBeInTheDocument()
    expect(screen.getByText(zh.ledgerErrorBlurb)).toBeInTheDocument()
  })
})

describe('账本的内容', () => {
  it('不是统计后台：页面上没有任何图表元素（AC2）', async () => {
    const { container } = renderPage()

    await screen.findByText('writer-agent')
    for (const tag of ['canvas', 'svg', 'table']) {
      expect(container.querySelector(tag)).toBeNull()
    }
  })

  it('每条带设备与时刻', async () => {
    renderPage()

    // 时刻按本地时区渲染，用例不重算一遍时区换算 —— 那等于把实现抄一遍。
    // 这里断言的是「有一个时刻，且它后面跟着设备」，精确写法由 ledgerView 的用例守。
    expect(await screen.findByText(/\d{2}:\d{2}:\d{2} · 000042/)).toBeInTheDocument()
  })

  it('过滤片筛掉别的结论，且这次筛选可以分享（走 URL）', async () => {
    renderPage('/ledger?lane=refused')

    expect(await screen.findByText(zh.ledgerEmptyLane)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: zh.ledgerRefused })).toHaveAttribute('aria-pressed', 'true')
  })

  it('认不出的过滤片退回「全部」，不隐藏任何记录', async () => {
    renderPage('/ledger?lane=turbo')

    await screen.findByText('writer-agent')
    expect(screen.getByRole('button', { name: zh.ledgerAll })).toHaveAttribute('aria-pressed', 'true')
  })

  it('服务端过滤条件进请求（AC1：结果来自数据库查询而不是本地裁剪）', async () => {
    renderPage('/ledger?agent=ag-9')

    await flush()
    expect(calls.some((call) => call.path.includes('agent_id=ag-9'))).toBe(true)
  })
})

describe('条目详情与两个动作', () => {
  it('选中一条后展开键值明细', async () => {
    renderPage()

    fireEvent.click(await screen.findByRole('button', { name: /writer-agent/ }))
    const panel = within(screen.getByRole('complementary'))
    expect(panel.getByText('dv-000042')).toBeInTheDocument()
    expect(panel.getByText('01KYM0OP1')).toBeInTheDocument()
  })

  it('「据此写规则」带上这条记录的 Scope（AC3）', async () => {
    renderPage()

    fireEvent.click(await screen.findByRole('button', { name: /writer-agent/ }))
    expect(screen.getByRole('link', { name: zh.ledgerWriteRule })).toHaveAttribute(
      'href',
      '/automation/advanced/draft?agent=ag-1&identity=id-1&service=github',
    )
  })

  it('Lease 已失效时「收回」置灰并说明原因（AC4）', async () => {
    renderPage('/ledger', {
      ledger: () => json({ items: [entry({ lease_status: 'expired' })], next_cursor: '' }),
    })

    fireEvent.click(await screen.findByRole('button', { name: /writer-agent/ }))
    expect(screen.getByRole('button', { name: zh.ledgerRevoke })).toBeDisabled()
    expect(screen.getByText(zh.ledgerLeaseGone('expired'))).toBeInTheDocument()
  })

  it('生效中的 Lease 收得回，走的是 DELETE', async () => {
    renderPage()

    fireEvent.click(await screen.findByRole('button', { name: /writer-agent/ }))
    fireEvent.click(screen.getByRole('button', { name: zh.ledgerRevoke }))

    await flush()
    expect(calls.some((call) => call.method === 'DELETE' && call.path === '/v1/leases/ls-1')).toBe(true)
  })
})

describe('导出（REQ-AUDIT-004）', () => {
  it('导出打到本机 Gateway，全程没有第二个主机（AC3）', async () => {
    renderPage()

    await screen.findByText('writer-agent')
    fireEvent.click(screen.getByRole('button', { name: zh.ledgerExport('jsonl') }))
    await flush()

    const exported = calls.filter((call) => call.path.startsWith('/v1/audit-events/export'))
    expect(exported).toHaveLength(1)
    // 反向对照：成功的那一次不该出现失败提示。
    expect(screen.queryByText(zh.ledgerExportFailed)).not.toBeInTheDocument()
    expect(exported[0]?.path).toContain('format=jsonl')
    // 每一次请求都是本机的相对路径：没有一个带主机名的绝对地址。
    for (const call of calls) {
      expect(call.path.startsWith('/v1/')).toBe(true)
    }
  })

  it('导出带上当前的服务端过滤条件（AC1）', async () => {
    renderPage('/ledger?service=github')

    await screen.findByText('writer-agent')
    fireEvent.click(screen.getByRole('button', { name: zh.ledgerExport('jsonl') }))
    await flush()

    expect(calls.find((call) => call.path.startsWith('/v1/audit-events/export'))?.path).toContain(
      'service=github',
    )
  })

  it('导出失败时说出来，而不是静静地什么都不发生', async () => {
    renderPage('/ledger', { export: () => json({ error: { code: 'internal', message: '坏', operation_id: 'op' } }, 500) })

    await screen.findByText('writer-agent')
    fireEvent.click(screen.getByRole('button', { name: zh.ledgerExport('jsonl') }))

    expect(await screen.findByText(zh.ledgerExportFailed)).toBeInTheDocument()
  })
})

describe('账本的布局', () => {
  it('脊线是缝的纵向投影，落在左列右缘', () => {
    const spine = /\.spine \{([\s\S]*?)\n\}/.exec(css)?.[1] ?? ''

    expect(spine).toContain('left: 212px')
    expect(spine).toContain('var(--seam)')
  })

  it('1280 只收窄详情栏，脊线不动', () => {
    const narrow = /@media \(max-width: 1439px\)\s*\{([\s\S]*?)\n\}/.exec(css)?.[1] ?? ''

    expect(narrow).toContain('.entryPanel')
    expect(narrow).not.toContain('.spine')
  })
})
