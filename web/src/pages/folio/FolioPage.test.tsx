import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import { QueryClientProvider } from '@tanstack/react-query'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { createQueryClient } from '../../data/queryClient'
import { copyFor } from '../../i18n/copy'
import { useLanguageStore } from '../../state/language'
import { SENTINEL_TOKEN } from '../../test/sentinel'

import { FolioPage } from './FolioPage'

/*
 * Access Folio（REQ-APPROVAL-001 / 004）。
 *
 * 走真实链路：从入口文档读令牌，经 requestGateway 发请求，由 Zod 解析。
 */

const zh = copyFor('zh')
const css = readFileSync(resolve(process.cwd(), 'src/pages/folio/FolioPage.module.css'), 'utf8')

const request = {
  id: 'rq-1',
  agent_id: 'ag-1',
  workspace_id: 'ws-1',
  service: 'cloudflare',
  operation: 'update_dns_record',
  resource: { zone: 'example.com', record: 'www' },
  desired_change: { content: '203.0.113.9' },
  change_preview: null,
  reason: '把 www 指到新机器',
  status: 'awaiting_approval',
  withheld_operations: null,
  created_at: '2026-07-29T09:00:00Z',
  decision: {
    verdict: 'require_approval',
    risk_level: 'medium',
    risk_factors: ['adapter_declared_label', 'production_write'],
    identity_id: 'id-1',
    reason_code: 'requires_confirmation',
    resolved_scope: {
      operation: 'update_dns_record',
      resource: { zone: 'example.com' },
      not_before: '2026-07-29T09:00:00Z',
      expires_at: '2026-07-29T09:15:00Z',
      request_limit: 1,
    },
  },
}

const agents = { items: [{ id: 'ag-1', name: 'writer-agent', type: 'claude_code', device_id: 'dv-1', workspace_id: 'ws-1', trust_level: 'known', status: 'active', last_seen_at: '2026-07-29T09:00:00Z' }] }
const identities = {
  items: [
    {
      id: 'id-1',
      service: 'cloudflare',
      account_label: 'ops@example.com',
      environment: 'production',
      is_default: true,
      status: 'active',
    },
  ],
}
const status = {
  status: 'running',
  version: 'test',
  listen_address: '127.0.0.1',
  web_api_port: 8787,
  started_at: '2026-07-29T00:00:00Z',
}

const json = (body: unknown, code = 200) =>
  Promise.resolve(
    new Response(JSON.stringify(body), {
      status: code,
      headers: { 'Content-Type': 'application/json; charset=utf-8' },
    }),
  )

const failure = (code: number) =>
  json({ error: { code: 'not_found', message: '没有这条请求', operation_id: 'op-1' } }, code)

/** 记下每次请求的方法，用来证明这一页不产生任何决策。 */
const calls: { path: string; method: string; body: string }[] = []

/** 审批项的主键与可用操作只在 `GET /v1/approvals` 里。 */
const approvals = (
  available: readonly string[] = ['allow_once', 'allow_until_task_end', 'auto_allow_in_project', 'always_ask', 'deny'],
) => ({
  items: [
    {
      id: 'ap-1',
      status: 'pending',
      created_at: '2026-07-29T09:00:00Z',
      available_actions: available,
      request: { ...request, decision: null },
      decision: null,
    },
  ],
})

/** 解锁的答复由每个用例自己决定；默认成功。 */
let unlockResponse: () => Promise<Response> = () => json({ unlocked: true })

function respondWith(folio: () => Promise<Response>, pending: () => Promise<Response>) {
  return (path: string, init?: RequestInit) => {
    calls.push({ path, method: init?.method ?? 'GET', body: typeof init?.body === 'string' ? init.body : '' })
    if (path === '/v1/vault/unlock') {
      return unlockResponse()
    }
    if (path.startsWith('/v1/approvals')) {
      return init?.method === 'POST' ? json({}) : pending()
    }
    if (path.startsWith('/v1/capability-requests/')) {
      return folio()
    }
    if (path === '/v1/agents') {
      return json(agents)
    }
    if (path === '/v1/identities') {
      return json(identities)
    }
    return json(status)
  }
}

function renderFolio(folio: () => Promise<Response>, pending = () => json(approvals())) {
  vi.stubGlobal('fetch', respondWith(folio, pending))
  return render(
    <QueryClientProvider client={createQueryClient()}>
      <MemoryRouter initialEntries={['/gate/folio/rq-1']}>
        <Routes>
          <Route path="/gate" element={<p>缝前</p>} />
          <Route path="/gate/folio/:id" element={<FolioPage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

/**
 * 把已经排上队的 promise 跑完。
 *
 * 断言「第二次没有发生」时必须先冲一次队列：请求是在微任务里发出的，
 * 同步断言会因为「还没来得及发」而通过，那样这条用例守不住任何东西。
 */
async function flush() {
  await act(async () => {
    await new Promise((resolve) => {
      setTimeout(resolve, 0)
    })
  })
}

const settled = () => json({ ...request, status: 'approved' })

describe('Access Folio 的三态', () => {
  beforeEach(() => {
    document.head.innerHTML = '<meta name="opendelo-session-token" content="test-session-token">'
    useLanguageStore.setState({ language: 'zh' })
    calls.length = 0
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    document.head.innerHTML = ''
  })

  it('加载中：卷宗的位置已经占好，缝在原处', () => {
    const { container } = renderFolio(() => new Promise<Response>(() => undefined))

    expect(screen.getByText(zh.folioLoading)).toBeInTheDocument()
    expect(container.innerHTML).toContain('seam')
  })

  it('读不回来时说明发生了什么，并给出下一步', async () => {
    renderFolio(() => failure(500))

    expect(await screen.findByText(zh.folioErrorTitle)).toBeInTheDocument()
    expect(screen.getByRole('link', { name: zh.backToGate })).toHaveAttribute('href', '/gate')
  })

  it('这条请求不在了与读不回来是两句不同的话', async () => {
    renderFolio(() => failure(404))

    expect(await screen.findByText(zh.folioMissingTitle)).toBeInTheDocument()
  })
})

describe('Access Folio 的内容', () => {
  beforeEach(() => {
    document.head.innerHTML = '<meta name="opendelo-session-token" content="test-session-token">'
    useLanguageStore.setState({ language: 'zh' })
    calls.length = 0
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    document.head.innerHTML = ''
  })

  it('直接访问 /gate/folio/:id 就能展开完整卷宗（REQ-APPROVAL-004 AC1）', async () => {
    renderFolio(() => json(request))

    expect(await screen.findByText(zh.folioArrival)).toBeInTheDocument()
    expect(screen.getByText(zh.folioConsequence)).toBeInTheDocument()
  })

  it('PRD §13.1 的十一项都在这一页上', async () => {
    renderFolio(() => json(request))

    // 左页：谁、哪个项目、哪个身份、哪个服务、哪个资源、什么变化、为什么是这个风险。
    expect(await screen.findByText('writer-agent')).toBeInTheDocument()
    expect(screen.getByText('ws-1')).toBeInTheDocument()
    expect(screen.getByText('ops@example.com · production')).toBeInTheDocument()
    expect(screen.getByText('cloudflare')).toBeInTheDocument()
    expect(screen.getAllByText('example.com · www').length).toBeGreaterThan(0)
    expect(screen.getByText('203.0.113.9')).toBeInTheDocument()
    expect(screen.getByText(`${zh.riskMedium} · ${zh.factorDeclaredLabel} · ${zh.factorProductionWrite}`)).toBeInTheDocument()

    // 右页：仍然关闭的权限、授权期限、可撤销、可回滚。
    expect(screen.getByText(zh.folioWithheldCredential)).toBeInTheDocument()
    expect(screen.getByText(zh.folioWithheldOperation('update_dns_record'))).toBeInTheDocument()
    expect(screen.getByText('15m')).toBeInTheDocument()
    expect(screen.getByText(zh.folioRevocable)).toBeInTheDocument()
    expect(screen.getByText(zh.folioRollbackPossible)).toBeInTheDocument()
  })

  it('只显示新值时把这一点说出来，不让它被当成「原来的样子」', async () => {
    renderFolio(() => json(request))

    expect(await screen.findByText(zh.folioChangeBaseline)).toBeInTheDocument()
  })

  it('查勘查到旧值时摆出「原值 → 新值」的对照', async () => {
    // REQ-APPROVAL-001 AC4：旧值来自审批之前的一次只读查询，不是从请求里推的。
    renderFolio(() =>
      json({
        ...request,
        change_preview: [
          { resource: 'www.example.com', field: 'content', before: '203.0.113.1', after: '203.0.113.9' },
        ],
      }),
    )

    expect(await screen.findByText('203.0.113.1')).toBeInTheDocument()
    expect(screen.getByText('203.0.113.9')).toBeInTheDocument()
    expect(screen.getByText(zh.folioChangePreviewed)).toBeInTheDocument()
    // 「尚未查询」那句必须消失：两句话同时在场就是自相矛盾。
    expect(screen.queryByText(zh.folioChangeBaseline)).not.toBeInTheDocument()
  })

  it('查勘没查到的字段旧值留空，不拿新值充数', async () => {
    renderFolio(() =>
      json({
        ...request,
        desired_change: { content: '203.0.113.9', ttl: 60 },
        change_preview: [
          { resource: 'www.example.com', field: 'content', before: '203.0.113.1', after: '203.0.113.9' },
        ],
      }),
    )

    expect(await screen.findByText(zh.folioChangeAbsent)).toBeInTheDocument()
  })

  it('仍然关闭的操作逐个点名，多出来的数目照实说', async () => {
    renderFolio(() =>
      json({
        ...request,
        withheld_operations: ['create_dns_record', 'delete_dns_record', 'purge_cache', 'read_dns_record'],
      }),
    )

    expect(
      await screen.findByText(
        zh.folioWithheldOperations(['create_dns_record', 'delete_dns_record', 'purge_cache'], 1),
      ),
    ).toBeInTheDocument()
    // 那句笼统的否定句被替掉了，而不是两句一起说。
    expect(screen.queryByText(zh.folioWithheldOperation('update_dns_record'))).not.toBeInTheDocument()
  })

  it('读操作明说不改变任何东西', async () => {
    renderFolio(() => json({ ...request, desired_change: null }))

    expect(await screen.findByText(zh.folioChangeNone)).toBeInTheDocument()
  })

  it('凭据的位置在这一页上不存在 —— 响应里塞进哨兵也渲染不出来', async () => {
    // 八个面里的 Console DOM 那一面（REQ-APPROVAL-001 AC2、REQ-NFR-002 AC1）。
    const { container } = renderFolio(() =>
      json({ ...request, authorization: SENTINEL_TOKEN, credential: { token: SENTINEL_TOKEN } }),
    )

    await waitFor(() => {
      expect(screen.getByText(zh.folioConsequence)).toBeInTheDocument()
    })
    expect(container.textContent).not.toContain(SENTINEL_TOKEN)
  })

  it('已被处理时显示提示而非报错（REQ-APPROVAL-004 AC3）', async () => {
    renderFolio(settled)

    expect(await screen.findByText(zh.folioSettled)).toBeInTheDocument()
    expect(screen.queryByText(zh.folioErrorTitle)).not.toBeInTheDocument()
    // 仍然是一份完整的卷宗：它当时的样子还看得见。
    expect(screen.getByText(zh.folioArrival)).toBeInTheDocument()
  })

  it('等待中的那条不显示「已处理」', async () => {
    renderFolio(() => json(request))

    await screen.findByText(zh.folioArrival)
    expect(screen.queryByText(zh.folioSettled)).not.toBeInTheDocument()
  })
})

describe('折回缝内', () => {
  beforeEach(() => {
    document.head.innerHTML = '<meta name="opendelo-session-token" content="test-session-token">'
    useLanguageStore.setState({ language: 'zh' })
    calls.length = 0
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    document.head.innerHTML = ''
  })

  it('Esc 折回 Gate，且不产生任何决策（REQ-APPROVAL-004 AC2）', async () => {
    renderFolio(() => json(request))
    await screen.findByText(zh.folioArrival)

    fireEvent.keyDown(window, { key: 'Escape' })

    expect(await screen.findByText('缝前')).toBeInTheDocument()
    expect(calls.filter((call) => call.method !== 'GET')).toEqual([])
  })

  it('折回与打开这两个按键不产生决策', async () => {
    renderFolio(() => json(request))
    await screen.findByText(zh.folioArrival)

    // `↵` 在缝前是「打开卷宗」，卷宗已经开着，它不该顺手变成一次同意。
    fireEvent.keyDown(window, { key: 'Enter' })
    await waitFor(() => {
      expect(screen.getByText(zh.folioArrival)).toBeInTheDocument()
    })
    expect(calls.filter((call) => call.method !== 'GET')).toEqual([])
  })

  it('折回也是一条链接，后退键因此等价于 Esc', async () => {
    renderFolio(() => json(request))

    expect(await screen.findByRole('link', { name: new RegExp(zh.folioBack) })).toHaveAttribute('href', '/gate')
  })
})

describe('卷宗上的决定', () => {
  beforeEach(() => {
    document.head.innerHTML = '<meta name="opendelo-session-token" content="test-session-token">'
    useLanguageStore.setState({ language: 'zh' })
    calls.length = 0
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    document.head.innerHTML = ''
  })

  const settlements = () => calls.filter((call) => call.method === 'POST').map((call) => call.path)

  it('五个操作各自打到自己的端点', async () => {
    renderFolio(() => json(request))

    fireEvent.click(await screen.findByRole('button', { name: new RegExp(zh.folioAllowFor('15m')) }))
    await waitFor(() => {
      expect(settlements()).toEqual(['/v1/approvals/ap-1/allow-task'])
    })

    fireEvent.click(screen.getByRole('button', { name: new RegExp(zh.folioAllowOnce) }))
    await waitFor(() => {
      expect(settlements()).toContain('/v1/approvals/ap-1/allow-once')
    })

    fireEvent.click(screen.getByRole('button', { name: new RegExp(zh.folioDeny) }))
    await waitFor(() => {
      expect(settlements()).toContain('/v1/approvals/ap-1/deny')
    })

    fireEvent.click(screen.getByRole('button', { name: zh.folioAllowProject }))
    await waitFor(() => {
      expect(settlements()).toContain('/v1/approvals/ap-1/allow-project')
    })

    // PRD §13.2 的第五种操作。它一度没有端点，按钮因此渲染不出来。
    fireEvent.click(screen.getByRole('button', { name: new RegExp(zh.folioAlwaysAsk) }))
    await waitFor(() => {
      expect(settlements()).toContain('/v1/approvals/ap-1/always-ask')
    })
  })

  it('「今后仍然问我」说清它记下的是什么', async () => {
    // 与相邻那个按钮方向相反：一个是「今后别再问」，一个是「今后仍然问」。
    // 不说这一句，两个按钮看起来像是同一类「记住我的选择」。
    renderFolio(() => json(request))

    expect(await screen.findByText(zh.folioAlwaysAskHint)).toBeInTheDocument()
  })

  it('后端没提供第五种操作时按钮不存在', async () => {
    // 高风险学不成任何记忆，available_actions 里因此没有它（REQ-TRUST-003）。
    const high = { ...request, decision: { ...request.decision, risk_level: 'high' } }
    renderFolio(() => json(high), () => json(approvals(['allow_once', 'allow_until_task_end', 'deny'])))

    await screen.findByText(zh.folioArrival)
    expect(screen.queryByRole('button', { name: new RegExp(zh.folioAlwaysAsk) })).not.toBeInTheDocument()
  })

  it('高风险页面上不存在「今后自动允许」按钮（REQ-APPROVAL-002 AC1）', async () => {
    const high = { ...request, decision: { ...request.decision, risk_level: 'high' } }
    renderFolio(() => json(high), () => json(approvals(['allow_once', 'allow_until_task_end', 'deny'])))

    await screen.findByText(zh.folioArrival)
    expect(screen.queryByRole('button', { name: zh.folioAllowProject })).not.toBeInTheDocument()
  })

  it('高风险未完成强认证时允许按钮不可用，拒绝仍然可用（REQ-APPROVAL-005 AC1）', async () => {
    const high = { ...request, decision: { ...request.decision, risk_level: 'high' } }
    renderFolio(() => json(high), () => json(approvals(['allow_once', 'allow_until_task_end', 'deny'])))

    expect(await screen.findByText(zh.folioHighRiskGate)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: new RegExp(zh.folioAllowFor('15m')) })).toBeDisabled()
    expect(screen.getByRole('button', { name: new RegExp(zh.folioAllowOnce) })).toBeDisabled()
    // 拒绝从不需要强认证：拿不到 Passkey 也要说得出「不许」。
    expect(screen.getByRole('button', { name: new RegExp(zh.folioDeny) })).toBeEnabled()
  })

  it('高风险按 A 也放行不了 —— 按键与按钮走同一个判断', async () => {
    const high = { ...request, decision: { ...request.decision, risk_level: 'high' } }
    renderFolio(() => json(high), () => json(approvals(['allow_once', 'allow_until_task_end', 'deny'])))
    await screen.findByText(zh.folioArrival)

    fireEvent.keyDown(window, { key: 'a' })
    fireEvent.keyDown(window, { key: 'A', shiftKey: true })
    // 拒绝是这一页上唯一走得通的按键，用它当同步点：等到它到达时，
    // 前面两个若真的生效也早该到了。
    fireEvent.keyDown(window, { key: 'd' })

    await waitFor(() => {
      expect(settlements()).toEqual(['/v1/approvals/ap-1/deny'])
    })
  })

  it.each([
    ['a' as const, false, '/v1/approvals/ap-1/allow-task'],
    ['A' as const, true, '/v1/approvals/ap-1/allow-once'],
    ['d' as const, false, '/v1/approvals/ap-1/deny'],
  ])('按 %s 打到它自己的端点', async (key, shiftKey, endpoint) => {
    // 三个按键各测一次而不是在同一次渲染里连按：`A` 与 `⇧A` 对应两个不同的
    // 端点，连按时 pending 会把第二个吞掉，那样谁打到哪里就分辨不出来了。
    renderFolio(() => json(request))
    await screen.findByText(zh.folioArrival)

    fireEvent.keyDown(window, { key, shiftKey })

    await waitFor(() => {
      expect(settlements()).toEqual([endpoint])
    })
  })

  it('后端没提供的操作按钮是禁用的，不是「点了才发现不行」', async () => {
    renderFolio(() => json(request), () => json(approvals(['deny'])))

    await screen.findByText(zh.folioArrival)
    expect(screen.getByRole('button', { name: new RegExp(zh.folioAllowFor('15m')) })).toBeDisabled()
    expect(screen.getByRole('button', { name: new RegExp(zh.folioDeny) })).toBeEnabled()
  })

  it('拿不到可用操作清单时一个放行按钮都不亮（Fail Closed）', async () => {
    renderFolio(() => json(request), () => json(approvals([])))

    await screen.findByText(zh.folioArrival)
    expect(screen.getByRole('button', { name: new RegExp(zh.folioAllowFor('15m')) })).toBeDisabled()
    expect(screen.getByRole('button', { name: new RegExp(zh.folioDeny) })).toBeDisabled()
    expect(screen.getByText(zh.folioNoActions)).toBeInTheDocument()
  })

  it('重复点击只产生一次决策（REQ-APPROVAL-006 AC2）', async () => {
    // 提交挂住不返回，模拟用户在等待期间又点了两下。
    renderFolio(() => json(request), () => json(approvals()))
    vi.stubGlobal('fetch', (path: string, init?: RequestInit) => {
      calls.push({ path, method: init?.method ?? 'GET', body: typeof init?.body === 'string' ? init.body : '' })
      if (init?.method === 'POST') {
        return new Promise<Response>(() => undefined)
      }
      if (path.startsWith('/v1/approvals')) {
        return json(approvals())
      }
      if (path.startsWith('/v1/capability-requests/')) {
        return json(request)
      }
      if (path === '/v1/agents') {
        return json(agents)
      }
      if (path === '/v1/identities') {
        return json(identities)
      }
      return json(status)
    })

    const deny = await screen.findByRole('button', { name: new RegExp(zh.folioDeny) })
    fireEvent.click(deny)
    await waitFor(() => {
      expect(settlements()).toHaveLength(1)
    })
    // 提交期间按钮自己也变灰：拦住第二次提交是一回事，让用户看见「已经在提交了」
    // 是另一回事，少了后者他只会觉得刚才那下没点上。
    expect(deny).toBeDisabled()

    fireEvent.click(deny)
    fireEvent.click(deny)

    expect(settlements()).toHaveLength(1)
  })

  it('连按两次也只产生一次决策 —— 按键绕得过变灰的按钮，绕不过提交中的判断', async () => {
    renderFolio(() => json(request), () => json(approvals()))
    await screen.findByText(zh.folioArrival)
    vi.stubGlobal('fetch', (path: string, init?: RequestInit) => {
      calls.push({ path, method: init?.method ?? 'GET', body: typeof init?.body === 'string' ? init.body : '' })
      return init?.method === 'POST' ? new Promise<Response>(() => undefined) : json(approvals())
    })

    fireEvent.keyDown(window, { key: 'd' })
    await waitFor(() => {
      expect(settlements()).toHaveLength(1)
    })
    fireEvent.keyDown(window, { key: 'd' })
    fireEvent.keyDown(window, { key: 'd' })
    await flush()

    expect(settlements()).toHaveLength(1)
  })

  it('已经有结论的那条不再提供决定', async () => {
    renderFolio(settled)

    await screen.findByText(zh.folioSettled)
    expect(screen.getByRole('button', { name: new RegExp(zh.folioDeny) })).toBeDisabled()
  })
})

describe('Access Folio 的形态', () => {
  it('1440 下两页不滚动 —— 十一项要一眼看全（REQ-APPROVAL-001 AC3）', () => {
    for (const rule of ['.sheet', '.arrival', '.consequence']) {
      const block = new RegExp(`\\${rule}[^{]*\\{([^}]*)\\}`).exec(css)?.[1] ?? ''

      expect(block).not.toBe('')
      expect(block).not.toContain('overflow')
    }
  })

  it('两页只用 transform 与 opacity 展开（REQ-UI-010 AC3）', () => {
    const pages = /@keyframes pageL \{([\s\S]*?)\n\}/.exec(css)?.[1] ?? ''

    expect(pages).toContain('transform: scaleX')
    expect(pages).toContain('opacity')
    expect(pages).not.toContain('left:')
    expect(pages).not.toContain('width:')
  })

  it('reduced-motion 下循环动画停下来', () => {
    // 拦截规则统一在 `styles/reset.css`，覆盖所有元素并带 `!important`
    // （由 `styles/motion.test.ts` 守着）。这一页要做的只有一件事：
    // 不用 `!important` 声明自己的动画 —— 那会盖过那条规则，书脊照旧呼吸。
    const shouted = [...css.matchAll(/animation[^;]*!important/g)]

    expect(shouted).toEqual([])
    expect(css).toContain('animation: breathe')
  })
})

describe('高风险的强认证（REQ-APPROVAL-005，用户决定 D-14 方案 C）', () => {
  const highRisk = () => json({ ...request, decision: { ...request.decision, risk_level: 'high' } })
  const highRiskApprovals = () => json(approvals(['allow_once', 'allow_until_task_end', 'deny']))
  const settlements = () => calls.filter((call) => call.method === 'POST' && call.path.startsWith('/v1/approvals'))

  beforeEach(() => {
    document.head.innerHTML = '<meta name="opendelo-session-token" content="test-session-token">'
    useLanguageStore.setState({ language: 'zh' })
    calls.length = 0
    unlockResponse = () => json({ unlocked: true })
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    document.head.innerHTML = ''
  })

  async function unlock(password: string) {
    fireEvent.change(await screen.findByLabelText(zh.folioUnlockLabel), { target: { value: password } })
    fireEvent.click(screen.getByRole('button', { name: zh.folioUnlockAction }))
  }

  it('解锁之后允许按钮可用，且这次放行提交得出去', async () => {
    renderFolio(highRisk, highRiskApprovals)
    await screen.findByText(zh.folioArrival)

    const allow = screen.getByRole('button', { name: new RegExp(zh.folioAllowFor('15m')) })
    expect(allow).toBeDisabled()

    await unlock(SENTINEL_TOKEN)
    await waitFor(() => {
      expect(allow).toBeEnabled()
    })
    expect(screen.getByText(zh.folioUnlockDone)).toBeInTheDocument()
    // 解开之后这一段整个消失：留着一个主密码输入框就是留着一处可以再输一次的地方。
    expect(screen.queryByLabelText(zh.folioUnlockLabel)).not.toBeInTheDocument()

    fireEvent.click(allow)
    await waitFor(() => {
      expect(settlements().map((call) => call.path)).toContain('/v1/approvals/ap-1/allow-task')
    })
  })

  it('解锁之后按 A 也放行得了 —— 按键与按钮共用同一个判断', async () => {
    renderFolio(highRisk, highRiskApprovals)
    await screen.findByText(zh.folioArrival)

    fireEvent.keyDown(window, { key: 'a' })
    await flush()
    expect(settlements()).toEqual([])

    await unlock(SENTINEL_TOKEN)
    await waitFor(() => {
      expect(screen.getByText(zh.folioUnlockDone)).toBeInTheDocument()
    })

    fireEvent.keyDown(window, { key: 'a' })
    await waitFor(() => {
      expect(settlements().map((call) => call.path)).toEqual(['/v1/approvals/ap-1/allow-task'])
    })
  })

  it('主密码送去 Gateway 校验，界面自己不判断对错', async () => {
    renderFolio(highRisk, highRiskApprovals)
    await screen.findByText(zh.folioArrival)

    await unlock(SENTINEL_TOKEN)
    await waitFor(() => {
      expect(calls.some((call) => call.path === '/v1/vault/unlock' && call.method === 'POST')).toBe(true)
    })
    const sent = calls.find((call) => call.path === '/v1/vault/unlock')
    expect(sent?.body).toBe(JSON.stringify({ master_password: SENTINEL_TOKEN }))
  })

  it('解锁失败时说明失败，允许按钮仍然不可用', async () => {
    unlockResponse = () =>
      json({ error: { code: 'unauthenticated', message: '解锁失败', operation_id: 'op-1' } }, 401)
    renderFolio(highRisk, highRiskApprovals)
    await screen.findByText(zh.folioArrival)

    await unlock(SENTINEL_TOKEN)

    expect(await screen.findByText(zh.folioUnlockFailed)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: new RegExp(zh.folioAllowFor('15m')) })).toBeDisabled()
    // 送出即忘：输入框里不留一份明文。查的是 value 而不是 innerHTML ——
    // React 设的是 DOM 属性，明文本来就不会出现在 innerHTML 里，
    // 只查那一处的断言会因为查错了地方而通过。
    expect(screen.getByLabelText(zh.folioUnlockLabel)).toHaveValue('')
  })

  it('三次失败之后的锁定说清楚，且输入框停下来', async () => {
    unlockResponse = () =>
      json(
        { error: { code: 'provider_locked_timeout', message: '锁定中', operation_id: 'op-1' } },
        504,
      )
    renderFolio(highRisk, highRiskApprovals)
    await screen.findByText(zh.folioArrival)

    await unlock(SENTINEL_TOKEN)

    expect(await screen.findByText(zh.folioUnlockLockedOut)).toBeInTheDocument()
    expect(screen.getByLabelText(zh.folioUnlockLabel)).toBeDisabled()
    expect(screen.getByRole('button', { name: zh.folioUnlockAction })).toBeDisabled()
  })

  it('主密码不留在页面上 —— 送出即忘', async () => {
    const { container } = renderFolio(highRisk, highRiskApprovals)
    await screen.findByText(zh.folioArrival)

    await unlock(SENTINEL_TOKEN)
    await waitFor(() => {
      expect(screen.getByText(zh.folioUnlockDone)).toBeInTheDocument()
    })

    expect(container.innerHTML).not.toContain(SENTINEL_TOKEN)
  })

  it('中风险的卷宗上没有解锁这一段', async () => {
    renderFolio(() => json(request))

    await screen.findByText(zh.folioArrival)
    expect(screen.queryByLabelText(zh.folioUnlockLabel)).not.toBeInTheDocument()
  })
})
