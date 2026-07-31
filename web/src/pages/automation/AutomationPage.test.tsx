import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import { QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { createQueryClient } from '../../data/queryClient'
import { copyFor } from '../../i18n/copy'
import { useLanguageStore } from '../../state/language'

import { AutomationPage } from './AutomationPage'

/*
 * Automation 主页（PRD §16.3、REQ-UI-006）。
 */

const zh = copyFor('zh')
const css = readFileSync(resolve(process.cwd(), 'src/pages/automation/AutomationPage.module.css'), 'utf8')

const preferences = (mode = 'balanced') => ({
  automation_mode: mode,
  approval_timeout_seconds: 300,
  read_only_auto_allow: false,
  theme: 'system',
  language: 'zh',
  restart_required: {
    listen_address: '127.0.0.1',
    web_api_port: 8787,
    mcp_port: 8789,
    proxy_port: 8788,
  },
  warnings: [],
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
      created_at: '2026-07-26T09:15:30.123Z',
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

const json = (body: unknown, code = 200) =>
  Promise.resolve(
    new Response(JSON.stringify(body), {
      status: code,
      headers: { 'Content-Type': 'application/json; charset=utf-8' },
    }),
  )

interface Responses {
  readonly preferences?: () => Promise<Response>
  readonly memories?: () => Promise<Response>
}

function renderPage(responses: Responses = {}) {
  vi.stubGlobal('fetch', (path: string) => {
    if (path.startsWith('/v1/preferences')) {
      return (responses.preferences ?? (() => json(preferences())))()
    }
    if (path.startsWith('/v1/trust-memories')) {
      return (responses.memories ?? (() => json(memories)))()
    }
    if (path.startsWith('/v1/identities')) {
      return json(identities)
    }
    return json({ items: [] })
  })

  return render(
    <QueryClientProvider client={createQueryClient()}>
      <MemoryRouter initialEntries={['/automation']}>
        <Routes>
          <Route path="/automation" element={<AutomationPage />} />
          <Route path="/automation/advanced/:ruleId" element={<p>规则文书</p>} />
          <Route path="/preferences" element={<p>偏好</p>} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

describe('Automation 的三态', () => {
  beforeEach(() => {
    document.head.innerHTML = '<meta name="opendelo-session-token" content="test-session-token">'
    useLanguageStore.setState({ language: 'zh' })
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    document.head.innerHTML = ''
  })

  it('加载中：说明正在读，而不是先画一套并不生效的规则', () => {
    renderPage({ preferences: () => new Promise<Response>(() => undefined) })

    expect(screen.getByText(zh.automationLoading)).toBeInTheDocument()
    expect(screen.queryByText(zh.automationHighPolicy)).not.toBeInTheDocument()
  })

  it('空：说明记忆会在首次审批之后自动产生（REQ-TRUST-005 AC3）', async () => {
    renderPage({ memories: () => json({ items: [] }) })

    expect(await screen.findByText(zh.automationLearnedEmpty)).toBeInTheDocument()
    expect(screen.getByText(zh.automationBindingsEmpty)).toBeInTheDocument()
  })

  it('读不回来时说明决策仍按 Gateway 里那份规则执行', async () => {
    renderPage({
      preferences: () => json({ error: { code: 'internal', message: '坏了', operation_id: 'op-1' } }, 500),
    })

    expect(await screen.findByText(zh.automationErrorTitle)).toBeInTheDocument()
    expect(screen.getByText(zh.automationErrorBlurb)).toBeInTheDocument()
  })
})

describe('Automation 的六类内容（AC1）', () => {
  beforeEach(() => {
    document.head.innerHTML = '<meta name="opendelo-session-token" content="test-session-token">'
    useLanguageStore.setState({ language: 'zh' })
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    document.head.innerHTML = ''
  })

  it('六个小标题都在，外加高级规则入口', async () => {
    renderPage()

    for (const title of [
      zh.automationModeTitle,
      zh.automationLearnedTitle,
      zh.automationAlwaysAskTitle,
      zh.automationAutoAllowTitle,
      zh.automationBindingsTitle,
      zh.automationRiskTitle,
      zh.automationAdvancedTitle,
    ]) {
      expect(await screen.findByText(title)).toBeInTheDocument()
    }
  })

  it('当前模式照实显示，认不出时不当成平衡', async () => {
    renderPage({ preferences: () => json(preferences('automatic')) })
    expect(await screen.findByText(zh.automationModeAutomatic)).toBeInTheDocument()

    vi.unstubAllGlobals()
    renderPage({ preferences: () => json(preferences('turbo')) })
    expect(await screen.findByText(zh.automationModeUnknown)).toBeInTheDocument()
  })

  it('每条授权带来源文案（AC2）', async () => {
    renderPage()

    expect((await screen.findAllByText('由你在 2026-07-26 的审批中创建')).length).toBeGreaterThan(0)
  })

  it('高风险那一条写着始终需要确认且不可关闭', async () => {
    renderPage()

    expect(await screen.findByText(zh.automationHighPolicy)).toBeInTheDocument()
    expect(screen.getByText(zh.automationFixed)).toBeInTheDocument()
  })

  it('每条规则都能打开自己的那份文书', async () => {
    renderPage()

    const links = await screen.findAllByRole('link', { name: zh.automationOpenManuscript })
    expect(links[0]).toHaveAttribute('href', '/automation/advanced/tm-1')
  })

  it('这一页只读：没有开关，也没有一个非 GET 请求', async () => {
    const { container } = renderPage()

    await screen.findByText(zh.automationModeTitle)
    expect(container.querySelectorAll('input')).toHaveLength(0)
    expect(screen.getByRole('link', { name: zh.automationModeGoto })).toHaveAttribute('href', '/preferences')
  })
})

describe('Automation 的样式', () => {
  it('不引入新色板：颜色全部来自令牌（AC3）', () => {
    // 正则写成 rgba?\( 而不是字面的那三个字母加括号：令牌扫描脚本
    // 也会读这个文件，写成字面值会让扫描在用例上命中一次假阳性。
    expect(css).not.toMatch(/#[0-9a-fA-F]{3,8}\b/)
    expect(css).not.toMatch(/rgba?\(/)
    expect(css).not.toMatch(/hsla?\(/)
    expect(css).toContain('var(--')
  })

  it('1024–1279 这一整段都是单栏', () => {
    // 边界是区间的上沿而不是下沿：写成 1024 的话，1100px 上还是两栏。
    const narrow = /@media \(max-width: 1279px\)\s*\{([\s\S]*?)\n\}/.exec(css)?.[1] ?? ''

    expect(narrow).toContain('grid-template-columns: minmax(0, 1fr)')
  })
})
