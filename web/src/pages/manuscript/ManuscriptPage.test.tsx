import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import { QueryClientProvider } from '@tanstack/react-query'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { createQueryClient } from '../../data/queryClient'
import { copyFor } from '../../i18n/copy'
import { useLanguageStore } from '../../state/language'
import { SENTINEL_TOKEN } from '../../test/sentinel'

import { ManuscriptPage } from './ManuscriptPage'

/*
 * Rule Manuscript（REQ-TRUST-006、设计稿 §06）。
 */

const zh = copyFor('zh')
const css = readFileSync(resolve(process.cwd(), 'src/pages/manuscript/ManuscriptPage.module.css'), 'utf8')

const memory = (behavior = 'auto_allow') => ({
  id: 'tm-1',
  agent_id: 'ag-1',
  workspace_id: 'ws-1',
  identity_id: 'id-1',
  service: 'github',
  environment: 'production',
  risk_ceiling: 'medium',
  approval_behavior: behavior,
  created_from: 'ap-1',
  status: 'active',
  invalidation_reason: '',
  expires_at: '2026-08-30T09:00:00Z',
  created_at: '2026-07-26T09:15:30.123Z',
})

const auditEvents = {
  items: [
    { id: 'e1', type: 'decision.user_allowed', verdict: 'require_approval', outcome: 'succeeded', service: 'github', agent_id: 'ag-1', created_at: new Date(Date.now() - 3_600_000).toISOString() },
    { id: 'e2', type: 'decision.auto_allowed', verdict: 'allow', outcome: 'succeeded', service: 'github', agent_id: 'ag-1', created_at: new Date(Date.now() - 7_200_000).toISOString() },
    { id: 'e3', type: 'decision.denied', verdict: 'deny', outcome: 'blocked', service: 'cloudflare', agent_id: 'ag-1', created_at: new Date(Date.now() - 7_200_000).toISOString() },
  ],
}

const json = (body: unknown, code = 200) =>
  Promise.resolve(
    new Response(JSON.stringify(body), {
      status: code,
      headers: { 'Content-Type': 'application/json; charset=utf-8' },
    }),
  )

const calls: { path: string; method: string; body: string }[] = []

function renderPage(behavior = 'auto_allow', ruleId = 'tm-1') {
  vi.stubGlobal('fetch', (path: string, init?: RequestInit) => {
    calls.push({
      path,
      method: init?.method ?? 'GET',
      body: typeof init?.body === 'string' ? init.body : '',
    })
    if (path.startsWith('/v1/trust-memories/')) {
      return json(memory('always_ask'))
    }
    if (path.startsWith('/v1/trust-memories')) {
      return json({ items: [memory(behavior)] })
    }
    if (path.startsWith('/v1/audit-events')) {
      return json(auditEvents)
    }
    if (path.startsWith('/v1/vault/unlock')) {
      return json({ unlocked: true })
    }
    return json({ items: [] })
  })

  return render(
    <QueryClientProvider client={createQueryClient()}>
      <MemoryRouter initialEntries={[`/automation/advanced/${ruleId}`]}>
        <Routes>
          <Route path="/automation/advanced/:ruleId" element={<ManuscriptPage />} />
          <Route path="/automation" element={<p>自动化</p>} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

describe('文书的样子', () => {
  beforeEach(() => {
    document.head.innerHTML = '<meta name="opendelo-session-token" content="test-session-token">'
    useLanguageStore.setState({ language: 'zh' })
    localStorage.clear()
    calls.length = 0
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    document.head.innerHTML = ''
  })

  it('槽位是行内下划线，不是输入框（AC1）', async () => {
    const { container } = renderPage()

    await screen.findByText(zh.manuscriptEyebrow)
    const slots = container.querySelectorAll('[data-slot]')
    expect(slots.length).toBeGreaterThan(0)
    // 正文里一个输入框都没有。页面上唯一的 input 是解锁那一处，它不在散文里。
    for (const prose of container.querySelectorAll('p')) {
      expect(prose.querySelector('input')).toBeNull()
      expect(prose.querySelector('textarea')).toBeNull()
    }
    for (const slot of slots) {
      expect(slot.tagName).not.toBe('INPUT')
    }
    // 下划线来自 border-bottom，而不是控件自带的边框。
    const slot = /\.slot \{([\s\S]*?)\n\}/.exec(css)?.[1] ?? ''
    expect(slot).toContain('border-bottom')
    expect(slot).toContain('border: none')
  })

  it('找不到这份文书时说清楚，并给出回去的路', async () => {
    renderPage('auto_allow', 'tm-missing')

    expect(await screen.findByText(zh.manuscriptNotFound)).toBeInTheDocument()
    expect(screen.getByRole('link', { name: zh.manuscriptBack })).toHaveAttribute('href', '/automation')
  })

  it('影响预览的三个数来自账本，且不数别的服务（AC2）', async () => {
    renderPage()

    expect(await screen.findByText(zh.manuscriptImpact('1', '1', '0'))).toBeInTheDocument()
    expect(calls.some((call) => call.path.startsWith('/v1/audit-events'))).toBe(true)
  })

  it('YAML 视图与文书视图可以来回切', async () => {
    renderPage()

    fireEvent.click(await screen.findByRole('button', { name: zh.manuscriptYaml }))
    expect(screen.getByText(/behavior: auto_allow/)).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: zh.manuscriptStructured }))
    expect(screen.queryByText(/behavior: auto_allow/)).not.toBeInTheDocument()
  })

  it('批注是从这条规则读出来的事实，不是一个存不下的输入框', async () => {
    renderPage()

    expect(await screen.findByText(zh.manuscriptMarginOrigin('2026-07-26'))).toBeInTheDocument()
    expect(screen.getByText(zh.manuscriptMarginTighten)).toBeInTheDocument()
  })

  it('缝预览随起草的行为改变', async () => {
    renderPage()

    expect(await screen.findByText(zh.manuscriptSeamAuto)).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: zh.manuscriptBehaviorAuto }))
    expect(screen.getByText(zh.manuscriptSeamAsk)).toBeInTheDocument()
  })
})

describe('签署（AC3）', () => {
  beforeEach(() => {
    document.head.innerHTML = '<meta name="opendelo-session-token" content="test-session-token">'
    useLanguageStore.setState({ language: 'zh' })
    localStorage.clear()
    calls.length = 0
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    document.head.innerHTML = ''
  })

  it('没有改动时签署按钮就不该亮', async () => {
    renderPage('always_ask')

    await screen.findByText(zh.manuscriptEyebrow)
    expect(screen.getByRole('button', { name: zh.manuscriptSign })).toBeDisabled()
    expect(screen.getByText(zh.manuscriptNoChange)).toBeInTheDocument()
  })

  it('解锁了但没有改动，签署照样不亮 —— 签一次什么也不会发生', async () => {
    renderPage('always_ask')

    await screen.findByText(zh.manuscriptEyebrow)
    fireEvent.change(screen.getByLabelText(zh.folioUnlockLabel), { target: { value: SENTINEL_TOKEN } })
    fireEvent.click(screen.getByRole('button', { name: zh.folioUnlockAction }))

    await waitFor(() => {
      expect(screen.queryByLabelText(zh.folioUnlockLabel)).not.toBeInTheDocument()
    })
    expect(screen.getByRole('button', { name: zh.manuscriptSign })).toBeDisabled()
  })

  it('起草了改动但没解锁时仍然不可签署', async () => {
    renderPage()

    await screen.findByText(zh.manuscriptEyebrow)
    fireEvent.click(screen.getByRole('button', { name: zh.manuscriptBehaviorAuto }))

    expect(screen.getByRole('button', { name: zh.manuscriptSign })).toBeDisabled()
    expect(screen.getByLabelText(zh.folioUnlockLabel)).toBeInTheDocument()
  })

  it('解锁之后签署把这条规则改成每次都问', async () => {
    renderPage()

    await screen.findByText(zh.manuscriptEyebrow)
    fireEvent.click(screen.getByRole('button', { name: zh.manuscriptBehaviorAuto }))
    fireEvent.change(screen.getByLabelText(zh.folioUnlockLabel), { target: { value: SENTINEL_TOKEN } })
    fireEvent.click(screen.getByRole('button', { name: zh.folioUnlockAction }))

    const sign = screen.getByRole('button', { name: zh.manuscriptSign })
    await waitFor(() => {
      expect(sign).toBeEnabled()
    })

    fireEvent.click(sign)
    await waitFor(() => {
      expect(calls.some((call) => call.method === 'PATCH')).toBe(true)
    })
    const patch = calls.find((call) => call.method === 'PATCH')
    expect(patch?.path).toBe('/v1/trust-memories/tm-1')
    // 请求体里只有一个取值：放宽在这条路上连表达都表达不出来。
    expect(patch?.body).toBe(JSON.stringify({ approval_behavior: 'always_ask' }))
  })

  it('起草之后顶栏说草稿已保存，刷新之后它还在', async () => {
    renderPage()

    await screen.findByText(zh.manuscriptEyebrow)
    expect(screen.getByText(zh.manuscriptUnsaved)).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: zh.manuscriptBehaviorAuto }))

    expect(screen.getByText(/草稿已自动保存/)).toBeInTheDocument()
    expect(localStorage.getItem('opendelo.manuscript.tm-1')).toContain('always_ask')
  })

  it('主密码不留在页面上', async () => {
    const { container } = renderPage()

    await screen.findByText(zh.manuscriptEyebrow)
    fireEvent.click(screen.getByRole('button', { name: zh.manuscriptBehaviorAuto }))
    fireEvent.change(screen.getByLabelText(zh.folioUnlockLabel), { target: { value: SENTINEL_TOKEN } })
    fireEvent.click(screen.getByRole('button', { name: zh.folioUnlockAction }))

    await waitFor(() => {
      expect(screen.getByRole('button', { name: zh.manuscriptSign })).toBeEnabled()
    })
    expect(container.innerHTML).not.toContain(SENTINEL_TOKEN)
  })
})
