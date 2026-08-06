import { test, expect } from '../harness/fixtures.js'
import { connect, openConsole } from '../harness/prepare.js'

/*
 * 列表不得带着浏览器默认的缩进（TASK-0751）。
 *
 * `<ul>`/`<ol>` 自带 `padding-inline-start: 40px`，而 reset 里原先只清了 margin。
 * 后果不是「缩进了一点」：Ledger 的脊线是绝对定位在时间轴上的，条目整体右移
 * 40px 而脊线不动，Agent 名就压在脊线上 —— 而脊线是缝的纵向投影，
 * 它落在哪里是这一页的空间常量（REQ-UI-002）。
 *
 * 这条不能只测 Ledger：下一个写 `<ul>` 的人不会知道有这回事，而 reset 是唯一
 * 能让他不必知道的地方。
 */

const PAGES = ['/gate', '/identities', '/automation', '/ledger', '/preferences'] as const

test('五个主页面上没有一个列表带着浏览器的默认缩进', async ({ api, agent, page }) => {
  // 先让每一页都有内容：空列表也能通过的用例等于没测。
  await connect(api, { service: 'github', item: 'GitHub Bot', field: 'token', accountLabel: 'work' })
  const session = await agent({ name: 'keychain-demo' })
  await session.confirm()
  await session.call('github.repository.read', { owner: 'octocat', repo: 'hello-world' })

  for (const path of PAGES) {
    await openConsole(page, path)
    const lists = await page.evaluate(() =>
      Array.from(document.querySelectorAll('ul,ol')).map((node) => ({
        cls: node.className.toString(),
        padding: getComputedStyle(node).paddingInlineStart,
      })),
    )
    const indented = lists.filter((list) => list.padding !== '0px')
    expect(indented, `${path} 上有列表带着浏览器的默认缩进：${JSON.stringify(indented)}`).toEqual([])
  }
})

test('账本左列的文字不压在脊线上', async ({ api, agent, page }) => {
  await connect(api, { service: 'github', item: 'GitHub Bot', field: 'token', accountLabel: 'work' })
  const session = await agent({ name: 'keychain-demo' })
  await session.call('github.repository.read', { owner: 'octocat', repo: 'hello-world' })

  // 三档都测：1024 那一档脊线与左列一起左移，两者是分别写死的两个数字，
  // 只改一个就会在那一档上重新压上（LedgerPage.module.css 的两处 132px）。
  for (const width of [1440, 1280, 1024]) {
    await page.setViewportSize({ width, height: 900 })
    await openConsole(page, '/ledger')

    const measured = await page.evaluate(() => {
      const spine = document.querySelector('[class*="spine"]')
      const agentName = document.querySelector('[class*="who"] [class*="agent"]')
      if (spine === null || agentName?.firstChild == null) return null
      const range = document.createRange()
      range.selectNodeContents(agentName.firstChild)
      return {
        spine: spine.getBoundingClientRect().left,
        textRight: range.getBoundingClientRect().right,
        text: agentName.textContent ?? '',
      }
    })

    expect(measured, `宽 ${String(width)} 上量不到脊线或 Agent 名`).not.toBeNull()
    if (measured === null) continue
    expect(measured.text, '左列是空的，这一轮什么也没量到').not.toBe('')
    expect(
      measured.spine - measured.textRight,
      `宽 ${String(width)}：Agent 名「${measured.text}」的右缘在 ${String(measured.textRight)}，` +
        `脊线在 ${String(measured.spine)} —— 文字压在脊线上了`,
    ).toBeGreaterThan(0)
  }
})
