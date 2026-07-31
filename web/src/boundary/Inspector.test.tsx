import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import { QueryClientProvider } from '@tanstack/react-query'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import type { Passage } from '../data/passages'
import { createQueryClient } from '../data/queryClient'
import { copyFor } from '../i18n/copy'
import { useLanguageStore } from '../state/language'
import { SENTINEL_TOKEN } from '../test/sentinel'

import { Inspector } from './Inspector'
import { mayDecideHere } from './inspectorRules'

const zh = copyFor('zh')
const css = readFileSync(resolve(process.cwd(), 'src/boundary/Inspector.module.css'), 'utf8')

const passage = (overrides: Partial<Passage> = {}): Passage => ({
  id: 'rq-1',
  agentId: 'ag-1',
  service: 'github',
  operation: 'read',
  resource: 'src/',
  verdict: 'waiting',
  riskLevel: 'medium',
  reason: 'requires_confirmation',
  approvalId: 'ap-1',
  availableActions: ['allow_once', 'allow_until_task_end', 'deny'],
  ...overrides,
  at: '2026-07-29T00:00:00Z',
})

const status = {
  status: 'running',
  version: 'test',
  listen_address: '127.0.0.1',
  web_api_port: 8787,
  started_at: '2026-07-29T00:00:00Z',
}

const json = (body: unknown) =>
  Promise.resolve(
    new Response(JSON.stringify(body), {
      status: 200,
      headers: { 'Content-Type': 'application/json; charset=utf-8' },
    }),
  )

function renderInspector(selected: Passage | null, respond: (path: string, init?: RequestInit) => Promise<Response>) {
  vi.stubGlobal('fetch', (path: string, init?: RequestInit) => respond(path, init))
  return render(
    <QueryClientProvider client={createQueryClient()}>
      <MemoryRouter>
        <Inspector passage={selected} agentName="writer-agent" status={status} isDrawerOpen={false} />
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

const noEvents = () => json({ items: [] })

describe('Inspector', () => {
  beforeEach(() => {
    document.head.innerHTML = '<meta name="opendelo-session-token" content="test-session-token">'
    useLanguageStore.setState({ language: 'zh' })
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    document.head.innerHTML = ''
  })

  it('没有选中时说明选中之后会看到什么，而不是一片空白', () => {
    renderInspector(null, noEvents)

    expect(screen.getByText(zh.inspectorEmpty)).toBeInTheDocument()
  })

  it('Credential 一行恒为「留在缝内」（REQ-UI-003 AC2）', () => {
    renderInspector(passage(), noEvents)

    expect(screen.getByText(zh.inspectorCredential)).toBeInTheDocument()
    expect(screen.getByText(zh.inspectorCredentialValue)).toBeInTheDocument()
  })

  it('渲染出来的内容里没有任何凭据哨兵', async () => {
    // 八个面里的 Console DOM 那一面（REQ-NFR-002 AC1）。
    const { container } = renderInspector(passage(), (path) =>
      path.includes('audit-events')
        ? json({
            items: [
              {
                id: 'ev-1',
                type: 'request.decided',
                verdict: 'allow',
                outcome: 'succeeded',
                service: SENTINEL_TOKEN,
                agent_id: 'ag-1',
                created_at: new Date().toISOString(),
              },
            ],
          })
        : noEvents(),
    )

    await waitFor(() => {
      expect(screen.getByText(zh.inspectorCredentialValue)).toBeInTheDocument()
    })
    expect(container.textContent).not.toContain(SENTINEL_TOKEN)
  })

  it('风险等级有文字，颜色不是唯一的信息载体', () => {
    renderInspector(passage({ riskLevel: 'high' }), noEvents)

    expect(screen.getByText(zh.riskHigh)).toBeInTheDocument()
  })

  it('高风险不在这里放行，并说清该去哪里（REQ-DECIDE-003 AC3）', () => {
    renderInspector(passage({ riskLevel: 'high' }), noEvents)

    expect(screen.getByText(zh.inspectorHighRisk)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: new RegExp(zh.inspectorAllow) })).toBeDisabled()
    expect(screen.getByRole('button', { name: new RegExp(zh.inspectorAllowOnce) })).toBeDisabled()
  })

  it('已经有结论的那条不再提供决定', () => {
    renderInspector(passage({ verdict: 'allowed' }), noEvents)

    expect(screen.getByText(zh.inspectorDecided)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: new RegExp(zh.inspectorAllow) })).toBeDisabled()
  })

  it('后端没提供的操作按钮是禁用的，不是「点了才发现不行」', () => {
    renderInspector(passage({ availableActions: ['deny'] }), noEvents)

    expect(screen.getByRole('button', { name: new RegExp(zh.inspectorAllow) })).toBeDisabled()
    expect(screen.getByRole('button', { name: new RegExp(zh.inspectorDeny) })).toBeEnabled()
  })

  it('后端提供第五种操作时它才出现，并打到 always-ask', async () => {
    // PRD §13.2 的第五种操作。默认夹具里没有它，因此这条用例先证明它确实缺席，
    // 再证明清单里有它时它出现 —— 少了前一半，一个永远渲染的按钮也能通过。
    renderInspector(passage(), noEvents)
    expect(screen.queryByRole('button', { name: new RegExp(zh.inspectorAlwaysAsk) })).not.toBeInTheDocument()

    const calls: string[] = []
    renderInspector(
      passage({ availableActions: ['allow_once', 'allow_until_task_end', 'always_ask', 'deny'] }),
      (path, init) => {
        if (init?.method === 'POST') {
          calls.push(path)
          return json({})
        }
        return noEvents()
      },
    )

    fireEvent.click(screen.getByRole('button', { name: new RegExp(zh.inspectorAlwaysAsk) }))
    await waitFor(() => {
      expect(calls).toContain('/v1/approvals/ap-1/always-ask')
    })
  })

  it('允许打到 allow-task，仅这一次打到 allow-once，拒绝打到 deny', async () => {
    const calls: string[] = []
    renderInspector(passage(), (path, init) => {
      if (init?.method === 'POST') {
        calls.push(path)
        return json({})
      }
      return noEvents()
    })

    fireEvent.click(screen.getByRole('button', { name: new RegExp(zh.inspectorAllow) }))
    await waitFor(() => {
      expect(calls).toEqual(['/v1/approvals/ap-1/allow-task'])
    })
  })

  it('打开 Folio 是一条链接，后退键因此等价于折回（REQ-APPROVAL-004）', () => {
    renderInspector(passage(), noEvents)

    expect(screen.getByRole('link', { name: new RegExp(zh.inspectorOpenFolio) })).toHaveAttribute(
      'href',
      '/gate/folio/rq-1',
    )
  })

  it('近 7 天的三个数都在，且带单位文字', async () => {
    renderInspector(passage(), (path) =>
      path.includes('audit-events')
        ? json({
            items: [
              {
                id: 'ev-1',
                type: 'request.decided',
                verdict: 'allow',
                outcome: 'succeeded',
                service: 'github',
                agent_id: 'ag-1',
                created_at: new Date().toISOString(),
              },
            ],
          })
        : noEvents(),
    )

    expect(await screen.findByText(zh.inspectorPassed)).toBeInTheDocument()
    expect(screen.getByText(zh.inspectorConfirmed)).toBeInTheDocument()
    // 「拒绝」在这一页出现两次：统计里的一行与拒绝按钮。这里要的是统计里那一行。
    const stats = screen.getByText(zh.inspectorStatsTitle).parentElement
    expect(stats).not.toBeNull()
    expect(within(stats ?? document.body).getByText(zh.inspectorRefused)).toBeInTheDocument()
  })
})

describe('Inspector 的形态', () => {
  it('1280 收窄到 232 并去掉统计块（REQ-UI-004 AC2）', () => {
    const narrow = /@media \(max-width: 1439px\)\s*\{([\s\S]*?)\n\}/.exec(css)?.[1] ?? ''

    expect(narrow).toContain('flex: 0 0 232px')
    expect(narrow).toContain('.stats')
    expect(narrow).toContain('display: none')
  })

  it('1440 下统计块是显示的 —— 隐藏只写在收窄断点里', () => {
    // 断点之外的那一份声明。断点里的隐藏不能被当成基线。
    const outsideMedia = css.replace(/@media[\s\S]*?\n\}\n/g, '')
    const base = /\.stats\s*\{([^}]*)\}/.exec(outsideMedia)?.[1] ?? ''

    expect(base).not.toBe('')
    expect(base).not.toContain('display: none')
  })

  it('抽屉用 transform 移进移出，舞台宽度不变，缝因此不动（REQ-UI-004 ②）', () => {
    const drawer = /@media \(max-width: 1279px\)\s*\{([\s\S]*?)\n\}/.exec(css)?.[1] ?? ''

    expect(drawer).toContain('transform: translateX(100%)')
    expect(drawer).toContain('position: absolute')
    // 改宽度会推挤舞台。抽屉是盖上去的，不是挤进去的。
    expect(drawer).not.toContain('flex: 0 0')
  })
})

describe('在这里能不能决定', () => {
  it('等待中、非高风险、有 approval 主键，三者齐备才能', () => {
    expect(mayDecideHere(passage())).toBe(true)
    expect(mayDecideHere(passage({ riskLevel: 'high' }))).toBe(false)
    expect(mayDecideHere(passage({ verdict: 'allowed' }))).toBe(false)
    expect(mayDecideHere(passage({ approvalId: '' }))).toBe(false)
  })
})
