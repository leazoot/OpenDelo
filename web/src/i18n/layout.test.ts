import { readFileSync, readdirSync, statSync } from 'node:fs'
import { extname, join, relative } from 'node:path'

import { describe, expect, it } from 'vitest'

import { copyFor, type Copy } from './copy'

/*
 * 两种语言下不许有文案被吃掉（REQ-UI-008 AC2）。
 *
 * jsdom 不做布局，量不出像素（`Seam.test.tsx` 以来一贯如此，像素级断言留给
 * S7 的 Playwright）。能在这里守住的是**文字消失的三条路径**，它们都写在
 * CSS 里，而且都能读出来：
 *
 *   1. 省略号 / `nowrap` + `overflow:hidden` —— 直接把话切断。
 *   2. 页面根容器 `overflow:hidden` 而内层没有可滚动的区域 —— 长出来的部分够不着。
 *   3. `nowrap` 的文案槽位放不下更长的那种语言 —— 撑破所在的盒子。
 *
 * 英文普遍比中文长一倍以上，这三条上中文能过、英文过不去是常态，所以按语言各查一遍。
 */

const SRC = 'src'

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

interface Rule {
  readonly file: string
  readonly selector: string
  readonly body: string
}

/** 逐条读出 CSS 规则。注释先去掉：注释里出现的声明不是声明。 */
function rulesOf(path: string): Rule[] {
  const css = readFileSync(path, 'utf8').replace(/\/\*[\s\S]*?\*\//g, '')
  return [...css.matchAll(/([^{}]+)\{([^{}]*)\}/g)].map((match) => ({
    file: relative(SRC, path),
    selector: (match[1] ?? '').trim().replace(/\s+/g, ' '),
    body: match[2] ?? '',
  }))
}

const ALL_RULES = [...walk(SRC)].flatMap(rulesOf)

/** 会让文字从画面上消失的三种写法。 */
function clips(body: string): boolean {
  return (
    /text-overflow\s*:\s*ellipsis/.test(body) ||
    /white-space\s*:\s*nowrap/.test(body) ||
    /clip-path\s*:\s*inset\(50%\)/.test(body)
  )
}

/**
 * 允许裁切的槽位，按理由分三类。
 *
 * 名单之外的裁切一律视为缺陷：文案是我们自己写的，写不下就该换行或改短，
 * 而不是让它在某一种语言下断在半路。
 */
const CLIPPING_ALLOWED = new Map<string, string>([
  // 内容来自外部字符串（仓库名、路径、服务名），长度没有上限，只能省略。
  ['boundary/Passage.module.css .action', '操作与资源，来自请求本身'],
  ['boundary/LedgerRibbon.module.css .text', '服务与事件类型，来自审计记录'],
  // 文字留在无障碍树里，只是不占位置。
  ['app/BoundaryBar.module.css .navLabel', '1024 上换成图标，名字留给读屏'],
  ['app/BoundaryBar.module.css .pendingCount', '1024 上收成角点，数字留给读屏'],
  ['pages/gate/GatePage.module.css .announce', '只给读屏的播报区，界面上根本不占位置'],
  // 不裁切，只是不换行；能不能放下由下面的宽度预算守着。
  ['boundary/Passage.module.css .risk', '风险等级，一行放得下'],
])

describe('文案不会被裁掉', () => {
  it('只有名单上的槽位允许裁切或不换行', () => {
    const found = ALL_RULES.filter((rule) => clips(rule.body)).map((rule) => `${rule.file} ${rule.selector}`)

    expect(new Set(found)).toEqual(new Set(CLIPPING_ALLOWED.keys()))
  })

  it('名单上的每一条都写清了为什么允许', () => {
    for (const [slot, reason] of CLIPPING_ALLOWED) {
      expect(reason, slot).not.toBe('')
    }
  })

  it('Inspector 的值不再用省略号', () => {
    // 「文书」这一行装的是整句判断理由：英文两倍长，省略号会正好切在「为什么」上。
    const rowValue = ALL_RULES.find((rule) => rule.file.endsWith('Inspector.module.css') && rule.selector === '.rowValue')

    expect(rowValue?.body).toContain('overflow-wrap: anywhere')
    expect(rowValue?.body).not.toContain('text-overflow')
  })
})

describe('长出来的内容够得着', () => {
  /*
   * 页面根容器：把整页裁掉一截的地方。
   *
   * 只查 `pages/` —— 外壳（`AppLayout`）不滚是有意的，顶栏与缝要留在原处，
   * 滚动由每一页自己负责。
   */
  const CLIPPED_PAGES = ALL_RULES.filter(
    (rule) =>
      rule.file.startsWith('pages/') &&
      /overflow(-y)?\s*:\s*hidden/.test(rule.body) &&
      /height\s*:\s*100%/.test(rule.body) &&
      // Access Folio 有意不滚：十一项要在 1440 下一眼看全（REQ-APPROVAL-001 AC3），
      // 由 `FolioPage.test.tsx` 守着。
      !rule.file.startsWith('pages/folio/'),
  )

  it('确实找得到这些整页裁切的容器', () => {
    // 名单为空时上面那条断言会空跑，这里挡住那种「全绿其实什么也没查」。
    expect(CLIPPED_PAGES.length).toBeGreaterThanOrEqual(4)
  })

  it.each(CLIPPED_PAGES.map((rule) => [rule.file, rule.selector] as const))(
    '%s %s 的内层有可滚动的区域',
    (file) => {
      const inner = ALL_RULES.filter((rule) => rule.file === file && /overflow-y\s*:\s*auto/.test(rule.body))

      expect(inner.length, `${file} 把整页裁掉了，却没有任何一层能滚`).toBeGreaterThan(0)
    },
  )
})

/*
 * 宽度预算。
 *
 * 不追求像素准确 —— jsdom 量不了，也不该在这里假装能量。取的是每个字符的**上界**：
 * 拉丁字符按 0.62em（大写与数字这一档），汉字按 1em。估出来只会偏大，因此
 * 「估算放得下」是可信的，「估算放不下」则一定要去看真实渲染。
 */
const LATIN_ADVANCE = 0.62
/** 汉字、中文标点与全角字符：这三段都按一个字宽算。 */
const HAN = /[\u4E00-\u9FFF\u3000-\u303F\uFF00-\uFFEF]/

function estimatedWidth(text: string, fontSize: number): number {
  let em = 0
  for (const character of text) {
    em += HAN.test(character) ? 1 : LATIN_ADVANCE
  }
  return em * fontSize
}

describe('不换行的文案在最窄的断点上放得下', () => {
  /*
   * Passage 卡片在 1024 上宽 214：减去内边距 26、mark 26 与两处间距 19，
   * 正文剩 143。风险等级与代理名共用这一行，给风险留一半略多。
   */
  const RISK_BUDGET = 90
  const RISK_FONT_SIZE = 11

  const riskKeys = ['riskLow', 'riskMedium', 'riskHigh', 'riskUnknown'] as const

  it.each(['zh', 'en'] as const)('%s 的四个风险等级都不撑破卡片', (language) => {
    const copy: Copy = copyFor(language)

    for (const key of riskKeys) {
      const width = estimatedWidth(copy[key], RISK_FONT_SIZE)
      expect(width, `${language}.${key}「${copy[key]}」估算 ${String(Math.round(width))}px`).toBeLessThanOrEqual(
        RISK_BUDGET,
      )
    }
  })

  it('卡片宽度改了这条预算就要重算', () => {
    const passage = readFileSync(join(SRC, 'boundary/Passage.module.css'), 'utf8')

    // 214 是 1024 上的卡片宽度，预算就是从它推出来的。
    expect(passage).toContain('width: 214px')
  })
})
