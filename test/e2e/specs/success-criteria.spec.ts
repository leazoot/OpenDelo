import { test, expect } from '../harness/fixtures.js'
import type { WebAPI } from '../harness/api.js'
import { connect } from '../harness/prepare.js'
import { allSentinels, sentinelToken } from '../harness/sentinel.js'
import type {
  ApprovalView,
  AuditEventView,
  ListOf,
  TrustMemoryView,
} from '../harness/views.js'

/*
 * PRD §32 的十条核心验收标准，一条一个用例。
 *
 * 每个用例跑的都是真实二进制：真的决策链路、真的 Lease、真的审计、真的凭据取用
 * （从 PATH 上的假 op），出站只到本地假外部服务。断言尽量打在**行为**上而不是
 * 内部状态上 —— Agent 拿到什么、人看到什么、有没有东西真的发出去。
 *
 * 名字里带 S1–S10，与 PRD §32 的小节一一对应，删掉哪一条一眼可见。
 */

/** 各服务在假保险库里的条目与账号标签。 */
const gitHubIdentity = { service: 'github', item: 'GitHub Bot', field: 'token' } as const
const cloudflareIdentity = { service: 'cloudflare', item: 'Cloudflare Bot', field: 'token' } as const

/** repository 是各用例共用的那个仓库坐标。 */
const repository = { owner: 'octocat', repo: 'hello-world' }

// ---------------------------------------------------------------- S1

test('S1 · 无规则即可使用：连接身份后 Agent 直接读到了仓库', async ({
  api,
  agent,
  external,
}) => {
  await connect(api, { ...gitHubIdentity, accountLabel: 'work' })

  // 「不创建规则」是这条标准的全部重点：这里没有任何一次写入记忆或策略的动作。
  const before = await api.get<ListOf<TrustMemoryView>>('/v1/trust-memories')
  expect(before.body.items).toEqual([])

  const session = await agent()
  const read = await session.call('github.repository.read', repository)

  expect(read.rpcError).toBe('')
  expect(read.refused).toBe(false)
  expect(read.text).toContain('octocat/hello-world')

  // 真的出去了一次，而且只出去了这一次。
  expect(external.github.received().map((arrived) => arrived.path)).toEqual([
    '/repos/octocat/hello-world',
  ])
})

// ---------------------------------------------------------------- S2

test('S2 · 自动身份匹配：这个项目用过哪个身份，之后就一直用它', async ({ api, agent }) => {
  // 「自动选择」靠的是**这个项目用过哪一个**（REQ-IDENT-002 的第三级），
  // 不是身份上的默认标记 —— 存在多个候选时取默认是猜，产品明确不猜。
  //
  // 两个身份都标 production：候选不止一个时环境判不出来，会按生产处理
  // （见 docs/12_PROGRESS.md 的 R-23）。这里不去绕开那条规则，
  // 而是让身份与它一致 —— 本条标准问的是选中了谁，不是环境怎么推。
  const work = await connect(api, {
    ...gitHubIdentity,
    accountLabel: 'work',
    environment: 'production',
  })

  const session = await agent({ workspacePath: '/work/telecall' })
  await session.confirm()
  expect((await session.call('github.issue.create', { ...repository, title: '第一次' })).refused).toBe(
    true,
  )
  await settle(api, (await onlyApproval(api)).id, 'allow-project')

  // 后来又连了个人账号。同一个项目里的同一件事不该因此变得需要重新问人。
  const personal = await connect(api, {
    ...gitHubIdentity,
    accountLabel: 'personal',
    environment: 'production',
    isDefault: false,
  })

  const again = await session.call('github.issue.create', { ...repository, title: '第二次' })
  expect(again.refused).toBe(false)

  const executed = await eventFor(api, 'adapter.executed')
  expect(executed.identity_id).toBe(work.id)
  expect(executed.identity_id).not.toBe(personal.id)
})

// ---------------------------------------------------------------- S3

test('S3 · 低风险自动执行：读仓库与查 DNS 都没有惊动人', async ({ api, agent, external }) => {
  await connect(api, { ...gitHubIdentity, accountLabel: 'work' })
  await connect(api, { ...cloudflareIdentity, accountLabel: 'dns' })

  const session = await agent()
  const read = await session.call('github.repository.read', repository)
  const dns = await session.call('cloudflare.dns_records.read', { zone_id: 'zone-1' })

  expect(read.refused).toBe(false)
  expect(dns.refused).toBe(false)

  const pending = await api.get<ListOf<ApprovalView>>('/v1/approvals')
  expect(pending.body.items).toEqual([])

  expect(external.github.received()).toHaveLength(1)
  expect(external.cloudflare.received()).toHaveLength(1)
})

// ---------------------------------------------------------------- S4

test('S4 · 中风险首次确认：第一次建 PR 停在缝前，什么也没有发出去', async ({
  api,
  agent,
  external,
}) => {
  await connect(api, { ...gitHubIdentity, accountLabel: 'work' })

  const session = await agent()
  const created = await session.call('github.pull_request.create', {
    ...repository,
    title: '一个 PR',
    head: 'feature',
    base: 'main',
  })

  // 拒绝是产品的正常输出，不是故障：模型据此知道该去找人。
  expect(created.rpcError).toBe('')
  expect(created.refused).toBe(true)
  expect(created.text).toContain('approve')

  const waiting = await onlyApproval(api)
  expect(waiting.decision?.risk_level).toBe('medium')
  expect(waiting.request?.operation).toBe('create_pull_request')

  // 「什么也没有发出去」是这条标准的另一半。
  expect(external.github.received()).toEqual([])
})

// ---------------------------------------------------------------- S5

test('S5 · 自动学习：选了「今后在当前项目自动允许」之后同范围不再询问', async ({
  api,
  agent,
  external,
}) => {
  await connect(api, { ...gitHubIdentity, accountLabel: 'work' })
  const session = await agent()
  await session.confirm()

  const first = await session.call('github.issue.create', { ...repository, title: '第一次' })
  expect(first.refused).toBe(true)

  const waiting = await onlyApproval(api)
  expect(waiting.available_actions).toContain('auto_allow_in_project')
  await settle(api, waiting.id, 'allow-project')

  const memories = await api.get<ListOf<TrustMemoryView>>('/v1/trust-memories')
  expect(memories.body.items).toHaveLength(1)

  const again = await session.call('github.issue.create', { ...repository, title: '第二次' })
  expect(again.refused).toBe(false)

  // 只有第二次真的出去了 —— 第一次停在缝前，那一次的批准放行的是当时那条请求。
  const posted = external.github.received().filter((arrived) => arrived.method === 'POST')
  expect(posted).toHaveLength(1)

  // **出站正文就是用户批准的那件事。** 少了这一条，「请求发出去了」与
  // 「请求发出去了但内容是空的」在断言里长成同一个样子 —— 而假 GitHub 对
  // 任何正文都回 200，于是一个把正文丢掉的实现能让整套用例全绿
  //（2026-08-04 人工验收对着真实 GitHub 撞出：空 body 的 POST 得到 422）。
  expect(posted[0]?.body, '出站正文里没有用户批准的那件事').toContain('第二次')
  const stillPending = await api.get<ListOf<ApprovalView>>('/v1/approvals')
  expect(stillPending.body.items.filter((item) => item.status === 'pending')).toEqual([])
})

// ---------------------------------------------------------------- S6

test('S6 · 范围变化重新确认：换一个项目之后同样的调用重新进入审批', async ({ api, agent }) => {
  await connect(api, { ...gitHubIdentity, accountLabel: 'work' })

  const telecall = await agent({ workspacePath: '/work/telecall' })
  await telecall.confirm()
  const first = await telecall.call('github.issue.create', { ...repository, title: '第一次' })
  expect(first.refused).toBe(true)
  await settle(api, (await onlyApproval(api)).id, 'allow-project')

  // 同一台机器、同一个身份、同一个仓库，只换了项目。
  const finance = await agent({ workspacePath: '/work/finance' })
  await finance.confirm()
  const elsewhere = await finance.call('github.issue.create', { ...repository, title: '换个项目' })

  expect(elsewhere.refused).toBe(true)
  const reopened = await pendingApprovals(api)
  expect(reopened).toHaveLength(1)
  expect(reopened[0]?.request?.operation).toBe('create_issue')
})

// ---------------------------------------------------------------- S7

test('S7 · 高风险永远确认：四种高风险操作都要人，且都不提供「今后自动允许」', async ({
  api,
  agent,
  external,
}) => {
  await connect(api, { ...gitHubIdentity, accountLabel: 'work' })
  await connect(api, { ...cloudflareIdentity, accountLabel: 'dns' })
  const session = await agent()

  const dangerous: [string, Record<string, unknown>][] = [
    ['github.repository.delete', repository],
    ['cloudflare.zone.delete', { zone_id: 'zone-1' }],
    ['cloudflare.token.manage', { token_id: 'token-1', name: '新令牌' }],
    ['cloudflare.member.manage', { account_id: 'account-1', member_id: 'member-1', role: 'admin' }],
  ]

  // 这个 Agent 是人确认过的、最宽松的那一档。高风险仍然要人。
  await session.confirm()

  for (const [tool, args] of dangerous) {
    const attempted = await session.call(tool, args)
    expect(attempted.refused, `${tool} 没有停下来`).toBe(true)
  }

  // 四条都要**停在缝前等人**，而不是被拒绝掉：REQ-DECIDE-003 说的是「永远需要
  // 人工确认」，被永久拒绝的操作连确认的机会都没有。R-38 修复前 manage_token
  // 恰恰落在后一种上（禁止列表的关键词误伤），因此这里数条数。
  const waiting = await pendingApprovals(api)
  expect(waiting.length, '有高风险操作没有停在缝前，而是被直接拒绝了').toBe(dangerous.length)
  for (const approval of waiting) {
    expect(approval.decision?.risk_level, JSON.stringify(approval.request)).toBe('high')
    // 「不得学习为永久允许」——这个等级下后端根本不提供那个选项（REQ-APPROVAL-002）。
    expect(approval.available_actions).not.toContain('auto_allow_in_project')
    expect(approval.available_actions).not.toContain('always_ask')
  }

  expect(external.github.received()).toEqual([])
  expect(external.cloudflare.received()).toEqual([])
})

// ---------------------------------------------------------------- S8

test('S8 · Secret 不可见：凭据只出现在出站请求上，其余每一面都扫不到', async ({
  api,
  agent,
  gateway,
  external,
  page,
}) => {
  await connect(api, { ...gitHubIdentity, accountLabel: 'work' })
  const session = await agent()
  const read = await session.call('github.repository.read', repository)
  expect(read.refused).toBe(false)

  // 先证明凭据**确实流动过** —— 否则下面每一条都会因为「压根没取过」而通过。
  const outbound = external.github.received()[0]
  expect(outbound?.headers['authorization']).toBe(`Bearer ${sentinelToken}`)

  // ① Agent 上下文：回给模型的那段文字。
  expectNoSentinel(read.text, '回给 Agent 的工具结果')
  // ② MCP 响应的其余部分：工具清单。
  expectNoSentinel(JSON.stringify(await session.listTools()), 'MCP 工具清单')
  // ③ 日志。
  expectNoSentinel(gateway.output(), 'Gateway 日志')
  // ④ 审计。
  const ledger = await api.get<ListOf<AuditEventView>>('/v1/audit-events')
  expectNoSentinel(JSON.stringify(ledger.body), '账本')
  // ⑤ Web API 的其余响应。
  expectNoSentinel(JSON.stringify((await api.get<unknown>('/v1/identities')).body), '身份列表')
  expectNoSentinel(JSON.stringify((await api.get<unknown>('/v1/leases')).body), 'Lease 列表')
  // ⑥ Console DOM。
  await page.goto('/identities')
  expectNoSentinel(await page.content(), 'Console DOM')
  // ⑦ 数据目录里的每一个文件（数据库、配置、会话令牌）。
  expectNoSentinel(await readDataDir(gateway.configDir), '数据目录')
})

// ---------------------------------------------------------------- S9

test('S9 · 权限自动失效：任务结束时授权被收回，之后必须重新获得', async ({ api, agent }) => {
  await connect(api, { ...gitHubIdentity, accountLabel: 'work' })
  const session = await agent()
  await session.confirm()

  const first = await session.call('github.issue.create', { ...repository, title: '第一次' })
  expect(first.refused).toBe(true)
  // 「允许到任务结束」签发的是一条绑定本次会话的 Lease。
  await settle(api, (await onlyApproval(api)).id, 'allow-task')

  const issued = await activeLeases(api)
  expect(issued).toHaveLength(1)
  expect(issued[0]?.is_session_bound).toBe(true)

  // 任务结束：会话名下的授权当场收回，不等它自己到期。
  await session.disconnect()
  expect(await activeLeases(api)).toEqual([])

  // 之后同样的调用必须重新获得授权，而不是沿用刚才那一次。
  const resumed = await agent()
  await resumed.confirm()
  const afterwards = await resumed.call('github.issue.create', { ...repository, title: '第二次' })
  expect(afterwards.refused).toBe(true)
})

// ---------------------------------------------------------------- S10

test('S10 · Gateway 失败关闭：网关停掉之后请求失败且出站为零', async ({
  api,
  agent,
  gateway,
  external,
}) => {
  await connect(api, { ...gitHubIdentity, accountLabel: 'work' })
  const session = await agent()

  const before = await session.call('github.repository.read', repository)
  expect(before.refused).toBe(false)
  const sentSoFar = external.github.received().length

  await gateway.stop()

  // 网关停了：调用必须失败，而不是「既然刚才允许过，这次就直接发出去」。
  let failed = false
  try {
    const afterwards = await session.call('github.repository.read', repository)
    failed = afterwards.refused || afterwards.rpcError !== ''
  } catch {
    // 端口已经关掉，连不上本身就是「失败关闭」。
    failed = true
  }
  expect(failed).toBe(true)

  // 出站为零：Agent 没有绕过缝直连。
  expect(external.github.received()).toHaveLength(sentSoFar)
})

// ---------------------------------------------------------------- 辅助

/** activeLeases 取回此刻仍然生效的授权。 */
async function activeLeases(
  api: WebAPI,
): Promise<{ status: string; is_session_bound: boolean }[]> {
  const listed = await api.get<ListOf<{ status: string; is_session_bound: boolean }>>('/v1/leases')
  return listed.body.items.filter((lease) => lease.status === 'active')
}

/** pendingApprovals 取回还在等人的那些审批。 */
async function pendingApprovals(api: WebAPI): Promise<ApprovalView[]> {
  const listed = await api.get<ListOf<ApprovalView>>('/v1/approvals')
  return listed.body.items.filter((item) => item.status === 'pending')
}

/** onlyApproval 断言此刻恰好有一个待决项并返回它。 */
async function onlyApproval(api: WebAPI): Promise<ApprovalView> {
  const waiting = await pendingApprovals(api)
  expect(waiting).toHaveLength(1)
  const only = waiting[0]
  if (only === undefined) {
    throw new Error('没有待决项')
  }
  return only
}

/** settle 替人做出一次决定。 */
async function settle(api: WebAPI, approvalID: string, action: string): Promise<void> {
  const decided = await api.post<unknown>(`/v1/approvals/${approvalID}/${action}`, {})
  expect(decided.status, JSON.stringify(decided.body)).toBe(200)
}

/** eventFor 取回最近一条指定类型的账本记录。 */
async function eventFor(api: WebAPI, type: string): Promise<AuditEventView> {
  const ledger = await api.get<ListOf<AuditEventView>>('/v1/audit-events')
  const found = ledger.body.items.find((event) => event.type === type)
  if (found === undefined) {
    throw new Error(`账本里没有 ${type}：${JSON.stringify(ledger.body.items.map((e) => e.type))}`)
  }
  return found
}

/** expectNoSentinel 断言一段内容里不含任何哨兵。 */
function expectNoSentinel(content: string, face: string): void {
  for (const sentinel of allSentinels) {
    expect(content.includes(sentinel), `${face}里出现了凭据`).toBe(false)
  }
}

/** readDataDir 把配置目录下的每一个文件按字节读出来拼在一起。 */
async function readDataDir(configDir: string): Promise<string> {
  const { readdir, readFile } = await import('node:fs/promises')
  const { join } = await import('node:path')

  const entries = await readdir(configDir, { recursive: true, withFileTypes: true })
  const contents: string[] = []
  for (const entry of entries) {
    if (!entry.isFile()) {
      continue
    }
    contents.push((await readFile(join(entry.parentPath, entry.name))).toString('latin1'))
  }
  return contents.join('\n')
}
