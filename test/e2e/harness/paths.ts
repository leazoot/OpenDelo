import { fileURLToPath } from 'node:url'
import path from 'node:path'

/*
 * 仓库内的固定位置。
 *
 * 从本文件出发向上解析，而不是依赖 process.cwd()：Playwright 的 worker
 * 与 globalSetup 的工作目录不一定相同，用 cwd 会在其中一处静默指错。
 */

const here = path.dirname(fileURLToPath(import.meta.url))

/** e2eDir 是本 E2E 工程的根。 */
export const e2eDir = path.resolve(here, '..')

/** repoRoot 是 OpenDelo 仓库根目录。 */
export const repoRoot = path.resolve(e2eDir, '..', '..')

/**
 * binaryPath 是 E2E 专用二进制。
 *
 * 它带 `-tags e2e` 构建，因此出站地址可以被指向本地假服务；分发出去的那份
 * 构建没有这条路（`internal/cli/outbound_release.go`）。名字里带 `-e2e`
 * 是为了任何人在 `bin/` 里看到它时立刻知道这不是发布产物。
 */
export const binaryPath = path.join(repoRoot, 'bin', 'opendelo-e2e')
