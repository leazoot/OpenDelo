import { test, expect } from '../harness/fixtures.js'
import { sentinelToken } from '../harness/sentinel.js'

/*
 * E2E 基础设施的自证用例（TASK-0701）。
 *
 * 这些用例证明的不是产品行为，而是**后面所有用例赖以成立的前提**：
 * 跑的是真实二进制、每个用例互不相干、凭据来自假 op、出站只到本地假服务。
 * 其中任何一条不成立，S1–S10 的结论就都是假的。
 */

test('真实二进制起得来，Console 由它自己提供', async ({ page, gateway }) => {
  const response = await page.goto('/')

  expect(response?.status()).toBe(200)
  await expect(page).toHaveTitle(/^OpenDelo/)

  // Console 是从这个进程里出来的，不是 dev server：入口文档带着它注入的会话令牌。
  const injected = await page
    .locator('meta[name="opendelo-session-token"]')
    .getAttribute('content')
  expect(injected).toBe(gateway.sessionToken)
})

test('Console 拿得到自己的数据，四个导航项都在', async ({ page }) => {
  await page.goto('/')

  for (const label of ['Gate', 'Identities', 'Automation', 'Ledger']) {
    await expect(page.getByRole('link', { name: label })).toBeVisible()
  }
})

test('假 op 让凭据取用路径整条走通', async ({ api }) => {
  const connected = await api.connectIdentity({
    service: 'github',
    item: 'GitHub Bot',
    field: 'token',
    accountLabel: 'e2e-bot',
  })

  expect(connected.status).toBe(201)

  const listed = await api.get<{ items: { account_label: string }[] }>('/v1/identities')
  expect(listed.status).toBe(200)
  expect(listed.body.items.map((identity) => identity.account_label)).toEqual(['e2e-bot'])
})

test('取不到的坐标不留下身份', async ({ api }) => {
  // 假 op 只认三个字段名。认不出的那个走的是真实的失败路径。
  const refused = await api.connectIdentity({
    service: 'github',
    item: 'GitHub Bot',
    field: 'not_a_field',
    accountLabel: 'e2e-bot',
  })

  expect(refused.status).toBeGreaterThanOrEqual(400)

  const listed = await api.get<{ items: unknown[] }>('/v1/identities')
  expect(listed.body.items).toEqual([])
})

test('每个用例拿到的是一个空实例', async ({ api, gateway }) => {
  // 与上面两个用例共用一份数据目录的话，这里会看到它们连出来的身份。
  const listed = await api.get<{ items: unknown[] }>('/v1/identities')

  expect(listed.body.items).toEqual([])
  expect(gateway.configDir).toContain('opendelo-e2e-')
})

test('假外部服务按真实形状应答，并记下每一次请求', async ({ external }) => {
  const response = await fetch(`${external.github.baseURL}/repos/octocat/hello-world`)
  const repository = (await response.json()) as { full_name: string }

  expect(response.status).toBe(200)
  expect(repository.full_name).toBe('octocat/hello-world')

  const arrived = external.github.received()
  expect(arrived).toHaveLength(1)
  expect(arrived[0]?.path).toBe('/repos/octocat/hello-world')
})

test('假外部服务不认的路径返回 404，而不是兜底放行', async ({ external }) => {
  const response = await fetch(`${external.cloudflare.baseURL}/there/is/no/such/thing`)

  expect(response.status).toBe(404)
})

test('没有 Proxy 授权的请求既不放行也不产生出站流量', async ({ gateway, external }) => {
  const target = new URL(external.github.baseURL)

  const response = await fetch(`${gateway.proxyURL}/repos/octocat/hello-world`, {
    headers: { Host: target.host },
  })

  expect(response.status).toBeGreaterThanOrEqual(400)
  expect(external.github.received()).toHaveLength(0)
})

test('Console 的入口文档里没有哨兵', async ({ page }) => {
  await page.goto('/')

  expect(await page.content()).not.toContain(sentinelToken)
})
