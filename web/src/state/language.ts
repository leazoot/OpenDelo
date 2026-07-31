import { create } from 'zustand'

/** 界面语言（REQ-UI-008）。本期只有中英两种。 */
export type Language = 'zh' | 'en'

const STORAGE_KEY = 'opendelo.language'

/** `<html lang>` 的取值，供屏幕阅读器选择正确的语音。 */
export const HTML_LANG: Record<Language, string> = {
  zh: 'zh-CN',
  en: 'en',
}

interface LanguageState {
  language: Language
  setLanguage: (language: Language) => void
}

/**
 * 默认中文：设计稿与 PRD 都以中文为第一语言，而 Console 只在本机使用，
 * 猜浏览器语言只会让同一台机器上的界面时中时英。
 */
function readStoredLanguage(): Language {
  const stored = localStorage.getItem(STORAGE_KEY)
  return stored === 'zh' || stored === 'en' ? stored : 'zh'
}

export const useLanguageStore = create<LanguageState>((set) => ({
  language: readStoredLanguage(),
  setLanguage: (language) => {
    localStorage.setItem(STORAGE_KEY, language)
    set({ language })
  },
}))

/** 两种语言之间来回切；顶栏的「中 / EN」是一个开关而不是一个菜单。 */
export function nextLanguage(language: Language): Language {
  return language === 'zh' ? 'en' : 'zh'
}
