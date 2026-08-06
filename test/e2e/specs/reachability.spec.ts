import { test, expect } from '../harness/fixtures.js'
import { connect, openConsole } from '../harness/prepare.js'
import type { ListOf } from '../harness/views.js'

/*
 * 只用鼠标能不能把事情做完。
 *
 * 键盘全流程早有用例守着（`web/src/app/keyboard.test.tsx` 与设计稿的
 * REQ-APPROVAL-003），**反方向一直没有**。而学习类的决定（「今后在此项目
 * 自动允许」「始终要求确认」）只在卷宗里给出 —— 缝边的快捷键做不了它们。
 * 到不了卷宗，就等于这个产品最主打的那件事对只用鼠标的人不存在。
 *
 * 2026-08-04 的人工验收把这一条记成了 R-45（「卷宗只能用回车打开」）。
 * 本文件是那条记录的对账：整条路上一次键盘都不碰。
 */

const gitHubIdentity = { service: 'github', item: 'GitHub Bot', field: 'token' } as const
const repository = { owner: 'octocat', repo: 'hello-world' }

test('只用鼠标：从缝前一路走到卷宗，并做一个学习类的决定', async ({ api, agent, page }) => {
  await connect(api, { ...gitHubIdentity, accountLabel: 'work' })
  const session = await agent()
  await session.confirm()

  await openConsole(page, '/gate')

  const refused = await session.call('github.issue.create', { ...repository, title: '鼠标' })
  expect(refused.refused).toBe(true)

  // 一、点卡片。这一下只负责选中与展开 Inspector。
  const card = page.locator('button[aria-pressed]')
  await expect(card).toHaveCount(1)
  await card.first().click()
  await expect(card.first()).toHaveAttribute('aria-pressed', 'true')

  // 二、Inspector 上必须有一个**能点的**卷宗入口。
  //     只有「按 ↵ 打开」的提示是不够的：那是给键盘的说明，不是入口。
  const openFolio = page.getByRole('link', { name: /Access Folio/ })
  await expect(openFolio, 'Inspector 上没有能点开卷宗的入口').toHaveCount(1)
  await openFolio.click()

  await expect(page).toHaveURL(/\/gate\/folio\//)

  // 三、学习类的决定只在卷宗里给出，缝边的快捷键做不了它。
  // 精确匹配按钮名：「始终要求确认」那一条的说明里也有「自动允许」四个字，
  // 松一点的正则会同时选中它们两个。
  const learn = page.getByRole('button', { name: '今后在此项目自动允许', exact: true })
  await expect(learn, '卷宗里没有「今后在此项目自动允许」').toHaveCount(1)
  await learn.click()

  // 决定真的送出去了：产生了一条记忆，缝前不再有人等。
  await expect
    .poll(async () => {
      const memories = await api.get<ListOf<{ id: string }>>('/v1/trust-memories')
      return memories.body.items.length
    }, { message: '点完之后没有产生授权记忆 —— 这个决定没有送出去' })
    .toBe(1)

  const again = await session.call('github.issue.create', { ...repository, title: '第二次' })
  expect(again.refused, '学过之后同范围的调用仍然被拦下').toBe(false)
})

/*
 * 1024 这一档：Inspector 收成右侧抽屉（REQ-UI-004）。
 *
 * 单列一条而不是把上面那条改宽：抽屉是**另一种到达方式**（点卡片先把它拉出来），
 * 而 R-45 最可能就是在这一档上观察到的 —— 抽屉没拉开时，卷宗入口确实看不见。
 * 看不见与到不了是两件事，这条用例分开的正是它们。
 */
test('只用鼠标：1024 上 Inspector 收成抽屉，卷宗入口仍然点得到', async ({ api, agent, page }) => {
  await page.setViewportSize({ width: 1024, height: 768 })

  await connect(api, { ...gitHubIdentity, accountLabel: 'work' })
  const session = await agent()
  await session.confirm()

  await openConsole(page, '/gate')
  const refused = await session.call('github.issue.create', { ...repository, title: '窄窗口' })
  expect(refused.refused).toBe(true)

  const card = page.locator('button[aria-pressed]')
  await expect(card).toHaveCount(1)
  await card.first().click()

  const openFolio = page.getByRole('link', { name: /Access Folio/ })
  await expect(openFolio, '1024 上点开卡片之后仍然找不到卷宗入口').toBeVisible()
  await openFolio.click()

  await expect(page).toHaveURL(/\/gate\/folio\//)
  await expect(page.getByRole('button', { name: '今后在此项目自动允许', exact: true })).toBeVisible()
})
