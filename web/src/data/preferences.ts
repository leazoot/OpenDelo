import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { z } from 'zod'

import { requestGateway, type GatewayRequestOptions } from './gateway'

/*
 * 偏好（`GET /v1/preferences`，REQ-PREF-001）。
 *
 * 运行期偏好与需要重启的设置在响应里是分开的两层：混成一层，用户就无从知道
 * 自己刚才那次修改是立刻生效了还是要重启（后端 PreferencesView 的注释）。
 */

/** 改完必须重启才生效的那些（REQ-PREF-001 AC2）。 */
export const restartRequiredSchema = z.object({
  listen_address: z.string(),
  web_api_port: z.number().int(),
  mcp_port: z.number().int(),
  proxy_port: z.number().int(),
})

export const preferencesSchema = z.object({
  automation_mode: z.string(),
  approval_timeout_seconds: z.number().int(),
  read_only_auto_allow: z.boolean(),
  theme: z.string(),
  language: z.string(),
  restart_required: restartRequiredSchema,
  /** 读配置时认不出的项：用默认值继续，但要说出来（REQ-PREF-001 AC3）。 */
  warnings: z.array(z.string()),
})

export type Preferences = z.infer<typeof preferencesSchema>

export const PREFERENCES_KEY = ['preferences'] as const

export async function fetchPreferences(options: GatewayRequestOptions = {}): Promise<Preferences> {
  return preferencesSchema.parse(await requestGateway('/v1/preferences', options))
}

export interface PreferencesView {
  readonly preferences: Preferences | null
  readonly isLoading: boolean
  readonly isError: boolean
}

export interface UsePreferencesOptions {
  /** 覆盖请求实现，只在测试里用。 */
  readonly request?: (options: GatewayRequestOptions) => Promise<Preferences>
}

/**
 * 当前偏好。
 *
 * 拉不回来时给 null 而不是一份默认值：**「现在是哪种自动化模式」答错了，
 * 页面会理直气壮地告诉用户一套并不生效的规则**。
 */
export function usePreferences(options: UsePreferencesOptions = {}): PreferencesView {
  const request = options.request ?? fetchPreferences

  const query = useQuery({
    queryKey: PREFERENCES_KEY,
    queryFn: ({ signal }) => request({ signal }),
    retry: false,
  })

  return { preferences: query.data ?? null, isLoading: query.isPending, isError: query.isError }
}

export interface SavePreferencesView {
  readonly save: (changes: Readonly<Record<string, string>>) => void
  readonly isPending: boolean
  readonly isError: boolean
}

export interface UseSavePreferencesOptions {
  /** 覆盖请求实现，只在测试里用。 */
  readonly save?: (changes: Readonly<Record<string, string>>) => Promise<void>
}

/**
 * 改一项偏好。
 *
 * 只送改动的那一项：整份回写会把另一个窗口刚改过的项覆盖掉。
 * 认不出的键名会被后端拒绝，而不是悄悄忽略。
 */
export async function savePreferences(
  changes: Readonly<Record<string, string>>,
  options: GatewayRequestOptions = {},
): Promise<void> {
  await requestGateway('/v1/preferences', {
    ...options,
    method: 'PATCH',
    body: { preferences: changes },
  })
}

export function useSavePreferences(options: UseSavePreferencesOptions = {}): SavePreferencesView {
  const client = useQueryClient()
  const send = options.save ?? ((changes: Readonly<Record<string, string>>) => savePreferences(changes))
  const mutation = useMutation({
    mutationFn: send,
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: PREFERENCES_KEY })
    },
  })

  return {
    save: (changes: Readonly<Record<string, string>>) => {
      if (mutation.isPending) {
        return
      }
      mutation.mutate(changes)
    },
    isPending: mutation.isPending,
    isError: mutation.isError,
  }
}
