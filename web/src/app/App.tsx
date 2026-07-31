import { createBrowserRouter, RouterProvider } from 'react-router'

import { routes } from './routes'

/**
 * Console 的入口。
 *
 * 用真实的浏览器路由而不是内存路由：Access Folio 是路由态，后退键必须等价于 Esc
 * （REQ-APPROVAL-004）。Gateway 在提供静态资源时把认不出的路径回落到 index.html，
 * 因此直接输入 /identities 这类地址也能进得来。
 */
const router = createBrowserRouter(routes)

export function App() {
  return <RouterProvider router={router} />
}
