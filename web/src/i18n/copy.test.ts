import { readFileSync, readdirSync, statSync } from 'node:fs'
import { extname, join, relative } from 'node:path'

import { describe, expect, it } from 'vitest'

import { copyFor, type Copy } from './copy'

const zh = copyFor('zh')
const en = copyFor('en')

/** 带插值的文案单独展开：它们的参数类型各不相同，没法一起遍历。 */
function interpolated(copy: Copy): string[] {
  return [
    copy.themeAria('x'),
    copy.pendingBadgeAria(2),
    copy.pendingInTitle(2),
    copy.folioHeadline('writer-agent', 'x'),
    copy.leaseTabAria('github', '41m'),
    copy.leaseRevokeBlurb('github'),
    copy.ledgerCounts(34, 3, false),
  ]
}

function plainValues(copy: Copy): [string, string][] {
  return Object.entries(copy).filter((entry): entry is [string, string] => typeof entry[1] === 'string')
}

describe('文案字典', () => {
  it('两种语言的键集合完全一致', () => {
    // 类型已经挡住了「英文少一条」；这一条挡的是反过来多出一条。
    expect(Object.keys(en).sort()).toEqual(Object.keys(zh).sort())
  })

  it('没有一条文案是空的', () => {
    for (const copy of [zh, en]) {
      for (const [key, value] of plainValues(copy)) {
        expect(value, key).not.toBe('')
      }
      for (const rendered of interpolated(copy)) {
        expect(rendered).not.toBe('')
      }
    }
  })

  it('插值真的被填进去了，而不是留下一个占位符', () => {
    expect(zh.pendingInTitle(2)).toContain('2')
    expect(en.pendingInTitle(2)).toContain('2')
    expect(zh.folioHeadline('writer-agent', '写入')).toContain('writer-agent')
    expect(en.folioHeadline('writer-agent', 'write')).toContain('writer-agent')
  })

  it('英文里没有漏译成中文的条目', () => {
    // 「中 / EN」是两种语言下相同的开关标签，其余英文文案不应含汉字。
    const han = /[一-鿿]/
    for (const [key, value] of plainValues(en)) {
      if (key === 'languageToggle' || key === 'languageAria') {
        continue
      }
      expect(han.test(value), `en.${key} 含汉字：${value}`).toBe(false)
    }
    for (const rendered of interpolated(en)) {
      expect(han.test(rendered), `含汉字：${rendered}`).toBe(false)
    }
  })
})

/*
 * 字典与界面之间的两条对账（REQ-UI-008）。
 *
 * 一条查界面上有没有绕过字典的中文 —— 那种字符串在英文界面上会原样留着；
 * 一条查字典里有没有没人渲染的中文 —— 那种字符串没有任何用例核对过，
 * 也没人知道它在哪个断点上放不放得下。
 */

const SRC = 'src'

function* walk(dir: string): Generator<string> {
  for (const entry of readdirSync(dir)) {
    const path = join(dir, entry)
    if (statSync(path).isDirectory()) {
      yield* walk(path)
    } else if (extname(path) === '.tsx' || extname(path) === '.ts') {
      yield path
    }
  }
}

/** 参与界面渲染的源码：字典自己与测试不算。 */
const PRODUCTION_SOURCES = [...walk(SRC)].filter(
  (path) =>
    !path.includes('.test.') &&
    !path.startsWith(join(SRC, 'i18n')) &&
    // 测试装置里的夹具是造出来的数据，不是界面文案。
    !path.startsWith(join(SRC, 'test')),
)

/** 注释里的中文是给读代码的人看的，不会渲染。 */
function withoutComments(source: string): string {
  return source
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .replace(/^\s*\/\/.*$/gm, '')
    .replace(/\{\/\*[\s\S]*?\*\/\}/g, '')
}

/**
 * 允许留在代码里的中文，逐条写明理由。
 *
 * 判断标准是「它会不会随语言变化」：会变的必须进字典，不变的才留在这里。
 */
const NOT_TRANSLATED = new Map<string, string>([
  ['src/main.tsx', '挂载点缺失时的崩溃信息，界面还没有起来'],
  ['src/pages/preferences/PreferencesPage.tsx', '语言选项上的「中」是这门语言的自称，英文界面里也写作「中」'],
  ['src/data/gateway.ts', '抛出的诊断信息，界面从不渲染 error.message'],
  ['src/data/ledger.ts', '抛出的诊断信息，界面从不渲染 error.message'],
])

describe('界面上的中文都来自字典', () => {
  it('没有绕过字典的中文', () => {
    const offenders = PRODUCTION_SOURCES.filter((path) => /[一-鿿]/.test(withoutComments(readFileSync(path, 'utf8'))))
      .map((path) => relative('.', path))
      .filter((path) => !NOT_TRANSLATED.has(path))

    expect(offenders).toEqual([])
  })

  it('组件不渲染 error.message —— 诊断信息因此不必进字典', () => {
    // 上面两条数据层的例外全靠这一条成立：一旦有页面把 message 直接摆上去，
    // 那些中文会在英文界面上原样出现，例外也就不再成立。
    const components = PRODUCTION_SOURCES.filter((path) => extname(path) === '.tsx')
    const offenders = components.filter((path) => /\{[^{}]*\.message[^{}]*\}/.test(readFileSync(path, 'utf8')))

    expect(offenders).toEqual([])
  })

  it('例外名单上的文件确实还有中文，理由也写着', () => {
    // 例外过期之后应当从名单里去掉，而不是留在那里挡住后来的违规。
    for (const [path, reason] of NOT_TRANSLATED) {
      expect(withoutComments(readFileSync(path, 'utf8')), path).toMatch(/[一-鿿]/)
      expect(reason, path).not.toBe('')
    }
  })
})

describe('字典里的每一条都有人渲染', () => {
  const renderedSource = PRODUCTION_SOURCES.map((path) => readFileSync(path, 'utf8')).join('\n')

  it('没有孤立的文案', () => {
    const orphans = Object.keys(zh).filter((key) => !new RegExp(`\\b${key}\\b`).test(renderedSource))

    expect(orphans).toEqual([])
  })
})
