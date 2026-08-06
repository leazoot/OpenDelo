import { execFile } from 'node:child_process'
import { promisify } from 'node:util'
import { binaryPath, repoRoot } from './paths.js'

const run = promisify(execFile)

/*
 * 构建 E2E 用的二进制。
 *
 * 在 globalSetup 里做而不是交给调用者，是为了让 `pnpm test` 一条命令就能跑：
 * 一个「忘了重新构建」的 E2E 会拿上一次的代码给出结论，那比失败更糟。
 */

/**
 * buildBinary 构建带 `-tags e2e` 的二进制。
 *
 * Console 产物必须已经在 `web/embedded/dist`（go:embed 嵌的是构建那一刻的内容），
 * 由 Makefile 的 `e2e` target 保证先跑 `web-build`。
 */
export async function buildBinary(): Promise<void> {
  await run('go', ['build', '-tags', 'e2e', '-o', binaryPath, './cmd/opendelo'], {
    cwd: repoRoot,
  })
}
