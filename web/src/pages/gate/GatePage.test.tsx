import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import { QueryClientProvider } from '@tanstack/react-query'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { createQueryClient } from '../../data/queryClient'
import { copyFor } from '../../i18n/copy'
import { useLanguageStore } from '../../state/language'
import { useSelectionStore } from '../../state/selection'

import { GatePage } from './GatePage'

/*
 * Gate 的三态与缝的空间常量（REQ-UI-002 AC3）。
 *
 * 走真实链路：从入口文档读令牌，经 requestGateway 发请求，由 Zod 解析。
 */

const zh = copyFor('zh')
const gateCss = readFileSync(resolve(process.cwd(), 'src/pages/gate/GatePage.module.css'), 'utf8')

const approval = {
  id: 'ap-1',
  status: 'pending',
  created_at: '2026-07-29T00:00:00Z',
  request: {
    id: 'rq-1',
    agent_id: 'ag-1',
    service: 'github',
    operation: 'read',
    resource: { path: 'src/' },
    status: 'awaiting_approval',
    created_at: '2026-07-29T00:00:00Z',
    decision: null,
  },
  decision: null,
}

const allowed = {
  ...approval,
  id: 'ap-2',
  status: 'approved',
  request: { ...approval.request, id: 'rq-2' },
  decision: { verdict: 'allow', risk_level: 'low', reason_code: 'trust_memory_match' },
}

const json = (body: unknown) =>
  Promise.resolve(
    new Response(JSON.stringify(body), {
      status: 200,
      headers: { 'Content-Type': 'application/json; charset=utf-8' },
    }),
  )

function renderGate(respond: (path: string) => Promise<Response>) {
  vi.stubGlobal('fetch', (path: string) => respond(path))
  return render(
    <QueryClientProvider client={createQueryClient()}>
      <MemoryRouter>
        <GatePage />
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

/** 缝在不在。三态都必须答「在」。 */
function seamPresent(container: HTMLElement): boolean {
  return container.innerHTML.includes('seam')
}

describe('Gate 的三态', () => {
  beforeEach(() => {
    document.head.innerHTML = '<meta name="opendelo-session-token" content="test-session-token">'
    useLanguageStore.setState({ language: 'zh' })
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    document.head.innerHTML = ''
  })

  it('加载：显示骨架，缝仍在，不用整页 spinner', () => {
    const { container } = renderGate(() => new Promise<Response>(() => undefined))

    expect(screen.getByLabelText(zh.gateLoading)).toBeInTheDocument()
    expect(seamPresent(container)).toBe(true)
  })

  it('空：说明缝前无人等待，缝仍在（REQ-APPROVAL-006 AC1）', async () => {
    const { container } = renderGate(() => json({ items: [] }))

    expect(await screen.findByText(zh.gateEmptyTitle)).toBeInTheDocument()
    // 不是「暂无数据」式的通用文案。
    expect(screen.getByText(zh.gateEmptyBlurb)).toBeInTheDocument()
    expect(seamPresent(container)).toBe(true)
  })

  it('错误：说明发生了什么并说清本地数据仍然安全，缝仍在', async () => {
    const { container } = renderGate(() => Promise.reject(new Error('connection refused')))

    expect(await screen.findByText(zh.gateErrorTitle)).toBeInTheDocument()
    expect(screen.getByText(zh.gateErrorBlurb)).toBeInTheDocument()
    expect(seamPresent(container)).toBe(true)
  })

  it('三态下缝的定位声明完全一致，位置不因为状态而改变', () => {
    // 三态只换 .stream 里的内容，而 .stream 是绝对定位的一层 ——
    // 它装多少东西都推不动缝（REQ-UI-002 AC3、REQ-APPROVAL-006 AC3）。
    const stream = /\.stream\s*\{([^}]*)\}/.exec(gateCss)?.[1] ?? ''

    expect(stream).toContain('position: absolute')
    // 三态的容器也不带任何会挪动缝的声明。
    for (const selector of ['.notice', '.list', '.skeletonRow']) {
      const block = new RegExp(`\\${selector}\\s*\\{([^}]*)\\}`).exec(gateCss)?.[1] ?? ''
      expect(block, selector).not.toContain('position:')
    }
  })
})

describe('Gate 的内容', () => {
  beforeEach(() => {
    document.head.innerHTML = '<meta name="opendelo-session-token" content="test-session-token">'
    useLanguageStore.setState({ language: 'zh' })
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    document.head.innerHTML = ''
  })

  const respond = (path: string) => {
    if (path.includes('approvals')) {
      return json({ items: [approval, allowed] })
    }
    if (path.includes('agents')) {
      return json({ items: [{ id: 'ag-1', name: 'writer-agent', type: 'claude-code', device_id: 'dv-1', workspace_id: 'ws-1', trust_level: 'known', status: 'active', last_seen_at: '2026-07-29T09:00:00Z' }] })
    }
    return json({
      status: 'running',
      version: 'test',
      listen_address: '127.0.0.1',
      web_api_port: 8787,
      started_at: '2026-07-29T00:00:00Z',
    })
  }

  it('每条请求显示 Agent 名字与目的地', async () => {
    renderGate(respond)

    expect(await screen.findAllByText('writer-agent')).toHaveLength(2)
  })

  it('自动允许的那条显示理由（REQ-DECIDE-001 AC3）', async () => {
    renderGate(respond)

    expect(await screen.findByText(new RegExp(zh.reasonTrustMemoryMatch))).toBeInTheDocument()
  })

  it('页面里没有统计卡片式 Dashboard（REQ-UI-003 AC1）', async () => {
    const { container } = renderGate(respond)
    await screen.findAllByText('writer-agent')

    // 统计卡片的特征是一个大号数字加一句标签。缝前只有请求，没有指标。
    expect(container.querySelectorAll('[class*="stat"]')).toHaveLength(0)
    expect(container.querySelectorAll('[class*="metric"]')).toHaveLength(0)
  })

  it('列表有可读的名字，键盘与屏幕阅读器认得出它是什么', async () => {
    renderGate(respond)

    expect(await screen.findByLabelText(zh.passageListLabel)).toBeInTheDocument()
  })
})

describe('键盘决策（REQ-APPROVAL-003）', () => {
  beforeEach(() => {
    document.head.innerHTML = '<meta name="opendelo-session-token" content="test-session-token">'
    useLanguageStore.setState({ language: 'zh' })
    useSelectionStore.setState({ selectedPassageId: '' })
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    document.head.innerHTML = ''
  })

  const waiting = {
    ...approval,
    available_actions: ['allow_once', 'allow_until_task_end', 'deny'],
    decision: { verdict: 'require_approval', risk_level: 'medium', reason_code: 'requires_confirmation' },
  }

  function stubbed(item: unknown) {
    const posted: string[] = []
    const respond = (path: string, init?: RequestInit) => {
      if (init?.method === 'POST') {
        posted.push(path)
        return json({})
      }
      if (path.includes('approvals')) {
        return json({ items: [item] })
      }
      if (path.includes('agents')) {
        return json({ items: [{ id: 'ag-1', name: 'writer-agent', type: 'claude-code', device_id: 'dv-1', workspace_id: 'ws-1', trust_level: 'known', status: 'active', last_seen_at: '2026-07-29T09:00:00Z' }] })
      }
      return json({ items: [] })
    }
    vi.stubGlobal('fetch', (path: string, init?: RequestInit) => respond(path, init))
    return posted
  }

  async function selectFirst(item: unknown) {
    const posted = stubbed(item)
    render(
      <QueryClientProvider client={createQueryClient()}>
        <MemoryRouter>
          <GatePage />
        </MemoryRouter>
      </QueryClientProvider>,
    )
    fireEvent.click(await screen.findByRole('button', { name: /writer-agent/ }))
    return posted
  }

  it('A 打到 allow-task，全程不用鼠标之外的东西（AC1）', async () => {
    const posted = await selectFirst(waiting)

    fireEvent.keyDown(window, { key: 'a' })

    await waitFor(() => {
      expect(posted).toEqual(['/v1/approvals/ap-1/allow-task'])
    })
  })

  it('⇧A 打到 allow-once', async () => {
    const posted = await selectFirst(waiting)

    fireEvent.keyDown(window, { key: 'A', shiftKey: true })

    await waitFor(() => {
      expect(posted).toEqual(['/v1/approvals/ap-1/allow-once'])
    })
  })

  it('D 打到 deny', async () => {
    const posted = await selectFirst(waiting)

    fireEvent.keyDown(window, { key: 'd' })

    await waitFor(() => {
      expect(posted).toEqual(['/v1/approvals/ap-1/deny'])
    })
  })

  it('高风险的那条按 A 不放行（AC：高风险单键不直接放行）', async () => {
    const highRisk = {
      ...waiting,
      decision: { verdict: 'require_approval', risk_level: 'high', reason_code: 'high_risk' },
      available_actions: ['allow_once', 'deny'],
    }
    const posted = await selectFirst(highRisk)

    fireEvent.keyDown(window, { key: 'a' })
    fireEvent.keyDown(window, { key: 'A', shiftKey: true })

    await new Promise((resolve) => setTimeout(resolve, 20))
    expect(posted).toEqual([])
  })

  it('后端没提供的操作，按键也发不出去', async () => {
    const denyOnly = { ...waiting, available_actions: ['deny'] }
    const posted = await selectFirst(denyOnly)

    fireEvent.keyDown(window, { key: 'a' })

    await new Promise((resolve) => setTimeout(resolve, 20))
    expect(posted).toEqual([])
  })

  it('再次点击同一条收回抽屉，选中留在原处（REQ-UI-004 ②）', async () => {
    // 1024 上 Inspector 是抽屉，「Esc / 再次点击收回」是同一条降级规则的两半。
    await selectFirst(waiting)
    const card = screen.getByRole('button', { name: /writer-agent/ })
    const inspector = screen.getByLabelText(zh.inspectorLabel)
    expect(inspector.className).toContain('drawerOpen')

    fireEvent.click(card)

    expect(inspector.className).not.toContain('drawerOpen')
    // 收回的是抽屉不是选中：宽屏上 Inspector 一直在，收回不该把它清空。
    expect(screen.queryByText(zh.inspectorEmpty)).not.toBeInTheDocument()

    fireEvent.click(card)

    expect(inspector.className).toContain('drawerOpen')
  })

  it('Esc 折回：选中被清掉，Inspector 回到空态', async () => {
    await selectFirst(waiting)
    expect(screen.queryByText(zh.inspectorEmpty)).not.toBeInTheDocument()

    fireEvent.keyDown(window, { key: 'Escape' })

    expect(await screen.findByText(zh.inspectorEmpty)).toBeInTheDocument()
  })

  it('没有选中时按 A 什么都不发生', async () => {
    const posted = stubbed(waiting)
    render(
      <QueryClientProvider client={createQueryClient()}>
        <MemoryRouter>
          <GatePage />
        </MemoryRouter>
      </QueryClientProvider>,
    )
    await screen.findByRole('button', { name: /writer-agent/ })

    fireEvent.keyDown(window, { key: 'a' })

    await new Promise((resolve) => setTimeout(resolve, 20))
    expect(posted).toEqual([])
  })
})
