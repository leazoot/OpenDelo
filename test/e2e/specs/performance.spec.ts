import type { Page } from '@playwright/test'
import { test, expect } from '../harness/fixtures.js'
import type { WebAPI } from '../harness/api.js'
import { connect, openConsole } from '../harness/prepare.js'
import type { ListOf } from '../harness/views.js'

/*
 * REQ-NFR-001 中要浏览器才量得到的三项。
 *
 * 另外三项在别处：决策链路在 test/bench，Ledger 查询在 internal/store，
 * 首屏包体在 scripts/check-bundle.mjs。落点一览见 docs/09_TEST_PLAN.md §10.7。
 *
 * 三条都跑真实二进制与真实浏览器 —— 这几个数说的就是「人要等多久」，
 * 换成组件测试量到的只是渲染函数的耗时。
 *
 * **样本量与 P95 的关系：** 这里每条只取十来个样本，`percentile` 因此几乎
 * 等于最差的那一次。这是有意的：样本少时取最差比取中位数保守，
 * 不会因为「大部分很快」而放过偶发的慢。
 */

/** 各项的预算（REQ-NFR-001）。 */
const arrivalBudget = 1_000
const firstPaintBudget = 1_500
const countdownDriftBudget = 2

const gitHubIdentity = { service: 'github', item: 'GitHub Bot', field: 'token' } as const
const repository = { owner: 'octocat', repo: 'hello-world' }

/** 缝前的请求卡片。选中态是它独有的标记，与文案无关。 */
const passageCards = (page: Page) => page.locator('button[aria-pressed]')

/** 缝内侧的授权标签。可拖出缝外收回，这个属性别处没有。 */
const leaseTabs = (page: Page) => page.locator('button[draggable="true"]')

// ---------------------------------------------------------------- 审批产生 → Console 显示

test('审批项从产生到出现在已打开的 Console，P95 < 1s', async ({ api, agent, page }) => {
  await connect(api, { ...gitHubIdentity, accountLabel: 'work' })
  const session = await agent()

  await openConsole(page, '/gate')
  await expect(passageCards(page)).toHaveCount(0)

  const samples: number[] = []
  for (let round = 0; round < 12; round++) {
    // 计时从**发出调用**开始，比「审批项落库」早一点点：决策链路那段被算进来
    // 只会让结果偏保守，而落库那一刻在浏览器这一侧观察不到。
    const pending = session.call('github.issue.create', {
      ...repository,
      title: `第 ${String(round + 1)} 次`,
    })
    const started = Date.now()

    await expect(passageCards(page)).toHaveCount(round + 1)
    samples.push(Date.now() - started)

    const outcome = await pending
    expect(outcome.rpcError).toBe('')
    expect(outcome.refused).toBe(true)
  }

  const p95 = percentile(samples)
  expect(
    p95,
    `审批项出现在 Console 上的 P95 为 ${String(p95)}ms（样本 ${samples.join('/')}）`,
  ).toBeLessThan(arrivalBudget)
})

// ---------------------------------------------------------------- Gate 首屏

test('Gate 页面首屏（本机、冷缓存）P95 < 1.5s', async ({ api, agent, gateway, browser }) => {
  await connect(api, { ...gitHubIdentity, accountLabel: 'work' })

  // 先让缝前有一条真实内容：空页面的首屏比有内容时快，量它等于放宽了预算。
  // 用一条停在缝前的写请求 —— 自动放行的那些不进 `GET /v1/approvals`，
  // 冷启动的页面上看不到它们。
  const session = await agent()
  expect((await session.call('github.issue.create', { ...repository, title: '首屏' })).refused).toBe(
    true,
  )

  const samples: number[] = []
  for (let round = 0; round < 6; round++) {
    // 每轮一个全新的 context —— 冷缓存是这项指标的前提，复用 context
    // 第二轮起就是内存命中，量到的不是用户第一次打开的那一下。
    const context = await browser.newContext({ baseURL: gateway.consoleURL })
    const fresh = await context.newPage()
    try {
      const started = Date.now()
      await fresh.goto('/gate')
      await expect(passageCards(fresh)).toHaveCount(1)
      samples.push(Date.now() - started)
    } finally {
      await context.close()
    }
  }

  const p95 = percentile(samples)
  expect(p95, `Gate 首屏 P95 为 ${String(p95)}ms（样本 ${samples.join('/')}）`).toBeLessThan(
    firstPaintBudget,
  )
})

// ---------------------------------------------------------------- Lease 倒计时

test('Lease 剩余时间每秒刷新一次，与真实时间的误差不超过 2s', async ({ api, agent, page }) => {
  await connect(api, { ...gitHubIdentity, accountLabel: 'work' })
  const session = await agent()

  // 要一条**留在架上**的授权：自动放行签发的那条用完就结束了，
  // 架上留不下东西也就没有倒计时可看。「允许到任务结束」的那条会一直在。
  expect((await session.call('github.issue.create', { ...repository, title: '倒计时' })).refused).toBe(
    true,
  )
  await settleOnlyApproval(api, 'allow-task')

  // Lease 默认十五分钟，而剩余时间只在最后一分钟里按秒显示。
  // 把页面的时钟推到那一分钟，再让它按真实速度继续走 —— 被造出来的只有起点，
  // 之后的每一次跳动都是真实的一秒。
  await page.clock.install({ time: new Date() })
  await page.goto('/gate')
  await expect(leaseTabs(page)).toHaveCount(1)

  await page.clock.fastForward('14:15')
  await page.clock.resume()

  const readings: { at: number; left: number }[] = []
  const until = Date.now() + 6_000
  while (Date.now() < until) {
    readings.push({ at: Date.now(), left: await remainingSeconds(page) })
    await page.waitForTimeout(200)
  }

  const first = readings[0]
  const last = readings[readings.length - 1]
  if (first === undefined || last === undefined) {
    throw new Error('一次都没读到剩余时间')
  }

  // 每秒一次：六秒里至少要出现五个不同的读数。
  const distinct = new Set(readings.map((reading) => reading.left))
  expect(
    distinct.size,
    `六秒里只出现了 ${String(distinct.size)} 个不同的读数：${[...distinct].join('/')}`,
  ).toBeGreaterThanOrEqual(5)

  // 误差：显示走过的秒数应当与真实走过的秒数一致。
  const shown = first.left - last.left
  const elapsed = (last.at - first.at) / 1_000
  expect(
    Math.abs(shown - elapsed),
    `显示走了 ${String(shown)}s，真实走了 ${elapsed.toFixed(1)}s`,
  ).toBeLessThanOrEqual(countdownDriftBudget)
})

// ---------------------------------------------------------------- 辅助

/** percentile 取样本的 P95。样本少时它就是最差的那一次，见文件头的说明。 */
function percentile(samples: readonly number[]): number {
  const sorted = [...samples].sort((left, right) => left - right)
  const value = sorted[Math.floor((sorted.length * 95) / 100)] ?? sorted[sorted.length - 1]
  if (value === undefined) {
    throw new Error('没有样本')
  }
  return value
}

/** settleOnlyApproval 替人对此刻唯一的待决项做出决定。 */
async function settleOnlyApproval(api: WebAPI, action: string): Promise<void> {
  const listed = await api.get<ListOf<{ id: string; status: string }>>('/v1/approvals')
  const waiting = listed.body.items.filter((item) => item.status === 'pending')
  expect(waiting).toHaveLength(1)
  const only = waiting[0]
  if (only === undefined) {
    throw new Error('没有待决项')
  }
  const decided = await api.post<unknown>(`/v1/approvals/${only.id}/${action}`, {})
  expect(decided.status, JSON.stringify(decided.body)).toBe(200)
}

/** remainingSeconds 读出授权标签上显示的剩余秒数。 */
async function remainingSeconds(page: Page): Promise<number> {
  const text = await leaseTabs(page).first().innerText()
  const matched = /(\d+)s/.exec(text)
  if (matched?.[1] === undefined) {
    throw new Error(`授权标签上没有按秒显示的剩余时间：${text}`)
  }
  return Number(matched[1])
}
