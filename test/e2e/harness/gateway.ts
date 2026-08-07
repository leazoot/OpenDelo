import { spawn, type ChildProcessByStdio } from 'node:child_process'
import type { Readable } from 'node:stream'
import fs from 'node:fs/promises'
import os from 'node:os'
import path from 'node:path'
import { binaryPath } from './paths.js'
import { reservePorts } from './ports.js'
import { installFakeOnePassword } from './onepassword.js'
import type { ExternalServices } from './fakes.js'

/*
 * 启动一个隔离的真实 Gateway。
 *
 * 「隔离」指三件事，缺一条都会让并行的用例互相影响：
 *
 *   1. **独立配置目录**（`--config-dir`，权限 0700）。数据库、会话令牌、
 *      保险库都在它下面，用完整个删掉。**绝不碰用户的真实数据目录**
 *      （`.claude/rules/database.md` §15.1）。
 *   2. **独立端口**。三个接入面各要一个。
 *   3. **独立的假外部服务**。出站地址经环境变量交给 `-tags e2e` 的构建。
 *
 * 进程是真的：同一个 main、同一条装配链、同一套决策与审计。E2E 与生产之间
 * 只差出站地址（见 `internal/cli/outbound_release.go` 的说明）。
 */

/** startupTimeout 是等 Gateway 开始服务的上限。 */
const startupTimeout = 20_000

/** shutdownTimeout 是等进程收到 SIGINT 后退出的上限。 */
const shutdownTimeout = 10_000

/** Gateway 是一个正在运行的实例。 */
export interface Gateway {
  /** consoleURL 是 Console 与 Web API 的地址。 */
  readonly consoleURL: string
  /** proxyURL 是 Agent Proxy 的地址（8788 的对应物）。 */
  readonly proxyURL: string
  /** mcpURL 是 MCP over HTTP 的地址（8789 的对应物）。 */
  readonly mcpURL: string
  /** configDir 是本实例独占的配置目录。 */
  readonly configDir: string
  /** sessionToken 是 Console 访问 /v1 的令牌。 */
  readonly sessionToken: string
  /** output 是进程到目前为止的 stdout 与 stderr，失败时用来说明原因。 */
  output(): string
  /** stop 发 SIGINT 并等待优雅关闭，返回退出码。 */
  stop(): Promise<number | null>
  /**
   * run 用 `opendelo run` 启动一个子进程，返回它的输出与退出码。
   *
   * 这是「环境变量面」唯一如实的观察方式：会话凭证与代理设置正是由 run
   * 放进子进程环境的，别处看到的都不是 Agent 真正拿到的那一份。
   */
  run(command: readonly string[]): Promise<{ code: number | null; output: string }>
}

/** GatewayOptions 是 startGateway 的输入。 */
export interface GatewayOptions {
  /** External 是本实例要指向的假外部服务。 */
  readonly external: ExternalServices
  /** LogLevel 默认 debug —— E2E 失败时进程日志是唯一的现场。 */
  readonly logLevel?: string
}

/*
 * startGateway 建好隔离目录并启动进程，直到三个面都在服务后才返回。
 *
 * 端口撞了就重来。分配是「先占住、再放掉、然后启动」——`ports.ts` 的注释写着
 * 释放与启动之间那个窗口消不掉，而 CI 上并行跑的时候它会被撞中：
 * 2026-08-07 有一轮就是 `bind: address already in use`（agent-proxy）。
 * 撞中是小概率且完全无害的，重来一次即可；把它当成失败会让一整轮 CI 白跑，
 * 而那一轮里真正的问题就被这条噪声盖住了。
 */
export async function startGateway(options: GatewayOptions): Promise<Gateway> {
  const attempts = 5
  for (let attempt = 1; ; attempt++) {
    try {
      return await startOnce(options)
    } catch (cause) {
      if (attempt >= attempts || !isPortCollision(cause)) {
        throw cause
      }
    }
  }
}

/** isPortCollision 认出「端口被别人占了」这一种失败，其余一律照原样抛出。 */
function isPortCollision(cause: unknown): boolean {
  return /address already in use|EADDRINUSE/.test(String(cause))
}

async function startOnce(options: GatewayOptions): Promise<Gateway> {
  await assertBinaryBuilt()

  // 假 op 与配置目录分开放。放在一起的话哨兵扫描会在「数据目录」这一面上
  // 扫到假 op 脚本里那几个哨兵字面量 —— 那是夹具自己的东西，不是产品泄漏的。
  const workDir = await fs.mkdtemp(path.join(os.tmpdir(), 'opendelo-e2e-'))
  const configDir = path.join(workDir, 'config')
  await fs.mkdir(configDir, { mode: 0o700 })
  await fs.chmod(configDir, 0o700)

  const [webPort, proxyPort, mcpPort] = await reservePorts(3)
  if (webPort === undefined || proxyPort === undefined || mcpPort === undefined) {
    throw new Error('没能分配到三个空闲端口')
  }

  const fakeBinDir = await installFakeOnePassword(workDir)

  // 走的是用户第一次装完时的同一条路：先 init 建目录与令牌，再 start。
  // 直接 start 会因为数据目录不存在而失败 —— 那正是产品要求的顺序。
  await initialize(configDir)

  // 端口写进配置文件而不是只用命令行参数：`opendelo run` 从配置里读地址，
  // 只给 start 传参数的话它会去拨默认的 8787。
  await fs.writeFile(
    path.join(configDir, 'config.json'),
    JSON.stringify({
      listen_address: '127.0.0.1',
      web_api_port: webPort,
      agent_proxy_port: proxyPort,
      mcp_port: mcpPort,
      log_level: options.logLevel ?? 'debug',
    }),
    { mode: 0o600 },
  )

  const child = spawn(
    binaryPath,
    [
      'start',
      '--config-dir',
      configDir,
      '--web-api-port',
      String(webPort),
      '--agent-proxy-port',
      String(proxyPort),
      '--mcp-port',
      String(mcpPort),
      '--log-level',
      options.logLevel ?? 'debug',
    ],
    {
      // PATH 前置假 op；其余变量不继承用户环境里可能存在的真实凭据。
      env: {
        PATH: `${fakeBinDir}${path.delimiter}${process.env['PATH'] ?? ''}`,
        HOME: configDir,
        OPENDELO_E2E_GITHUB_BASE_URL: options.external.github.baseURL,
        OPENDELO_E2E_CLOUDFLARE_BASE_URL: options.external.cloudflare.baseURL,
        OPENDELO_E2E_OPENAI_BASE_URL: options.external.openai.baseURL,
        OPENDELO_E2E_ANTHROPIC_BASE_URL: options.external.anthropic.baseURL,
      },
      stdio: ['ignore', 'pipe', 'pipe'],
    },
  )

  const transcript = record(child)
  const exited = waitForExit(child)

  const consoleURL = `http://127.0.0.1:${String(webPort)}`
  try {
    await waitUntilServing(consoleURL, transcript, exited)
  } catch (cause) {
    child.kill('SIGKILL')
    await fs.rm(workDir, { recursive: true, force: true })
    throw cause
  }

  const sessionToken = (await fs.readFile(path.join(configDir, 'session_token'), 'utf8')).trim()

  return {
    consoleURL,
    proxyURL: `http://127.0.0.1:${String(proxyPort)}`,
    mcpURL: `http://127.0.0.1:${String(mcpPort)}`,
    configDir,
    sessionToken,
    output: () => transcript.text(),

    run: (command) =>
      new Promise((resolve, reject) => {
        const child = spawn(
          binaryPath,
          ['run', '--config-dir', configDir, '--', ...command],
          {
            env: {
              PATH: `${fakeBinDir}${path.delimiter}${process.env['PATH'] ?? ''}`,
              HOME: configDir,
            },
            stdio: ['ignore', 'pipe', 'pipe'],
          },
        )
        const said = record(child)
        child.once('error', reject)
        child.once('exit', (code) => resolve({ code, output: said.text() }))
      }),

    stop: async () => {
      if (child.exitCode === null && child.signalCode === null) {
        child.kill('SIGINT')
      }
      const code = await Promise.race([
        exited,
        delay(shutdownTimeout).then(() => {
          child.kill('SIGKILL')
          return null
        }),
      ])
      await fs.rm(workDir, { recursive: true, force: true })
      return code
    },
  }
}

/** initialize 跑一次 `opendelo init`，把配置目录建成可启动的样子。 */
function initialize(configDir: string): Promise<void> {
  return new Promise((resolve, reject) => {
    const child = spawn(binaryPath, ['init', '--config-dir', configDir], {
      env: { HOME: configDir },
      stdio: ['ignore', 'pipe', 'pipe'],
    })
    const said = record(child)
    child.once('exit', (code) => {
      if (code === 0) {
        resolve()
        return
      }
      reject(new Error(`opendelo init 以退出码 ${String(code)} 结束：\n${said.text()}`))
    })
  })
}

async function assertBinaryBuilt(): Promise<void> {
  try {
    await fs.access(binaryPath)
  } catch {
    throw new Error(
      `找不到 ${binaryPath}。E2E 跑的是真实二进制，先构建它：make e2e-binary`,
    )
  }
}

interface Transcript {
  text(): string
}

/** Spawned 是本文件启动的进程形状：stdin 关掉，stdout 与 stderr 是管道。 */
type Spawned = ChildProcessByStdio<null, Readable, Readable>

function record(child: Spawned): Transcript {
  const chunks: string[] = []
  child.stdout.setEncoding('utf8')
  child.stderr.setEncoding('utf8')
  child.stdout.on('data', (chunk: string) => chunks.push(chunk))
  child.stderr.on('data', (chunk: string) => chunks.push(chunk))
  return { text: () => chunks.join('') }
}

function waitForExit(child: Spawned): Promise<number | null> {
  return new Promise((resolve) => {
    child.once('exit', (code) => resolve(code))
  })
}

/**
 * waitUntilServing 轮询 Console 入口，直到它返回 200。
 *
 * 同时盯着进程本身：起不来时立刻带着日志失败，而不是等满 20 秒再报一句超时。
 */
async function waitUntilServing(
  consoleURL: string,
  transcript: Transcript,
  exited: Promise<number | null>,
): Promise<void> {
  let alive = true
  void exited.then(() => {
    alive = false
  })

  const deadline = Date.now() + startupTimeout
  while (Date.now() < deadline) {
    if (!alive) {
      throw new Error(`Gateway 启动后立刻退出了：\n${transcript.text()}`)
    }
    try {
      const response = await fetch(consoleURL, { redirect: 'manual' })
      if (response.ok) {
        return
      }
    } catch {
      // 还没开始监听。继续轮询，超时由 deadline 兜住。
    }
    await delay(50)
  }
  throw new Error(`Gateway 在 ${String(startupTimeout)}ms 内没有开始服务：\n${transcript.text()}`)
}

function delay(milliseconds: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, milliseconds))
}
