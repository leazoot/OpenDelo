import { describe, expect, it } from 'vitest'

import { activatesOnEnter, intentOf, isTypingIn } from './useGateKeys'

/*
 * 键盘决策的按键映射（REQ-APPROVAL-003）。
 */

const press = (key: string, modifiers: Partial<KeyboardEvent> = {}) => ({
  key,
  shiftKey: false,
  metaKey: false,
  ctrlKey: false,
  altKey: false,
  ...modifiers,
})

describe('按键 → 意图', () => {
  it('A 允许 · D 拒绝 · ⇧A 仅这一次 · ↵ 打开 · Esc 折回', () => {
    expect(intentOf(press('a'))).toBe('allow')
    expect(intentOf(press('d'))).toBe('deny')
    expect(intentOf(press('A', { shiftKey: true }))).toBe('allowOnce')
    expect(intentOf(press('Enter'))).toBe('open')
    expect(intentOf(press('Escape'))).toBe('dismiss')
  })

  it('大小写都认', () => {
    expect(intentOf(press('A'))).toBe('allow')
    expect(intentOf(press('D'))).toBe('deny')
  })

  it('带 ⌘ / ⌃ / ⌥ 的一律不认', () => {
    // ⌘A 是全选。把它接管成「允许」会让一次误按变成一次授权。
    for (const modifier of ['metaKey', 'ctrlKey', 'altKey'] as const) {
      expect(intentOf(press('a', { [modifier]: true })), modifier).toBeNull()
    }
  })

  it('⇧D 不是任何意图 —— 拒绝没有第二种写法', () => {
    expect(intentOf(press('D', { shiftKey: true }))).toBeNull()
  })

  it('其余按键都不认', () => {
    for (const key of ['b', 'x', ' ', 'Tab', 'ArrowDown']) {
      expect(intentOf(press(key)), key).toBeNull()
    }
  })
})

describe('输入控件里的按键不被接管', () => {
  it('输入框、文本域、下拉与可编辑区都算在打字', () => {
    // 在搜索框里打一个 a 不该放行一次请求。
    for (const tag of ['input', 'textarea', 'select']) {
      expect(isTypingIn(document.createElement(tag)), tag).toBe(true)
    }

    const editable = document.createElement('div')
    editable.contentEditable = 'true'
    // jsdom 不实现 isContentEditable，这里直接置位以覆盖那条分支。
    Object.defineProperty(editable, 'isContentEditable', { value: true })
    expect(isTypingIn(editable)).toBe(true)
  })

  it('普通元素与空目标不算', () => {
    expect(isTypingIn(document.createElement('div'))).toBe(false)
    expect(isTypingIn(null)).toBe(false)
  })
})

describe('回车归自己会响应回车的控件', () => {
  it('按钮与带 href 的链接算，其余不算', () => {
    // 抢过来的话，Tab 到卡片上按回车什么也不会发生（REQ-UI-009 AC1）。
    const link = document.createElement('a')
    link.href = '/gate'

    expect(activatesOnEnter(document.createElement('button'))).toBe(true)
    expect(activatesOnEnter(link)).toBe(true)
    expect(activatesOnEnter(document.createElement('a'))).toBe(false)
    expect(activatesOnEnter(document.createElement('div'))).toBe(false)
    expect(activatesOnEnter(null)).toBe(false)
  })
})
