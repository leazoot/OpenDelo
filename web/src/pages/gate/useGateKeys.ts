import { useEffect } from 'react'

/*
 * Gate 的键盘决策（REQ-APPROVAL-003）。
 *
 * `A` 允许 · `D` 拒绝 · `⇧A` 仅这一次 · `↵` 打开 Folio · `Esc` 折回。
 *
 * 高风险不在这里放行：这个 hook 只负责把按键翻成意图，「能不能」由 Inspector
 * 与决策引擎回答。把判断放进按键处理里，等于让快捷键有一套自己的规则。
 */

export type GateKey = 'allow' | 'allowOnce' | 'deny' | 'open' | 'dismiss'

/**
 * 一次按键对应的意图。认不出的按键返回 null。
 *
 * 带修饰键的一律不认（`⌘A` 是全选，`⌃D` 是终端里的另一件事）—— `⇧A` 除外，
 * 它就是「仅这一次」。
 */
export function intentOf(event: Pick<KeyboardEvent, 'key' | 'shiftKey' | 'metaKey' | 'ctrlKey' | 'altKey'>): GateKey | null {
  if (event.metaKey || event.ctrlKey || event.altKey) {
    return null
  }
  switch (event.key.toLowerCase()) {
    case 'a':
      return event.shiftKey ? 'allowOnce' : 'allow'
    case 'd':
      return event.shiftKey ? null : 'deny'
    case 'enter':
      return 'open'
    case 'escape':
      return 'dismiss'
    default:
      return null
  }
}

/**
 * 焦点在输入控件里时不接管按键。
 *
 * 否则在搜索框里打一个 a 就会放行一次请求。
 */
export function isTypingIn(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) {
    return false
  }
  if (target.isContentEditable) {
    return true
  }
  return ['input', 'textarea', 'select'].includes(target.tagName.toLowerCase())
}

/**
 * 焦点已经落在一个自己会响应回车的控件上。
 *
 * 按钮与链接的回车就是一次点击。全局处理器把它抢过来，键盘用户就 Tab 到卡片上
 * 按回车却什么也不会发生 —— 卡片还没被选中，「`↵` 打开 Folio」也就找不到
 * 要打开哪一条（REQ-UI-009 AC1）。字母键不在此列：`A` / `D` 是缝前的决定，
 * 焦点在哪个按钮上都算数。
 */
export function activatesOnEnter(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) {
    return false
  }
  const tag = target.tagName.toLowerCase()
  return tag === 'button' || (tag === 'a' && target.hasAttribute('href'))
}

export function useGateKeys(onKey: (intent: GateKey) => void): void {
  useEffect(() => {
    const handle = (event: KeyboardEvent) => {
      if (isTypingIn(event.target)) {
        return
      }
      const intent = intentOf(event)
      if (intent === null) {
        return
      }
      if (intent === 'open' && activatesOnEnter(event.target)) {
        return
      }
      event.preventDefault()
      onKey(intent)
    }

    window.addEventListener('keydown', handle)
    return () => {
      window.removeEventListener('keydown', handle)
    }
  }, [onKey])
}
