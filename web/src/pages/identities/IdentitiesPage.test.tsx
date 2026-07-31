import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import { QueryClientProvider } from '@tanstack/react-query'
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { createQueryClient } from '../../data/queryClient'
import { copyFor } from '../../i18n/copy'
import { useLanguageStore } from '../../state/language'

import { IdentitiesPage } from './IdentitiesPage'

/*
 * Identities 关系工作台（REQ-UI-005、REQ-IDENT-003）。
 *
 * 走真实链路：从入口文档读令牌，经 requestGateway 发请求，由 Zod 解析。
 */

const zh = copyFor('zh')
const css = readFileSync(resolve(process.cwd(), 'src/pages/identities/IdentitiesPage.module.css'), 'utf8')

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
      last_seen_at: '2026-07-30T08:59:00Z',
    },
  ],
}

const identities = {
  items: [
    {
      id: 'id-1',
      service: 'github',
      account_label: 'ops@example.com',
      environment: 'production',
      is_default: true,
      status: 'active',
    },
  ],
}

const leases = (agentId = 'ag-9') => ({
  items: [
    {
      id: 'ls-1',
      agent_id: agentId,
      identity_id: 'id-1',
      service: 'github',
      resource_scope: { repo: 'Runcoor/opendelo' },
      expires_at: new Date(Date.now() + 15 * 60_000).toISOString(),
      status: 'active',
      is_session_bound: false,
    },
  ],
})

const memories = {
  items: [
    {
      id: 'tm-1',
      agent_id: 'ag-1',
      workspace_id: 'ws-1',
      identity_id: 'id-1',
      service: 'github',
      environment: 'production',
      risk_ceiling: 'medium',
      approval_behavior: 'auto_allow',
      created_from: 'ap-1',
      status: 'active',
      invalidation_reason: '',
      expires_at: '2026-08-30T09:00:00Z',
      created_at: '2026-07-01T09:00:00Z',
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

/** 记下每次请求：这一页不该产生任何非 GET 请求。 */
const calls: { path: string; method: string }[] = []

interface Responses {
  readonly agents?: () => Promise<Response>
  readonly identities?: () => Promise<Response>
  readonly leases?: () => Promise<Response>
}

function renderPage(responses: Responses = {}) {
  vi.stubGlobal('fetch', (path: string, init?: RequestInit) => {
    calls.push({ path, method: init?.method ?? 'GET' })
    if (path.startsWith('/v1/agents')) {
      return (responses.agents ?? (() => json(agents)))()
    }
    if (path.startsWith('/v1/identities')) {
      return (responses.identities ?? (() => json(identities)))()
    }
    if (path.startsWith('/v1/leases')) {
      return (responses.leases ?? (() => json({ items: [] })))()
    }
    if (path.startsWith('/v1/trust-memories')) {
      return json(memories)
    }
    return json({ items: [] })
  })

  return render(
    <QueryClientProvider client={createQueryClient()}>
      <MemoryRouter initialEntries={['/identities']}>
        <Routes>
          <Route path="/identities" element={<IdentitiesPage />} />
          <Route path="/automation/advanced/:ruleId" element={<p>规则文书</p>} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

/**
 * 两列各自定位。
 *
 * Agent 的名字在右列的 Lease 标签上也会出现（谁拿着这条授权），
 * 因此按名字全局找会同时命中两处 —— 那样的用例分不清点到的是哪一张卡。
 */
const outside = () => within(screen.getByRole('region', { name: zh.identitiesOutside }))
const inside = () => within(screen.getByRole('region', { name: zh.identitiesInside }))

async function flush() {
  await act(async () => {
    await new Promise((resolve) => {
      setTimeout(resolve, 0)
    })
  })
}

describe('Identities 的三态', () => {
  beforeEach(() => {
    document.head.innerHTML = '<meta name="opendelo-session-token" content="test-session-token">'
    useLanguageStore.setState({ language: 'zh' })
    calls.length = 0
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    document.head.innerHTML = ''
  })

  it('加载中：两列的位置已经占好，缝在原处', () => {
    const { container } = renderPage({ agents: () => new Promise<Response>(() => undefined) })

    expect(screen.getByText(zh.identitiesOutside)).toBeInTheDocument()
    expect(container.innerHTML).toContain('seam')
  })

  it('空：说明这一列为什么空着，缝仍在', async () => {
    renderPage({ agents: () => json({ items: [] }), identities: () => json({ items: [] }) })

    expect(await screen.findByText(zh.identitiesAgentsEmpty)).toBeInTheDocument()
    expect(screen.getByText(zh.identitiesDestsEmpty)).toBeInTheDocument()
  })

  it('读不回来时说明发生了什么，并说清已经签发的授权不受影响', async () => {
    renderPage({ agents: () => json({ error: { code: 'internal', message: '坏了', operation_id: 'op-1' } }, 500) })

    expect(await screen.findByText(zh.identitiesErrorTitle)).toBeInTheDocument()
    expect(screen.getByText(zh.identitiesErrorBlurb)).toBeInTheDocument()
  })
})

describe('Identities 的内容', () => {
  beforeEach(() => {
    document.head.innerHTML = '<meta name="opendelo-session-token" content="test-session-token">'
    useLanguageStore.setState({ language: 'zh' })
    calls.length = 0
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    document.head.innerHTML = ''
  })

  it('不是表格式密码列表：两列是关系，页面上没有 table（AC1）', async () => {
    const { container } = renderPage()

    await screen.findByText('writer-agent')
    expect(container.querySelector('table')).toBeNull()
    expect(screen.getByText(zh.identitiesOutside)).toBeInTheDocument()
    expect(screen.getByText(zh.identitiesInside)).toBeInTheDocument()
  })

  it('每个 Agent 卡片说明请求来自哪台设备（AC2）', async () => {
    renderPage()

    expect(await screen.findByText(zh.identitiesDevice('000042'))).toBeInTheDocument()
  })

  it('Destination 卡片带上规则摘要与活跃 Lease 的持有者和剩余时间（AC3）', async () => {
    renderPage({ leases: () => json(leases('ag-1')) })

    expect(await screen.findByText(zh.identitiesRuleSummary(1, 'medium'))).toBeInTheDocument()
    await waitFor(() => {
      expect(inside().getByText('writer-agent')).toBeInTheDocument()
    })
    // 剩余时间的写法本身由 leases 的用例守；这里要的是「它确实显示了一个剩余时间」。
    expect(inside().getByText(/^\d+[ms]$/)).toBeInTheDocument()
  })

  it('页面上没有任何凭据字段 —— 连指针都不取', async () => {
    const { container } = renderPage()

    await screen.findByText('writer-agent')
    expect(container.innerHTML).not.toContain('credential')
  })

  it('连接身份的按钮是禁用的，而不是点了没反应', async () => {
    renderPage()

    const connect = await screen.findByRole('button', { name: zh.identitiesConnect })
    expect(connect).toBeDisabled()
    expect(connect).toHaveAttribute('title', zh.identitiesConnectDisabled)
  })
})

describe('拖放建立关系（REQ-IDENT-003）', () => {
  beforeEach(() => {
    document.head.innerHTML = '<meta name="opendelo-session-token" content="test-session-token">'
    useLanguageStore.setState({ language: 'zh' })
    calls.length = 0
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    document.head.innerHTML = ''
  })

  it('放下之后得到的是草稿，且全程没有一个非 GET 请求（AC1）', async () => {
    renderPage()

    await screen.findByText('writer-agent')
    fireEvent.dragStart(outside().getByRole('button', { name: /writer-agent/ }))
    fireEvent.drop(inside().getByRole('button', { name: /github\/ops@example\.com/ }))
    await flush()

    expect(screen.getByText(zh.identitiesDraft('writer-agent', 'github/ops@example.com'))).toBeInTheDocument()
    expect(screen.getByText(zh.identitiesDraftBlurb)).toBeInTheDocument()
    expect(calls.filter((call) => call.method !== 'GET')).toEqual([])
  })

  it('草稿要到规则文书里签署才生效（AC2）', async () => {
    renderPage()

    await screen.findByText('writer-agent')
    fireEvent.dragStart(outside().getByRole('button', { name: /writer-agent/ }))
    fireEvent.drop(inside().getByRole('button', { name: /github\/ops@example\.com/ }))

    expect(screen.getByRole('link', { name: zh.identitiesDraftSign })).toHaveAttribute(
      'href',
      '/automation/advanced/draft?agent=ag-1&identity=id-1',
    )
  })

  it('不用鼠标也走得通：拿起与放下都是按钮上的一次确认', async () => {
    renderPage()

    await screen.findByText('writer-agent')
    const agent = outside().getByRole('button', { name: /writer-agent/ })
    fireEvent.click(agent)
    expect(screen.getByText(zh.identitiesPickedUp('writer-agent'))).toBeInTheDocument()
    expect(agent).toHaveAttribute('aria-pressed', 'true')

    fireEvent.click(inside().getByRole('button', { name: /github\/ops@example\.com/ }))
    expect(screen.getByText(zh.identitiesDraft('writer-agent', 'github/ops@example.com'))).toBeInTheDocument()
  })

  it('手上没拿东西时点 Destination 不会凭空生出一条草稿', async () => {
    renderPage()

    await screen.findByText('writer-agent')
    fireEvent.click(inside().getByRole('button', { name: /github\/ops@example\.com/ }))

    expect(screen.queryByText(zh.identitiesDraftTitle)).not.toBeInTheDocument()
  })

  it('已经握着授权的组合不再签一次，并说明为什么', async () => {
    renderPage({ leases: () => json(leases('ag-1')) })

    // 等这一处的持有者出现：它到齐了才谈得上「已经握着授权」。
    await screen.findByText(zh.identitiesRuleSummary(1, 'medium'))
    await waitFor(() => {
      expect(inside().getByText('writer-agent')).toBeInTheDocument()
    })
    fireEvent.click(outside().getByRole('button', { name: /writer-agent/ }))
    fireEvent.click(inside().getByRole('button', { name: /github\/ops@example\.com/ }))

    expect(screen.getByText(zh.identitiesAlreadyHolds)).toBeInTheDocument()
    expect(screen.queryByText(zh.identitiesDraftTitle)).not.toBeInTheDocument()
  })

  it('丢掉草稿之后它就不在了', async () => {
    renderPage()

    await screen.findByText('writer-agent')
    fireEvent.click(outside().getByRole('button', { name: /writer-agent/ }))
    fireEvent.click(inside().getByRole('button', { name: /github\/ops@example\.com/ }))
    fireEvent.click(screen.getByRole('button', { name: zh.identitiesDraftDiscard }))

    expect(screen.queryByText(zh.identitiesDraftTitle)).not.toBeInTheDocument()
  })
})

describe('Identities 的布局', () => {
  it('收窄折的是卡片，两列与缝都留在原处（REQ-UI-002、PRD §24「保留 Boundary」）', () => {
    const narrow = /@media \(max-width: 1279px\)\s*\{([\s\S]*?)\n\}/.exec(css)?.[1] ?? ''

    // 「两层视图」折的是每张卡片的第二层信息。把两列改成上下两层会让缝
    // 横穿两段内容，而 PRD §24 在这个宽度上仍然要求保留 Boundary。
    expect(narrow).toContain('.tail')
    expect(narrow).not.toContain('.columns')
    // 缝的几何不在这一页里实现，收窄也不许在这里出现它。
    expect(narrow).not.toContain('seam')
    expect(css).not.toContain('.seam')
  })

  it('两列各占一半，谁也不能把缝挤开', () => {
    const column = /\.column \{([\s\S]*?)\n\}/.exec(css)?.[1] ?? ''

    expect(column).toContain('flex: 1')
    expect(column).toContain('min-width: 0')
  })
})
