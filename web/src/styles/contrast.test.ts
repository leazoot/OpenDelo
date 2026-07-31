import { readFileSync, readdirSync, statSync } from 'node:fs'
import { extname, join, relative } from 'node:path'

import { describe, expect, it } from 'vitest'

/*
 * 深浅两个主题逐页核对（REQ-UI-008）。
 *
 * 令牌扫描只能证明颜色都来自令牌，证明不了这一对令牌在两个主题下都还能看清：
 * 令牌各自跟着主题翻转，翻转之后**配在一起**是什么样，没有任何检查看着。
 * 深色是默认主题，浅色下的深底深字在开发时根本不会出现在眼前。
 *
 * 这里把每条同时写了背景与前景的规则，在两套令牌下各算一次对比度。
 * 只算两端都是实心 hex 的组合 —— 半透明令牌叠出来的实际颜色取决于它压在谁身上，
 * 那要真实渲染才知道（axe 扫描见 `app/a11y.test.tsx`）。
 */

const SRC = 'src'
const TOKENS = join(SRC, 'styles/tokens.css')

const tokensCss = readFileSync(TOKENS, 'utf8').replace(/\/\*[\s\S]*?\*\//g, '')

function definitionsIn(selectorPattern: string): Record<string, string> {
  const block = new RegExp(`${selectorPattern}\\s*\\{([^}]*)\\}`).exec(tokensCss)
  if (block === null) {
    throw new Error(`tokens.css 里找不到 ${selectorPattern}`)
  }
  return Object.fromEntries(
    [...(block[1] ?? '').matchAll(/(--[a-z0-9-]+)\s*:\s*([^;]+);/g)].map((match) => [match[1] ?? '', (match[2] ?? '').trim()]),
  )
}

const THEMES: Record<string, Record<string, string>> = {
  深色: definitionsIn(":root,\\s*\\[data-theme='dark'\\]"),
  浅色: definitionsIn("\\[data-theme='light'\\]"),
}

interface Color {
  readonly r: number
  readonly g: number
  readonly b: number
}

function channels(value: string): Color | null {
  const match = /^#([0-9a-f]{2})([0-9a-f]{2})([0-9a-f]{2})$/i.exec(value)
  if (match === null) {
    return null
  }
  const [r, g, b] = [1, 2, 3].map((index) => parseInt(match[index] ?? '0', 16) / 255)
  return { r: r ?? 0, g: g ?? 0, b: b ?? 0 }
}

/** WCAG 2.1 的相对亮度。 */
function luminance({ r, g, b }: Color): number {
  const linear = (channel: number) => (channel <= 0.03928 ? channel / 12.92 : ((channel + 0.055) / 1.055) ** 2.4)
  return 0.2126 * linear(r) + 0.7152 * linear(g) + 0.0722 * linear(b)
}

function contrast(a: Color, b: Color): number {
  const [brighter, darker] = [luminance(a), luminance(b)].sort((x, y) => y - x)
  return ((brighter ?? 0) + 0.05) / ((darker ?? 0) + 0.05)
}

function* walk(dir: string): Generator<string> {
  for (const entry of readdirSync(dir)) {
    const path = join(dir, entry)
    if (statSync(path).isDirectory()) {
      yield* walk(path)
    } else if (extname(path) === '.css') {
      yield path
    }
  }
}

interface Pair {
  readonly where: string
  readonly background: string
  readonly foreground: string
  /** 大号文本的门槛低一档。 */
  readonly isLarge: boolean
}

function pairsIn(path: string): Pair[] {
  const css = readFileSync(path, 'utf8').replace(/\/\*[\s\S]*?\*\//g, '')
  const pairs: Pair[] = []
  for (const rule of css.matchAll(/([^{}]+)\{([^{}]*)\}/g)) {
    const body = rule[2] ?? ''
    const background = /background(?:-color)?\s*:\s*var\((--[a-z0-9-]+)\)/.exec(body)
    const foreground = /(?:^|;|\s)color\s*:\s*var\((--[a-z0-9-]+)\)/.exec(body)
    if (background === null || foreground === null) {
      continue
    }
    const fontSize = /font-size\s*:\s*([\d.]+)px/.exec(body)
    pairs.push({
      where: `${relative(SRC, path)} ${(rule[1] ?? '').trim().replace(/\s+/g, ' ')}`,
      background: background[1] ?? '',
      foreground: foreground[1] ?? '',
      isLarge: fontSize !== null && Number(fontSize[1]) >= 19,
    })
  }
  return pairs
}

const PAIRS = [...walk(SRC)].flatMap(pairsIn)

describe('深浅两个主题下的对比度', () => {
  it('确实找得到成对写着背景与前景的规则', () => {
    // 取不到就说明解析写坏了，下面两条会空跑成绿色。
    expect(PAIRS.length).toBeGreaterThanOrEqual(10)
  })

  it.each(Object.keys(THEMES))('%s 主题下正文 ≥ 4.5:1、大号文本 ≥ 3:1', (theme) => {
    const tokens = THEMES[theme] ?? {}
    const failures: string[] = []

    for (const pair of PAIRS) {
      const background = channels(tokens[pair.background] ?? '')
      const foreground = channels(tokens[pair.foreground] ?? '')
      if (background === null || foreground === null) {
        continue
      }
      const ratio = contrast(background, foreground)
      const required = pair.isLarge ? 3 : 4.5
      if (ratio < required) {
        failures.push(
          `${pair.where}：${pair.foreground} 压在 ${pair.background} 上只有 ${ratio.toFixed(2)}:1，需要 ${String(required)}:1`,
        )
      }
    }

    expect(failures).toEqual([])
  })

  it('缝内侧那一档底色不拿来铺文字', () => {
    // --core 是凭据核心的颜色，两个主题下都是近黑；浅色主题的正文也是近黑，
    // 配在一起等于看不见（对比度 1.03:1）。要「陷进去」的输入框用 --desk。
    const core = [...walk(SRC)]
      .filter((path) => path !== TOKENS)
      .filter((path) => /background(?:-color)?\s*:\s*var\(--core\)/.test(readFileSync(path, 'utf8')))

    expect(core).toEqual([])
  })
})
