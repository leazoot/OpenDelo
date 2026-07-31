import type { ReactElement } from 'react'
import { matchPath, Navigate, type RouteObject } from 'react-router'

import { NarrowGuard, type GuardedArea } from '../components/NarrowGuard'
import type { Copy } from '../i18n/copy'
import { AutomationPage } from '../pages/automation/AutomationPage'
import { FolioPage } from '../pages/folio/FolioPage'
import { GatePage } from '../pages/gate/GatePage'
import { IdentitiesPage } from '../pages/identities/IdentitiesPage'
import { LedgerPage } from '../pages/ledger/LedgerPage'
import { ManuscriptPage } from '../pages/manuscript/ManuscriptPage'
import { NotFoundPage } from '../pages/NotFoundPage'
import { PreferencesPage } from '../pages/preferences/PreferencesPage'

import { AppLayout } from './AppLayout'

/*
 * 七条路由（REQ-UI-001）。
 *
 * 路径与标题写在同一张表里：标签标题要按当前页面变化，而把「有哪些页面」抄成
 * 两份的结果一定是加了一条路由却忘了给它标题。
 */

interface Page {
  readonly path: string
  readonly title: (copy: Copy) => string
  readonly element: ReactElement
  /**
   * 小于 1024 时拦下这条路由（PRD §24、REQ-UI-004 AC3）。
   *
   * 写在路由表上而不是各自页面里：「窄屏上还剩哪几页」是一个整体判断，
   * 抄成七份的结果一定是加了一页却忘了它属于哪一边。
   */
  readonly guarded?: GuardedArea
}

export const PAGES: readonly Page[] = [
  { path: '/gate', title: (copy) => copy.pageGate, element: <GatePage /> },
  { path: '/gate/folio/:id', title: (copy) => copy.pageFolio, element: <FolioPage /> },
  {
    path: '/identities',
    title: (copy) => copy.pageIdentities,
    element: <IdentitiesPage />,
    guarded: 'identities',
  },
  {
    path: '/automation',
    title: (copy) => copy.pageAutomation,
    element: <AutomationPage />,
    guarded: 'automation',
  },
  {
    path: '/automation/advanced/:ruleId',
    title: (copy) => copy.pageManuscript,
    element: <ManuscriptPage />,
    guarded: 'automation',
  },
  { path: '/ledger', title: (copy) => copy.pageLedger, element: <LedgerPage /> },
  {
    path: '/preferences',
    title: (copy) => copy.pagePreferences,
    element: <PreferencesPage />,
    guarded: 'preferences',
  },
]

export const routes: RouteObject[] = [
  {
    path: '/',
    element: <AppLayout />,
    children: [
      // 根路径落到 Gate：这是产品的主界面，也是唯一有等待中的人的地方。
      { index: true, element: <Navigate to="/gate" replace /> },
      ...PAGES.map(({ path, element, guarded }) => ({ path, element: guard(element, guarded) })),
      { path: '*', element: <NotFoundPage /> },
    ],
  },
]

function guard(element: ReactElement, area: GuardedArea | undefined): ReactElement {
  return area === undefined ? element : <NarrowGuard area={area}>{element}</NarrowGuard>
}

/** 当前路径对应的页面标题；认不出的路径用「未找到」那一页的标题。 */
export function pageTitleOf(pathname: string, copy: Copy): string {
  const page = PAGES.find(({ path }) => matchPath(path, pathname) !== null)
  return page === undefined ? copy.pageNotFound : page.title(copy)
}
