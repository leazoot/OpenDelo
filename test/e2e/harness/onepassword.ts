import fs from 'node:fs/promises'
import path from 'node:path'
import { sentinelAPIKey, sentinelPassword, sentinelToken } from './sentinel.js'

/*
 * 假的 1Password CLI。
 *
 * `internal/credential/onepassword` 从 PATH 上找一个叫 op 的可执行文件，
 * 用参数数组调用它（`.claude/rules/security.md` §7 禁止 `sh -c` 拼接）。
 * 把这个假的 op 放在 PATH 最前面，**正式构建的取用路径就被原封不动地跑到了**：
 * 真的 exec、真的参数、真的解析、真的 secret.Value 包装与清零。
 *
 * 这是本 E2E 里唯一的凭据来源，因此不必碰用户的钥匙串，也不必有 1Password 账号
 * —— 那两样在 CI 上都不存在，在本机上则属于「操作用户真实数据」。
 */

/** vaultName 是假 op 认得的唯一保险库。 */
export const vaultName = 'opendelo-e2e'

/**
 * fieldSecrets 是字段名到哨兵值的映射。
 *
 * 分成三个而不是共用一个：出站请求里出现的是哪一个哨兵，说明凭据是从哪个字段
 * 取的。串了的话「取错字段」这类缺陷在扫描里看不出来。
 */
export const fieldSecrets: Readonly<Record<string, string>> = {
  token: sentinelToken,
  api_key: sentinelAPIKey,
  password: sentinelPassword,
}

/**
 * installFakeOnePassword 在 dir 下装一个假的 op，返回要前置到 PATH 的目录。
 *
 * **只用 shell 内建命令。** `internal/credential/onepassword` 调用 op 时把
 * 子进程的环境清空（那是对的：本进程环境里可能有别的服务的令牌），于是
 * PATH 也没了 —— 任何 `#!/usr/bin/env node` 或调用外部程序的写法都会在
 * 那一刻以 127 失败，而症状是一句「1Password CLI 调用失败」。
 *
 * 权限必须是 0755：`registry.ResolveBinary` 拒绝执行任何组内或其他人可改写的
 * 文件，0777 会被它挡下。
 */
export async function installFakeOnePassword(dir: string): Promise<string> {
  const binDir = path.join(dir, 'fake-bin')
  await fs.mkdir(binDir, { recursive: true })

  const fieldArms = Object.entries(fieldSecrets)
    .map(([field, value]) => `  ${field}) printf '%s' '${value}' ;;`)
    .join('\n')

  const script = `#!/bin/sh
# 假的 1Password CLI。由 test/e2e/harness/onepassword.ts 生成，不要手工编辑。
# 只用 shell 内建命令：调用方会把环境清空，PATH 不存在。

if [ "$1" = "--version" ]; then
  echo "2.30.0-fake"
  exit 0
fi

if [ "$1" != "read" ]; then
  echo "假 op 只实现了 --version 与 read" >&2
  exit 1
fi

# 地址是最后一个参数（前面可能有 --no-newline 这类开关）。
for argument in "$@"; do uri="$argument"; done

case "$uri" in
  op://*/*/*) rest=\${uri#op://} ;;
  *) echo "[ERROR] invalid secret reference" >&2; exit 1 ;;
esac

vault=\${rest%%/*}
rest=\${rest#*/}
field=\${rest##*/}

if [ "$vault" != "${vaultName}" ]; then
  echo "[ERROR] vault not found" >&2
  exit 1
fi

case "$field" in
${fieldArms}
  *) echo "[ERROR] field not found in item" >&2; exit 1 ;;
esac
`

  const opPath = path.join(binDir, 'op')
  await fs.writeFile(opPath, script, { mode: 0o755 })
  await fs.chmod(opPath, 0o755)
  return binDir
}

/** itemRef 拼出一份引用的条目坐标（`op://<vault>/<item>`）。 */
export function itemRef(item: string): string {
  return `op://${vaultName}/${item}`
}
