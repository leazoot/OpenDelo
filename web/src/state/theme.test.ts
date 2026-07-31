import { beforeEach, describe, expect, it, vi } from 'vitest'

import { resolveTheme, useThemeStore } from './theme'

describe('主题偏好', () => {
  beforeEach(() => {
    localStorage.clear()
    useThemeStore.setState({ preference: 'system' })
  })

  it('默认跟随系统', () => {
    expect(useThemeStore.getState().preference).toBe('system')
  })

  it('选过之后落盘（REQ-UI-008 AC3）', () => {
    useThemeStore.getState().setPreference('light')

    expect(useThemeStore.getState().preference).toBe('light')
    expect(localStorage.getItem('opendelo.theme')).toBe('light')
  })

  it('重新打开 Console 时读回上次的选择，而不是回到跟随系统', async () => {
    localStorage.setItem('opendelo.theme', 'dark')
    vi.resetModules()

    const reloaded = await import('./theme')

    expect(reloaded.useThemeStore.getState().preference).toBe('dark')
  })

  it('存着的值不认识时回到跟随系统，而不是让界面没有主题', async () => {
    localStorage.setItem('opendelo.theme', 'sepia')
    vi.resetModules()

    const reloaded = await import('./theme')

    expect(reloaded.useThemeStore.getState().preference).toBe('system')
  })

  it.each([
    ['dark', true, 'dark'],
    ['dark', false, 'dark'],
    ['light', true, 'light'],
    ['light', false, 'light'],
    ['system', true, 'dark'],
    ['system', false, 'light'],
  ] as const)('%s + 系统偏好深色=%s → %s', (preference, prefersDark, expected) => {
    // 明确选过的两档不看系统：选了浅色就是浅色，哪怕系统是深色。
    expect(resolveTheme(preference, prefersDark)).toBe(expected)
  })
})
