import { useQuery } from '@tanstack/react-query'
import { z } from 'zod'

import { requestGateway, type GatewayRequestOptions } from './gateway'

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

export const identityListSchema = z.object({ items: z.array(identitySchema) })

export type Identity = z.infer<typeof identitySchema>

export const IDENTITIES_KEY = ['identities'] as const

export async function fetchIdentities(options: GatewayRequestOptions = {}): Promise<Identity[]> {
  return identityListSchema.parse(await requestGateway('/v1/identities', options)).items
}

/** 一条身份的可读写法：账号标签 + 环境。 */
export function describeIdentity(identity: Identity): string {
  return identity.environment === '' ? identity.account_label : `${identity.account_label} · ${identity.environment}`
}

export interface IdentitiesView {
  readonly identities: readonly Identity[]
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

  return { identities: query.data ?? [], isLoading: query.isPending, isError: query.isError }
}

export interface UseIdentitiesOptions {
  /** 覆盖请求实现，只在测试里用。 */
  readonly request?: (options: GatewayRequestOptions) => Promise<Identity[]>
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

  return new Map((query.data ?? []).map((identity) => [identity.id, describeIdentity(identity)]))
}
