import { test as base } from '@playwright/test'
import { startExternalServices, type ExternalServices } from './fakes.js'
import { startGateway, type Gateway } from './gateway.js'
import { webAPI, type WebAPI } from './api.js'
import { registerAgent, type AgentIdentity, type AgentSession } from './agent.js'

/*
 * 每个用例一套完整的世界。
 *
 * Playwright 的 fixture 保证了拆卸一定会跑（包括用例失败与超时），所以
 * 「独立数据目录」不是靠每个用例记得自己清理，而是结构上就成立。
 *
 * 顺序是有意的：假服务先起来，Gateway 才知道该往哪里出站。
 */

interface Isolated {
  /** external 是这个用例专属的假外部服务。 */
  external: ExternalServices
  /** gateway 是这个用例专属的 Gateway 进程。 */
  gateway: Gateway
  /** api 是绑好会话令牌的 Web API 客户端。 */
  api: WebAPI
  /** agent 注册一个 Agent 会话。多次调用得到互相独立的会话。 */
  agent: (identity?: AgentIdentity) => Promise<AgentSession>
}

export const test = base.extend<Isolated>({
  external: async ({}, use) => {
    const services = await startExternalServices()
    await use(services)
    await services.close()
  },

  gateway: async ({ external }, use) => {
    const gateway = await startGateway({ external })
    await use(gateway)

    // 退出码不是 0 意味着关闭路径上有东西没释放，那属于缺陷而不是噪音。
    const code = await gateway.stop()
    if (code !== 0) {
      throw new Error(`Gateway 以退出码 ${String(code)} 结束：\n${gateway.output()}`)
    }
  },

  api: async ({ gateway }, use) => {
    await use(webAPI(gateway))
  },

  agent: async ({ gateway }, use) => {
    await use((identity) => registerAgent(gateway, identity))
  },

  // Console 由被测 Gateway 自己提供，因此基地址只能在实例起来之后才知道。
  baseURL: async ({ gateway }, use) => {
    await use(gateway.consoleURL)
  },
})

export { expect } from '@playwright/test'
