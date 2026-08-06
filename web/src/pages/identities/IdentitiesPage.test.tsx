import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import { QueryClientProvider } from '@tanstack/react-query'
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { createQueryClient } from '../../data/queryClient'
import { copyFor } from '../../i18n/copy'
import { useLanguageStore } from '../../state/language'

import { SENTINEL_TOKEN } from '../../test/sentinel'

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
  connectable_services: ['github'],
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
  readonly connect?: (body: string) => Promise<Response>
  readonly trust?: (body: string) => Promise<Response>
}

function renderPage(responses: Responses = {}) {
  vi.stubGlobal('fetch', (path: string, init?: RequestInit) => {
    calls.push({ path, method: init?.method ?? 'GET' })
    // 确认要排在名册前面：两者的路径前缀相同。
    if (path.endsWith('/trust')) {
      const sent = typeof init?.body === 'string' ? init.body : ''
      return (responses.trust ?? (() => json({})))(sent)
    }
    if (path.startsWith('/v1/agents')) {
      return (responses.agents ?? (() => json(agents)))()
    }
    // 连接要排在身份列表前面：两者的路径前缀相同。
    if (path === '/v1/identities/connect') {
      const sent = typeof init?.body === 'string' ? init.body : ''
      return (responses.connect ?? (() => json(identities.items[0], 201)))(sent)
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

  it('连接身份的按钮可用，展开的是一张只收坐标的表单', async () => {
    renderPage()

    const connect = await screen.findByRole('button', { name: zh.identitiesConnect })
    expect(connect).toBeEnabled()

    fireEvent.click(connect)

    const form = screen.getByRole('form', { name: zh.identitiesConnect })
    // 默认是 1Password，因此问的是保险库 / 条目 / 字段。
    expect(within(form).getByLabelText(zh.identitiesOnePasswordVault)).toBeInTheDocument()
    expect(within(form).getByLabelText(zh.identitiesOnePasswordItem)).toBeInTheDocument()
    expect(within(form).getByLabelText(zh.identitiesOnePasswordField)).toBeInTheDocument()

    // 这张表单没有一处可以填凭据：一个 password 类型的输入框都不存在。
    expect(form.querySelector('input[type="password"]')).toBeNull()
  })
})

describe('连接身份（REQ-CRED-002 AC1）', () => {
  beforeEach(() => {
    document.head.innerHTML = '<meta name="opendelo-session-token" content="test-session-token">'
    useLanguageStore.setState({ language: 'zh' })
    calls.length = 0
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    document.head.innerHTML = ''
  })

  /** 填完一份合法的坐标并提交。全程只用键盘能做到的事。 */
  async function fillAndSubmit() {
    fireEvent.click(await screen.findByRole('button', { name: zh.identitiesConnect }))
    const form = screen.getByRole('form', { name: zh.identitiesConnect })

    for (const [label, value] of [
      [zh.identitiesOnePasswordVault, 'Work'],
      [zh.identitiesOnePasswordItem, 'GitHub Bot'],
      [zh.identitiesOnePasswordField, 'token'],
      [zh.identitiesConnectService, 'github'],
      [zh.identitiesConnectAccount, 'bot'],
    ] as const) {
      fireEvent.change(within(form).getByLabelText(label), { target: { value } })
    }

    fireEvent.submit(form)
    return form
  }

  it('提交的是坐标，请求体里没有任何凭据字段', async () => {
    let sent = ''
    renderPage({
      connect: (body) => {
        sent = body
        return json(identities.items[0], 201)
      },
    })

    await fillAndSubmit()
    await waitFor(() => {
      expect(sent).not.toBe('')
    })

    const payload: unknown = JSON.parse(sent)
    // op:// 前缀由表单拼出来，用户不用知道它存在。
    expect(payload).toEqual({
      provider_kind: '1password',
      provider_label: 'Work',
      provider_item_ref: 'op://Work/GitHub Bot',
      field: 'token',
      service: 'github',
      account_label: 'bot',
      environment: 'non-production',
      is_default: false,
    })
    // 一次连接是一次状态变更，不能走 GET（`.claude/rules/security.md` §8）。
    expect(calls).toContainEqual({ path: '/v1/identities/connect', method: 'POST' })
  })

  it('换成钥匙串时问的是另一组东西，拼出的坐标也是另一种形状', async () => {
    let sent = ''
    renderPage({
      connect: (body) => {
        sent = body
        return json(identities.items[0], 201)
      },
    })

    fireEvent.click(await screen.findByRole('button', { name: zh.identitiesConnect }))
    const form = screen.getByRole('form', { name: zh.identitiesConnect })

    fireEvent.change(within(form).getByLabelText(zh.identitiesConnectKind), {
      target: { value: 'macos-keychain' },
    })

    // 保险库那一项换成了条目类型 —— 钥匙串里没有「保险库」这个东西。
    expect(within(form).queryByLabelText(zh.identitiesOnePasswordVault)).toBeNull()
    expect(within(form).getByLabelText(zh.identitiesKeychainItemKind)).toBeInTheDocument()

    for (const [label, value] of [
      [zh.identitiesKeychainService, 'github.com'],
      [zh.identitiesKeychainAccount, 'octocat'],
      [zh.identitiesConnectService, 'github'],
    ] as const) {
      fireEvent.change(within(form).getByLabelText(label), { target: { value } })
    }
    fireEvent.submit(form)

    await waitFor(() => {
      expect(sent).not.toBe('')
    })
    const payload: unknown = JSON.parse(sent)
    expect(payload).toMatchObject({
      provider_kind: 'macos-keychain',
      // keychain:// 前缀与条目类型由表单拼出来。
      provider_item_ref: 'keychain://internet/github.com',
      // 钥匙串的 field 是 `security -a` 的账号名，不是字段名。
      field: 'octocat',
      // 身份名留空时取账号名。
      account_label: 'octocat',
    })
  })

  it('「服务」的提示随条目类型换：应用密码不给网站密码的例子', async () => {
    // 两种条目类型下「服务」这一格填的根本不是同一种东西 —— 网站密码填主机名，
    // 应用密码填建条目时自己起的名字。给同一个 github.com 的例子，
    // 选应用密码的人照着填必然取不到（2026-08-06 人工验收踩到）。
    renderPage()

    fireEvent.click(await screen.findByRole('button', { name: zh.identitiesConnect }))
    const form = screen.getByRole('form', { name: zh.identitiesConnect })
    fireEvent.change(within(form).getByLabelText(zh.identitiesConnectKind), {
      target: { value: 'macos-keychain' },
    })

    expect(within(form).getByText(zh.identitiesKeychainServiceHint('internet'))).toBeInTheDocument()

    fireEvent.change(within(form).getByLabelText(zh.identitiesKeychainItemKind), {
      target: { value: 'generic' },
    })

    expect(within(form).getByText(zh.identitiesKeychainServiceHint('generic'))).toBeInTheDocument()
    expect(within(form).queryByText(zh.identitiesKeychainServiceHint('internet'))).toBeNull()
  })

  it('填不全时提交按钮不可用，按下去也不发请求', async () => {
    renderPage()

    fireEvent.click(await screen.findByRole('button', { name: zh.identitiesConnect }))
    const form = screen.getByRole('form', { name: zh.identitiesConnect })
    fireEvent.change(within(form).getByLabelText(zh.identitiesOnePasswordItem), {
      target: { value: 'GitHub Bot' },
    })

    expect(within(form).getByRole('button', { name: zh.identitiesConnectSubmit })).toBeDisabled()

    fireEvent.submit(form)
    await flush()
    expect(calls.filter((call) => call.method === 'POST')).toHaveLength(0)
  })

  it('提交过程中不受理第二次：重复提交会登记出两个身份', async () => {
    let posted = 0
    renderPage({
      connect: () => {
        posted += 1
        return new Promise<Response>(() => undefined)
      },
    })

    const form = await fillAndSubmit()
    await waitFor(() => {
      expect(within(form).getByRole('button', { name: zh.identitiesConnecting })).toBeDisabled()
    })

    fireEvent.submit(form)
    await flush()
    expect(posted).toBe(1)
  })

  it('来源取不到时说清楚什么都没写入，并给出下一步', async () => {
    renderPage({
      connect: () =>
        json(
          {
            error: {
              code: 'provider_unavailable',
              message: 'The credential provider is unavailable.',
              operation_id: 'op-1',
            },
          },
          503,
        ),
    })

    await fillAndSubmit()
    expect(await screen.findByText(zh.identitiesConnectProviderDown)).toBeInTheDocument()
  })

  it('哪一项填错了由错误体的 fields 指出，而不是只说一句失败了', async () => {
    renderPage({
      connect: () =>
        json(
          {
            error: { code: 'invalid_request', message: 'The request is not valid.', operation_id: 'op-2' },
            fields: ['service'],
          },
          400,
        ),
    })

    await fillAndSubmit()
    expect(await screen.findByText(zh.identitiesConnectBadField('service'))).toBeInTheDocument()
  })

  it('Esc 收起表单，焦点回到叫它出来的那个按钮', async () => {
    renderPage()

    const connect = await screen.findByRole('button', { name: zh.identitiesConnect })
    fireEvent.click(connect)

    fireEvent.keyDown(screen.getByRole('form', { name: zh.identitiesConnect }), { key: 'Escape' })

    await waitFor(() => {
      expect(screen.queryByRole('form', { name: zh.identitiesConnect })).toBeNull()
    })
    expect(document.activeElement).toBe(connect)
  })

  it('渲染出来的 DOM 里不含凭据哨兵', async () => {
    const { container } = renderPage({
      connect: () => json({ ...identities.items[0], leaked: SENTINEL_TOKEN }, 201),
    })

    await fillAndSubmit()
    await flush()
    expect(container.innerHTML).not.toContain(SENTINEL_TOKEN)
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

  /*
   * 选中态只许改外观，不许改盒模型（回归）。
   *
   * `.held` 原本在同一份 CSS Module 里被两个不同的元素共用：拿在手上的那张
   * Agent 卡片，和底部「拿起了 X」那句提示。CSS Module 按**类名**作用域，
   * 同名就是同一个类 —— 提示那条的 `padding: 12px 0` 因此盖掉了卡片的
   * `13px 15px`，选中的一瞬间左右内间距归零，卡片自己跳一下。
   *
   * 守的是「修饰类不碰盒模型」这条更一般的规矩：选中让布局位移，无论成因
   * 都是缺陷。人工验收时用户一眼看出来的正是这个。
   */
  it('选中一张 Agent 卡片不会改变它的盒模型', () => {
    const boxModel = ['padding', 'margin', 'width', 'height', 'font-size', 'border-width']

    // 注释先去掉：它会连在选择器前面，留着的话规则就认不出来了。
    const withoutComments = css.replace(/\/\*[\s\S]*?\*\//g, '')
    // 取出全部把 .held 列进选择器的规则 —— 单独出现和写在分组里都算。
    const rules = [...withoutComments.matchAll(/([^{}]+)\{([^}]*)\}/g)].filter(([, selectors]) =>
      (selectors ?? '')
        .split(',')
        .map((one) => one.trim())
        .includes('.held'),
    )
    expect(rules.length, '.held 一条规则都没有，用例是空跑的').toBeGreaterThan(0)

    for (const [, selectors, declarations] of rules) {
      for (const declaration of (declarations ?? '').split(';')) {
        const property = declaration.split(':')[0]?.trim() ?? ''
        expect(
          boxModel.some((each) => property === each || property.startsWith(`${each}-`)),
          `选中态规则 ${(selectors ?? '').trim()} 设了 ${property} —— 选中会让卡片跳一下`,
        ).toBe(false)
      }
    }
  })

  it('两列各占一半，谁也不能把缝挤开', () => {
    const column = /\.column \{([\s\S]*?)\n\}/.exec(css)?.[1] ?? ''

    expect(column).toContain('flex: 1')
    expect(column).toContain('min-width: 0')
  })
})

/*
 * Agent 信任确认（REQ-AGENT-002 AC3）。
 *
 * 这条闭环此前是断的：后端有 `POST /v1/agents/:id/trust`，Console 只读 trust_level
 * 拿去展示，从来不调它。而未确认的 Agent 写操作**永远**不会被自动放行 ——
 * 于是「今后在当前项目自动允许」光靠界面走不到（2026-08-04 人工验收撞出）。
 */
describe('Agent 信任确认', () => {
  beforeEach(() => {
    document.head.innerHTML = '<meta name="opendelo-session-token" content="test-session-token">'
    useLanguageStore.setState({ language: 'zh' })
    calls.length = 0
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    document.head.innerHTML = ''
  })

  const unverified = {
    items: [
      {
        id: 'ag-new',
        name: 'bash',
        type: 'generic',
        device_id: 'dv-000042',
        workspace_id: 'ws-1',
        trust_level: 'unverified',
        status: 'disconnected',
        last_seen_at: '2026-08-04T16:29:10Z',
      },
    ],
  }

  const withUnverified = (extra: Responses = {}) =>
    renderPage({ agents: () => json(unverified), ...extra })

  it('未确认的 Agent 说得出后果，而不只是标一个等级', async () => {
    withUnverified()

    expect(await screen.findByText(zh.identitiesUnverified)).toBeInTheDocument()
    // 用户要知道的是「这会怎样」，不是内部等级名。
    expect(screen.getByText(zh.identitiesUnverifiedWhy)).toBeInTheDocument()
  })

  it('确认一次，发出的是 POST /v1/agents/:id/trust', async () => {
    let sent = ''
    withUnverified({
      trust: (body) => {
        sent = body
        return json({})
      },
    })

    fireEvent.click(await screen.findByRole('button', { name: zh.identitiesConfirmAria('bash') }))

    await waitFor(() => {
      expect(calls).toContainEqual({ path: '/v1/agents/ag-new/trust', method: 'POST' })
    })
    expect(JSON.parse(sent)).toEqual({ confirmed: true })
  })

  it('已经确认过的 Agent 上没有这个入口', async () => {
    renderPage()

    await screen.findByText('writer-agent')
    expect(screen.queryByText(zh.identitiesUnverified)).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /确认/ })).not.toBeInTheDocument()
  })

  it('确认失败时说清楚等级没有变', async () => {
    withUnverified({ trust: () => json({ error: { code: 'internal' } }, 500) })

    fireEvent.click(await screen.findByRole('button', { name: zh.identitiesConfirmAria('bash') }))

    expect(await screen.findByText(zh.identitiesConfirmFailed)).toBeInTheDocument()
    // 失败之后仍然是未确认：界面不许先说成已确认再回滚。
    expect(screen.getByText(zh.identitiesUnverified)).toBeInTheDocument()
  })

  it('不用鼠标也确认得了', async () => {
    withUnverified()

    const confirm = await screen.findByRole('button', { name: zh.identitiesConfirmAria('bash') })
    confirm.focus()
    expect(confirm).toHaveFocus()

    fireEvent.click(confirm)
    await waitFor(() => {
      expect(calls).toContainEqual({ path: '/v1/agents/ag-new/trust', method: 'POST' })
    })
  })

  it('确认中不接受第二次点击', async () => {
    let answer: (value: Response) => void = () => undefined
    withUnverified({ trust: () => new Promise<Response>((resolve) => (answer = resolve)) })

    const confirm = await screen.findByRole('button', { name: zh.identitiesConfirmAria('bash') })
    fireEvent.click(confirm)

    await waitFor(() => {
      expect(confirm).toBeDisabled()
    })
    expect(confirm).toHaveTextContent(zh.identitiesConfirming)

    fireEvent.click(confirm)
    expect(calls.filter((call) => call.path.endsWith('/trust'))).toHaveLength(1)

    await act(async () => {
      answer(await json({}))
    })
  })
})
