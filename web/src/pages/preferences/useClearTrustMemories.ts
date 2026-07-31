import { useMutation, useQueryClient } from '@tanstack/react-query'

import { requestGateway } from '../../data/gateway'
import { TRUST_MEMORIES_KEY } from '../../data/trustMemories'

/*
 * 清除全部 Trust Memory（REQ-UI-007 AC3）。
 *
 * 没有批量端点，因此逐条 DELETE。**审计由后端在删除之前写下**
 * （`pipeline.ClearTrustMemory`）—— 界面写不了账本，也不该假装自己写了。
 *
 * 中途失败就停下并如实报数：已经清掉的那些不会回来，剩下的仍然生效。
 * 报一个「全部清除成功」比停下来更糟。
 */

export interface ClearView {
  readonly clear: (ids: readonly string[]) => void
  /** 这一次真的清掉了几条。 */
  readonly cleared: number
  readonly isPending: boolean
  readonly isError: boolean
}

export interface UseClearOptions {
  /** 覆盖请求实现，只在测试里用。 */
  readonly forget?: (id: string) => Promise<void>
}

export async function forgetMemory(id: string): Promise<void> {
  await requestGateway(`/v1/trust-memories/${encodeURIComponent(id)}`, { method: 'DELETE' })
}

export function useClearTrustMemories(options: UseClearOptions = {}): ClearView {
  const client = useQueryClient()
  const forget = options.forget ?? forgetMemory

  const mutation = useMutation({
    mutationFn: async (ids: readonly string[]) => {
      let done = 0
      for (const id of ids) {
        // 顺序删除而不是并发：一次失败之后，「已经清掉几条」必须是确定的。
        await forget(id)
        done += 1
      }
      return done
    },
    onSettled: () => {
      void client.invalidateQueries({ queryKey: TRUST_MEMORIES_KEY })
    },
  })

  return {
    clear: (ids: readonly string[]) => {
      if (mutation.isPending) {
        return
      }
      mutation.mutate(ids)
    },
    cleared: mutation.data ?? 0,
    isPending: mutation.isPending,
    isError: mutation.isError,
  }
}
