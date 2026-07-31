/*
 * 折回缝前时带的路由状态。
 *
 * 卷宗是一条路由而不是弹窗（REQ-APPROVAL-004），折回时上一页整棵重建，焦点
 * 因此掉回 `<body>` —— 键盘用户要从顶栏一路 Tab 才能回到刚才那一条请求
 *
 * 用路由状态而不是全局 store：它只跟着这一次导航走。刷新页面或直接输入
 * /gate 都不带这个标记，Gate 也就不会莫名其妙地把焦点抢过去。
 */

export const FOLD_STATE: { readonly restoreFocus: true } = { restoreFocus: true }

/** 这次导航是不是从卷宗折回来的。认不出的状态一律当作不是。 */
export function wantsFocusBack(state: unknown): boolean {
  return typeof state === 'object' && state !== null && 'restoreFocus' in state && state.restoreFocus === true
}
