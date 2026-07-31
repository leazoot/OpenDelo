import { useMutation } from '@tanstack/react-query'

import { GatewayError, requestGateway, type GatewayRequestOptions } from './gateway'

/*
 * 本地保险库的解锁（REQ-CRED-004、REQ-APPROVAL-005，用户决定 D-14 方案 C）。
 *
 * 主密码由 Gateway 校验，Console 只是把它送过去 —— 界面自己判断「密码对不对」
 * 等于把强认证放在一个 Agent 也能改的地方。
 *
 * 这里**没有读取保险库状态的 hook**：解没解锁只有 Gateway 说了算，
 * 而它在决定放行的那一刻自己会看（后端 `strongAuthCompleted`）。
 * 界面记住的只是「这一次解锁请求成功过」。
 */

/** 连续失败三次后的锁定（REQ-APPROVAL-005 AC2）。 */
export const LOCKOUT_CODE = 'provider_locked_timeout'

export interface UnlockInput {
  readonly masterPassword: string
}

export async function unlockVault(
  input: UnlockInput,
  options: GatewayRequestOptions = {},
): Promise<void> {
  await requestGateway('/v1/vault/unlock', {
    ...options,
    method: 'POST',
    body: { master_password: input.masterPassword },
  })
}

export interface UseUnlockOptions {
  /** 覆盖请求实现，只在测试里用。 */
  readonly unlock?: (input: UnlockInput) => Promise<void>
}

export interface UnlockView {
  readonly unlock: (input: UnlockInput) => void
  readonly isUnlocked: boolean
  readonly isPending: boolean
  /** 失败的错误码；没有失败过时为空串。 */
  readonly failureCode: string
}

/**
 * 提交一次主密码。
 *
 * 失败的原因只分两种对外可说的：锁定与其它。**不区分「密码错误」与
 * 「保险库不存在」**—— 后端也不区分，
 * 界面自然没有那份信息可以显示。
 */
export function useUnlockVault(options: UseUnlockOptions = {}): UnlockView {
  const send = options.unlock ?? ((input: UnlockInput) => unlockVault(input))
  const mutation = useMutation({ mutationFn: send })

  return {
    unlock: (input: UnlockInput) => {
      if (mutation.isPending) {
        return
      }
      mutation.mutate(input)
    },
    isUnlocked: mutation.isSuccess,
    isPending: mutation.isPending,
    failureCode: mutation.error instanceof GatewayError ? mutation.error.code : '',
  }
}

export interface CreateVaultView {
  readonly create: (input: UnlockInput) => void
  readonly isCreated: boolean
  readonly isPending: boolean
  readonly failureCode: string
}

export interface UseCreateVaultOptions {
  /** 覆盖请求实现，只在测试里用。 */
  readonly create?: (input: UnlockInput) => Promise<void>
}

/**
 * 建立本地保险库并设定主密码（REQ-CRED-004 §2，用户决定 D-15）。
 *
 * 已存在时后端拒绝且不覆盖 —— 界面因此不需要、也不该先问一句「已经有了吗」：
 * 那个答案本身就是「这台机器上有没有保险库」，而那正是解锁路径拒绝泄漏的东西。
 */
export async function createVault(
  input: UnlockInput,
  options: GatewayRequestOptions = {},
): Promise<void> {
  await requestGateway('/v1/vault', {
    ...options,
    method: 'POST',
    body: { master_password: input.masterPassword },
  })
}

export function useCreateVault(options: UseCreateVaultOptions = {}): CreateVaultView {
  const send = options.create ?? ((input: UnlockInput) => createVault(input))
  const mutation = useMutation({ mutationFn: send })

  return {
    create: (input: UnlockInput) => {
      if (mutation.isPending) {
        return
      }
      mutation.mutate(input)
    },
    isCreated: mutation.isSuccess,
    isPending: mutation.isPending,
    failureCode: mutation.error instanceof GatewayError ? mutation.error.code : '',
  }
}

/** 主密码的最短长度，与后端 minMasterPasswordLength 一致。 */
export const MIN_MASTER_PASSWORD = 12
