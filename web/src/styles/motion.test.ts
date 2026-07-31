import { readdirSync, readFileSync } from 'node:fs'
import { join } from 'node:path'

import { describe, expect, it } from 'vitest'

/*
 * 动画（REQ-UI-010）。
 *
 * 三件事各自都能悄悄坏掉：关键帧被人「顺手调一下手感」而与设计稿分家；
 * 某一条改成动 `left` 或 `height` 从而每帧触发布局；新加的循环动画忘了
 * 被 reduced-motion 拦住 —— 而这三件事在界面上都不会立刻显形。
 */

const SRC = join(process.cwd(), 'src')
const RESET = join(SRC, 'styles', 'reset.css')

function* walk(dir: string): Generator<string> {
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const path = join(dir, entry.name)
    if (entry.isDirectory()) {
      yield* walk(path)
    } else if (entry.name.endsWith('.css')) {
      yield path
    }
  }
}

const STYLESHEETS = [...walk(SRC)]

/** 一个关键帧：`偏移 → { 属性: 值 }`。偏移原样保留（`0%,100%` 是一个键）。 */
type Keyframe = Record<string, Record<string, string>>

function keyframesIn(css: string): Map<string, Keyframe> {
  const found = new Map<string, Keyframe>()
  const head = /@keyframes\s+([A-Za-z][\w-]*)\s*\{/g
  for (let match = head.exec(css); match !== null; match = head.exec(css)) {
    const name = match[1] ?? ''
    let depth = 1
    let cursor = match.index + match[0].length
    while (depth > 0) {
      const char = css[cursor]
      depth += char === '{' ? 1 : char === '}' ? -1 : 0
      cursor += 1
    }
    found.set(name, stepsIn(css.slice(match.index + match[0].length, cursor - 1)))
  }
  return found
}

function stepsIn(body: string): Keyframe {
  const steps: Keyframe = {}
  const step = /([^{}]+)\{([^{}]*)\}/g
  for (let match = step.exec(body); match !== null; match = step.exec(body)) {
    const offset = (match[1] ?? '').replace(/\s+/g, '')
    const declarations: Record<string, string> = {}
    for (const line of (match[2] ?? '').split(';')) {
      const colon = line.indexOf(':')
      if (colon === -1) {
        continue
      }
      declarations[line.slice(0, colon).trim()] = line.slice(colon + 1).trim().replace(/\s+/g, ' ')
    }
    steps[offset] = declarations
  }
  return steps
}

const ALL_KEYFRAMES = new Map<string, Keyframe>()
const DEFINED_IN = new Map<string, string[]>()
for (const path of STYLESHEETS) {
  for (const [name, steps] of keyframesIn(readFileSync(path, 'utf8'))) {
    ALL_KEYFRAMES.set(name, steps)
    DEFINED_IN.set(name, [...(DEFINED_IN.get(name) ?? []), path])
  }
}

/**
 * 设计稿里的七个关键帧。名字与设计稿一致，只有一处例外见 `seamBreath`。
 *
 * `reqL` 是唯一改过的一条：设计稿动的是 `left`，那会每帧触发布局，与 AC3 相斥。
 * 换成 `translateX`，位移改用轨迹宽度表达，视觉相同。
 */
const DESIGN: Record<string, Keyframe> = {
  // 设计稿里叫 seamB。CSS Modules 会给关键帧名加作用域，改成能读出意思的全称。
  seamBreath: {
    '0%,100%': { opacity: '0.7' },
    '50%': { opacity: '1' },
  },
  reqL: {
    '0%': { transform: 'translateX(0)', opacity: '0' },
    '8%': { opacity: '0.9' },
    '46%': { transform: 'translateX(calc(var(--trace-width) - 14px))', opacity: '0.9' },
    '54%': { transform: 'translateX(calc(var(--trace-width) - 14px))', opacity: '0' },
    '100%': { transform: 'translateX(calc(var(--trace-width) - 14px))', opacity: '0' },
  },
  corePulse: {
    '0%,46%': { opacity: '0', transform: 'scaleY(0.4)' },
    '52%': { opacity: '1', transform: 'scaleY(1)' },
    '60%,100%': { opacity: '0', transform: 'scaleY(0.4)' },
  },
  breathe: {
    '0%,100%': { opacity: '0.35' },
    '50%': { opacity: '1' },
  },
  pageL: {
    from: { transform: 'scaleX(0.04)', opacity: '0' },
    to: { transform: 'scaleX(1)', opacity: '1' },
  },
  pageR: {
    from: { transform: 'scaleX(0.04)', opacity: '0' },
    to: { transform: 'scaleX(1)', opacity: '1' },
  },
  offPulse: {
    '0%,100%': { opacity: '0.25' },
    '50%': { opacity: '0.6' },
  },
}

describe('七个关键帧与设计稿一致（AC1）', () => {
  it.each(Object.keys(DESIGN))('%s 的每一帧都对得上', (name) => {
    expect(ALL_KEYFRAMES.get(name)).toEqual(DESIGN[name])
  })

  it('没有第八个关键帧', () => {
    // 动画只服务于缝的叙事，多出来的一定是装饰性的。
    expect([...ALL_KEYFRAMES.keys()].sort()).toEqual(Object.keys(DESIGN).sort())
  })

  it('每一个都有人在用', () => {
    const declarations = STYLESHEETS.map((path) => readFileSync(path, 'utf8')).join('\n')
    const unused = [...ALL_KEYFRAMES.keys()].filter(
      (name) => !new RegExp(`animation:\\s*${name}\\b`).test(declarations),
    )
    expect(unused).toEqual([])
  })

  it('breathe 在两个文件里各有一份，且完全相同', () => {
    // CSS Modules 的关键帧按文件作用域，跨文件用不了同一份。既然只能抄，
    // 就断言两份一字不差 —— 只改其中一份是这里唯一会出的错。
    const files = DEFINED_IN.get('breathe') ?? []
    expect(files).toHaveLength(2)
    const shapes = files.map((path) => JSON.stringify(keyframesIn(readFileSync(path, 'utf8')).get('breathe')))
    expect(new Set(shapes).size).toBe(1)
  })
})

describe('动画不触发布局（AC3）', () => {
  const ANIMATABLE = new Set(['transform', 'opacity'])

  it.each([...ALL_KEYFRAMES.keys()])('%s 只动 transform 与 opacity', (name) => {
    const offending = Object.values(ALL_KEYFRAMES.get(name) ?? {}).flatMap((declarations) =>
      Object.keys(declarations).filter((property) => !ANIMATABLE.has(property)),
    )
    expect(offending).toEqual([])
  })

  it('过渡也只在这两者上', () => {
    // `transition: transform 160ms` 之外的属性会在整个过渡期间反复触发布局。
    const offending: string[] = []
    for (const path of STYLESHEETS) {
      const transitions = readFileSync(path, 'utf8').matchAll(/transition:\s*([^;]+);/g)
      for (const [, value = ''] of transitions) {
        const property = value.trim().split(/\s+/)[0] ?? ''
        if (!ANIMATABLE.has(property)) {
          offending.push(`${path}: ${value.trim()}`)
        }
      }
    }
    expect(offending).toEqual([])
  })
})

describe('reduced-motion 下循环动画停止（AC2）', () => {
  const reset = readFileSync(RESET, 'utf8')
  const block = /@media\s*\(prefers-reduced-motion:\s*reduce\)\s*\{([\s\S]*?)\n\}/.exec(reset)?.[1] ?? ''

  it('reset 里有一条覆盖所有元素的规则', () => {
    expect(block).toContain('*,')
    expect(block).toContain('*::before')
    expect(block).toContain('*::after')
  })

  it('循环被打断，时长归零，且都带 !important', () => {
    // 没有 !important 的话，组件里 `animation: seamBreath 7s ... infinite` 的
    // 简写会连同次数一起把这条规则盖掉，缝照旧一直呼吸。
    expect(block).toMatch(/animation-iteration-count:\s*1\s*!important/)
    expect(block).toMatch(/animation-duration:\s*0\.01ms\s*!important/)
    expect(block).toMatch(/transition-duration:\s*0\.01ms\s*!important/)
  })

  it('没有组件用 !important 把它盖回去', () => {
    const shouted = STYLESHEETS.filter(
      (path) => path !== RESET && /animation[^;]*!important/.test(readFileSync(path, 'utf8')),
    )
    expect(shouted).toEqual([])
  })

  it('这是唯一一处 reduced-motion 规则', () => {
    // 分散在各个组件里就一定会漏：新加一个循环动画的人不会想起要去补一条。
    const elsewhere = STYLESHEETS.filter(
      (path) => path !== RESET && /prefers-reduced-motion/.test(readFileSync(path, 'utf8')),
    )
    expect(elsewhere).toEqual([])
  })
})
