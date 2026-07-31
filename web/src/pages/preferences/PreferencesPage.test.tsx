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

import { PreferencesPage } from './PreferencesPage'

/*
 * Preferences（REQ-UI-007、REQ-PREF-001）。
 */

const zh = copyFor('zh')
const css = readFileSync(resolve(process.cwd(), 'src/pages/preferences/PreferencesPage.module.css'), 'utf8')

const preferences = (mode = 'balanced', warnings: string[] = []) => ({
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
  warnings,
})

const memory = (id: string) => ({
  id,
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
})

const json = (body: unknown, code = 200) =>
  Promise.resolve(
    new Response(JSON.stringify(body), {
      status: code,
      headers: { 'Content-Type': 'application/json; charset=utf-8' },
    }),
  )

const calls: { path: string; method: string; body: string }[] = []

interface Responses {
  readonly preferences?: () => Promise<Response>
  readonly memories?: () => Promise<Response>
  readonly vault?: () => Promise<Response>
  readonly del?: () => Promise<Response>
}

function renderPage(responses: Responses = {}) {
  vi.stubGlobal('fetch', (path: string, init?: RequestInit) => {
    calls.push({
      path,
      method: init?.method ?? 'GET',
      body: typeof init?.body === 'string' ? init.body : '',
    })
    if (path === '/v1/vault') {
      return (responses.vault ?? (() => json({ unlocked: true }, 201)))()
    }
    if (path.startsWith('/v1/preferences')) {
      return (responses.preferences ?? (() => json(preferences())))()
    }
    if (path.startsWith('/v1/trust-memories/')) {
      return (responses.del ?? (() => Promise.resolve(new Response(null, { status: 204 }))))()
    }
    if (path.startsWith('/v1/trust-memories')) {
      return (responses.memories ?? (() => json({ items: [] })))()
    }
    return json({ items: [] })
  })

  return render(
    <QueryClientProvider client={createQueryClient()}>
      <MemoryRouter initialEntries={['/preferences']}>
        <Routes>
          <Route path="/preferences" element={<PreferencesPage />} />
          <Route path="/gate" element={<p>缝前</p>} />
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
  localStorage.clear()
  calls.length = 0
})

afterEach(() => {
  vi.unstubAllGlobals()
  document.head.innerHTML = ''
})

describe('六个模块（AC1）', () => {
  it('六个小标题全部在场', async () => {
    renderPage()

    for (const title of [
      zh.prefsGeneral,
      zh.prefsGateway,
      zh.prefsCredentials,
      zh.prefsAutomation,
      zh.prefsSecurity,
      zh.prefsNotifications,
    ]) {
      expect(await screen.findByText(title)).toBeInTheDocument()
    }
  })

  it('本期未实现的项禁用且有说明，没有一个「点得动但无效」的控件（AC2）', async () => {
    renderPage()

    await screen.findByText(zh.prefsGateway)
    for (const later of screen.getAllByRole('button', { name: zh.prefsLater })) {
      expect(later).toBeDisabled()
    }
    expect(screen.getAllByText(zh.prefsNotSupported).length).toBeGreaterThan(0)
  })

  it('端口与监听地址标明要重启（REQ-PREF-001 AC2）', async () => {
    renderPage()

    expect(await screen.findByText('8787')).toBeInTheDocument()
    expect(screen.getByText('127.0.0.1')).toBeInTheDocument()
    // 端口与监听地址各一条：少任何一条都意味着有一项改完不知道要重启。
    expect(screen.getAllByText(zh.prefsRestartHint)).toHaveLength(2)
  })

  it('配置里认不出的项照实说，而不是当作没发生', async () => {
    renderPage({ preferences: () => json(preferences('balanced', ['automation_mode=turbo'])) })

    expect(await screen.findByText(zh.prefsWarning('automation_mode=turbo'))).toBeInTheDocument()
  })

  it('读不回来时说清 Gateway 仍按自己那份配置工作', async () => {
    renderPage({
      preferences: () => json({ error: { code: 'internal', message: '坏了', operation_id: 'op-1' } }, 500),
    })

    expect(await screen.findByText(zh.prefsErrorTitle)).toBeInTheDocument()
  })
})

describe('三种自动化等级（AC2、REQ-DECIDE-003 AC7）', () => {
  it('三种都可选中，当前的那个被标出来', async () => {
    renderPage({ preferences: () => json(preferences('automatic')) })

    await screen.findByText(zh.prefsAutomation)
    expect(screen.getByRole('button', { name: new RegExp(`^${zh.prefsModeAutomatic}`) })).toHaveAttribute(
      'aria-pressed',
      'true',
    )
    for (const name of [zh.prefsModeCautious, zh.prefsModeBalanced]) {
      expect(screen.getByRole('button', { name: new RegExp(`^${name}`) })).toBeEnabled()
    }
  })

  it('切到自动模式直接生效并持久化', async () => {
    renderPage()

    await screen.findByText(zh.prefsAutomation)
    fireEvent.click(screen.getByRole('button', { name: new RegExp(`^${zh.prefsModeAutomatic}`) }))
    await flush()

    const patch = calls.find((call) => call.method === 'PATCH')
    expect(patch?.path).toBe('/v1/preferences')
    expect(patch?.body).toContain('automatic')
  })

  it('切到谨慎模式先提示记忆会失效，确认之后才提交（AC7）', async () => {
    renderPage()

    await screen.findByText(zh.prefsAutomation)
    fireEvent.click(screen.getByRole('button', { name: new RegExp(`^${zh.prefsModeCautious}`) }))

    expect(screen.getByText(zh.prefsCautiousWarning)).toBeInTheDocument()
    await flush()
    expect(calls.filter((call) => call.method === 'PATCH')).toEqual([])

    fireEvent.click(screen.getByRole('button', { name: zh.prefsConfirm }))
    await flush()
    expect(calls.find((call) => call.method === 'PATCH')?.body).toContain('cautious')
  })

  it('取消之后什么也没发生', async () => {
    renderPage()

    await screen.findByText(zh.prefsAutomation)
    fireEvent.click(screen.getByRole('button', { name: new RegExp(`^${zh.prefsModeCautious}`) }))
    fireEvent.click(screen.getByRole('button', { name: zh.prefsCancel }))
    await flush()

    expect(calls.filter((call) => call.method === 'PATCH')).toEqual([])
    expect(screen.queryByText(zh.prefsCautiousWarning)).not.toBeInTheDocument()
  })

  it('高风险那一行写着任何配置都不例外', async () => {
    renderPage()

    expect(await screen.findByText(zh.prefsHighRiskValue)).toBeInTheDocument()
  })
})

describe('清除 Trust Memory（AC3）', () => {
  it('没有记忆时说清没有可清除的东西', async () => {
    renderPage()

    expect(await screen.findByText(zh.prefsClearEmpty)).toBeInTheDocument()
  })

  it('要二次确认：第一次点击不删任何东西', async () => {
    renderPage({ memories: () => json({ items: [memory('tm-1'), memory('tm-2')] }) })

    fireEvent.click(await screen.findByRole('button', { name: zh.prefsClear }))
    await flush()

    expect(screen.getByRole('button', { name: zh.prefsClearConfirm })).toBeInTheDocument()
    expect(calls.filter((call) => call.method === 'DELETE')).toEqual([])
  })

  it('确认之后逐条清除，并如实报出清掉了几条', async () => {
    renderPage({ memories: () => json({ items: [memory('tm-1'), memory('tm-2')] }) })

    fireEvent.click(await screen.findByRole('button', { name: zh.prefsClear }))
    fireEvent.click(screen.getByRole('button', { name: zh.prefsClearConfirm }))

    await waitFor(() => {
      expect(screen.getByText(zh.prefsCleared(2))).toBeInTheDocument()
    })
    expect(calls.filter((call) => call.method === 'DELETE').map((call) => call.path)).toEqual([
      '/v1/trust-memories/tm-1',
      '/v1/trust-memories/tm-2',
    ])
  })

  it('中途失败时不报「全部清除成功」', async () => {
    let attempt = 0
    renderPage({
      memories: () => json({ items: [memory('tm-1'), memory('tm-2')] }),
      del: () => {
        attempt += 1
        return attempt === 1
          ? Promise.resolve(new Response(null, { status: 204 }))
          : json({ error: { code: 'internal', message: '坏', operation_id: 'op' } }, 500)
      },
    })

    fireEvent.click(await screen.findByRole('button', { name: zh.prefsClear }))
    fireEvent.click(screen.getByRole('button', { name: zh.prefsClearConfirm }))

    expect(await screen.findByText(zh.prefsClearFailed)).toBeInTheDocument()
    expect(screen.queryByText(zh.prefsCleared(2))).not.toBeInTheDocument()
  })
})

describe('设定主密码（用户决定 D-15）', () => {
  it('太短时提交不出去', async () => {
    renderPage()

    const field = await screen.findByLabelText(zh.prefsVaultSet)
    fireEvent.change(field, { target: { value: 'short' } })
    expect(screen.getByRole('button', { name: zh.prefsVaultSet })).toBeDisabled()
  })

  it('设定成功后说保险库已解锁，且主密码不留在页面上', async () => {
    const { container } = renderPage()

    fireEvent.change(await screen.findByLabelText(zh.prefsVaultSet), {
      target: { value: SENTINEL_TOKEN },
    })
    fireEvent.click(screen.getByRole('button', { name: zh.prefsVaultSet }))

    expect(await screen.findByText(zh.prefsVaultDone)).toBeInTheDocument()
    expect(container.innerHTML).not.toContain(SENTINEL_TOKEN)
    expect(calls.find((call) => call.path === '/v1/vault')?.method).toBe('POST')
  })

  it('已经有保险库时照实说，且不声称覆盖成功', async () => {
    renderPage({
      vault: () => json({ error: { code: 'conflict', message: '已存在', operation_id: 'op' } }, 409),
    })

    fireEvent.change(await screen.findByLabelText(zh.prefsVaultSet), {
      target: { value: SENTINEL_TOKEN },
    })
    fireEvent.click(screen.getByRole('button', { name: zh.prefsVaultSet }))

    expect(await screen.findByText(zh.prefsVaultExists)).toBeInTheDocument()
    expect(screen.queryByText(zh.prefsVaultDone)).not.toBeInTheDocument()
    // 送出即忘。查的是 value：明文本来就不会进 innerHTML，
    // 只查那一处的断言会因为查错了地方而通过。
    expect(screen.getByLabelText(zh.prefsVaultSet)).toHaveValue('')
  })
})

// 窄屏拦截移到了路由层，用例见 src/app/breakpoints.test.tsx。

describe('样式', () => {
  it('不引入新色板', () => {
    expect(css).not.toMatch(/#[0-9a-fA-F]{3,8}\b/)
    expect(css).not.toMatch(/rgba?\(/)
    expect(css).toContain('var(--')
  })

  it('红色只用在破坏性操作那一处（REQ-UI-012 AC1）', () => {
    const blocks = css.split('\n\n').filter((block) => block.includes('--block'))

    for (const block of blocks) {
      expect(block).toMatch(/\.danger/)
    }
  })
})
