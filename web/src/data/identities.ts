import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { z } from 'zod'

import { GatewayError, requestGateway, type GatewayRequestOptions } from './gateway'

/*
 * 身份（`GET /v1/identities`，REQ-API-001）。
 *
 * 决策里只有 identity_id。审批页要回答的是「用谁的身份去做这件事」，
 * 而一串 ULID 回答不了。
 *
 * 这里没有任何字段能表达凭据：响应给的是 credential_reference_id，
 * 一个指针而不是内容，本模块连它都不取。
 */

export const identitySchema = z.object({
  id: z.string().min(1),
  service: z.string(),
  account_label: z.string(),
  environment: z.string(),
  is_default: z.boolean(),
  status: z.string(),
})

export const identityListSchema = z.object({
  items: z.array(identitySchema),
  // 已声明 Adapter 的服务。它不是身份的属性，而是这台 Gateway 的能力 ——
  // 连接表单据此把「服务」做成下拉而不是让用户猜着填。
  connectable_services: z.array(z.string()).default([]),
})

export type Identity = z.infer<typeof identitySchema>

export const IDENTITIES_KEY = ['identities'] as const

export interface IdentityRoster {
  readonly identities: readonly Identity[]
  readonly connectableServices: readonly string[]
}

export async function fetchIdentities(options: GatewayRequestOptions = {}): Promise<IdentityRoster> {
  const parsed = identityListSchema.parse(await requestGateway('/v1/identities', options))
  return { identities: parsed.items, connectableServices: parsed.connectable_services }
}

/** 一条身份的可读写法：账号标签 + 环境。 */
export function describeIdentity(identity: Identity): string {
  return identity.environment === '' ? identity.account_label : `${identity.account_label} · ${identity.environment}`
}

export interface IdentitiesView {
  readonly identities: readonly Identity[]
  readonly connectableServices: readonly string[]
  readonly isLoading: boolean
  readonly isError: boolean
}

/**
 * 整份身份名册（Identities 页面的右列）。
 *
 * 与 useIdentityLabels 共用一个 query key —— 同一份数据拉两次，
 * 只会让两处在不同时刻各自过时。
 */
export function useIdentities(options: UseIdentitiesOptions = {}): IdentitiesView {
  const request = options.request ?? fetchIdentities

  const query = useQuery({
    queryKey: IDENTITIES_KEY,
    queryFn: ({ signal }) => request({ signal }),
    retry: false,
  })

  return {
    identities: query.data?.identities ?? [],
    connectableServices: query.data?.connectableServices ?? [],
    isLoading: query.isPending,
    isError: query.isError,
  }
}

export interface UseIdentitiesOptions {
  /** 覆盖请求实现，只在测试里用。 */
  readonly request?: (options: GatewayRequestOptions) => Promise<IdentityRoster>
}

/** 本期实现的三种凭据来源（REQ-CRED-006 AC1），顺序与后端的实现清单一致。 */
export const PROVIDER_KINDS = ['1password', 'macos-keychain', 'local-vault'] as const

export type ProviderKind = (typeof PROVIDER_KINDS)[number]

/**
 * 连接一个身份要填的东西（REQ-CRED-002 AC1）。
 *
 * 全是**坐标**：去哪个来源、取哪一项、取哪个字段。这里没有一个字段
 * 承载凭据本身，也不会有 —— 明文从不经过 Web API（REQ-CRED-001），
 * 而 Gateway 拒绝未知字段，多塞一个 token 进去只会得到 400。
 */
export interface ConnectDraft {
  readonly providerKind: ProviderKind
  readonly providerLabel: string
  readonly providerItemRef: string
  readonly field: string
  readonly service: string
  readonly accountLabel: string
  readonly environment: 'production' | 'non-production'
}

export async function connectIdentity(
  draft: ConnectDraft,
  options: GatewayRequestOptions = {},
): Promise<Identity> {
  const payload = await requestGateway('/v1/identities/connect', {
    ...options,
    method: 'POST',
    body: {
      provider_kind: draft.providerKind,
      provider_label: draft.providerLabel,
      provider_item_ref: draft.providerItemRef,
      field: draft.field,
      service: draft.service,
      account_label: draft.accountLabel,
      environment: draft.environment,
      is_default: false,
    },
  })
  return identitySchema.parse(payload)
}

export interface ConnectIdentityView {
  readonly connect: (draft: ConnectDraft) => void
  readonly isPending: boolean
  readonly isError: boolean
  /**
   * 失败的详情，认不出的异常为 null。
   *
   * 表单据此指出是哪一项不对，而不是只说一句「失败了」；为 null 时
   * 仍然有 isError 说明这次没成，两者不能互相替代。
   */
  readonly failure: GatewayError | null
  readonly reset: () => void
}

/**
 * 连接身份。
 *
 * 不做乐观更新：这一步会在服务端探测凭据来源，成功与否只有它知道。
 * 先把一条身份画上去再回滚，等于让「连上了」闪现一次 ——
 * 而这一页说的正是「哪些身份是真的连着的」。
 */
export function useConnectIdentity(options: UseConnectIdentityOptions = {}): ConnectIdentityView {
  const client = useQueryClient()
  const send = options.connect ?? ((draft: ConnectDraft) => connectIdentity(draft))

  const mutation = useMutation({
    mutationFn: send,
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: IDENTITIES_KEY })
    },
  })

  return {
    connect: (draft: ConnectDraft) => {
      mutation.mutate(draft)
    },
    isPending: mutation.isPending,
    isError: mutation.isError,
    // 只认 GatewayError。别的异常（网络中断、解析失败）在这一层说不出更多，
    // 由表单退回那句通用说明，而不是把原始文本铺到界面上。
    failure: mutation.error instanceof GatewayError ? mutation.error : null,
    reset: () => {
      mutation.reset()
    },
  }
}

export interface UseConnectIdentityOptions {
  /** 覆盖请求实现，只在测试里用。 */
  readonly connect?: (draft: ConnectDraft) => Promise<Identity>
}

/**
 * identity_id → 可读写法。
 *
 * 查不到的返回 undefined，由调用方决定退回显示什么 —— 在这里编一个
 * 「未知身份」会让「还没拉回来」与「这条身份已被断开」长成同一个样子。
 */
export function useIdentityLabels(options: UseIdentitiesOptions = {}): ReadonlyMap<string, string> {
  const request = options.request ?? fetchIdentities

  const query = useQuery({
    queryKey: IDENTITIES_KEY,
    queryFn: ({ signal }) => request({ signal }),
    retry: false,
  })

  return new Map((query.data?.identities ?? []).map((identity) => [identity.id, describeIdentity(identity)]))
}
