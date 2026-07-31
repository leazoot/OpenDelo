import { QueryClient } from '@tanstack/react-query'

/**
 * 服务端数据的唯一来源。
 *
 * 每次调用返回一个新的 QueryClient：用例之间不共享缓存，否则上一个用例的
 * Gateway 状态会渗进下一个。
 */
export function createQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: {
        // 失败即失败。重试会推迟「已经连不上了」这个结论，而那正是要尽快说出口的事。
        retry: false,
        refetchOnWindowFocus: true,
      },
    },
  })
}
