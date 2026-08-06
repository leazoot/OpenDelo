import { defineConfig, devices } from '@playwright/test'

/*
 * E2E 配置。
 *
 * 两条与别处不同的取舍：
 *
 *   - **没有 webServer。** 每个用例自己起一个隔离的 Gateway（harness/fixtures.ts），
 *     共享一个服务端就等于共享一个数据库，用例之间会串。
 *   - **不重试。** `.claude/rules/testing.md` §11 禁止用 retry 掩盖 flaky：
 *     偶发失败要定位真实原因，不是多跑两遍。
 */

export default defineConfig({
  testDir: './specs',
  globalSetup: './harness/globalsetup.ts',
  fullyParallel: true,
  forbidOnly: true,
  retries: 0,
  // CI 上收紧并发：每个用例都是一个真实进程加四个假服务，机器越小越容易
  // 在启动那一刻互相抢资源，而那种失败会被误读成产品的不稳定。
  ...(process.env['CI'] === undefined ? {} : { workers: 2 }),
  reporter: process.env['CI'] === undefined ? [['list']] : [['list'], ['html', { open: 'never' }]],
  timeout: 60_000,
  expect: { timeout: 10_000 },

  use: {
    // 基地址由 fixture 按实例端口给出，这里只定公共行为。
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
    video: 'off',
  },

  /*
   * REQ-NFR-003 的四种浏览器。
   *
   * **整套用例只在 chromium 上跑一遍**，另外三个只跑兼容性那一份：其余用例问的是
   * 产品的行为（决策、Lease、审计），那些答案与浏览器无关，跑四遍只是把
   * 每次 CI 的时间乘以四。浏览器之间真正会不一样的是渲染与浏览器 API，
   * 那正是 compatibility.spec.ts 覆盖的。
   *
   * Edge 单列一个 project 而不是复用 chromium：它与 Chrome 同一个引擎，但装的是
   * 系统上那一份，版本与打包进 Playwright 的这份并不一致。默认不跑（要先
   * `playwright install msedge` 把它装到系统里，那是个侵入性动作），
   * 由 `make e2e-edge` 显式触发，CI 在 Linux 上跑它。
   */
  projects: [
    { name: 'chromium', use: { ...devices['Desktop Chrome'] } },
    {
      name: 'firefox',
      use: { ...devices['Desktop Firefox'] },
      testMatch: /compatibility\.spec\.ts/,
    },
    {
      name: 'webkit',
      use: { ...devices['Desktop Safari'] },
      testMatch: /compatibility\.spec\.ts/,
    },
    {
      name: 'msedge',
      use: { ...devices['Desktop Edge'], channel: 'msedge' },
      testMatch: /compatibility\.spec\.ts/,
    },
  ],
})
