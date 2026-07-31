#!/usr/bin/env node
/*
 * 扫描 web/src 下的字面色值。
 *
 * 设计令牌是全站唯一的色彩来源。组件里出现
 * 任何字面色值都会在切换主题时留下不跟随主题的残留，因此命中即失败。
 *
 * tokens.css 是唯一的例外——它就是令牌的定义处。
 */

import { readdirSync, readFileSync, statSync } from 'node:fs'
import { extname, join, relative } from 'node:path'
import { argv, exit, stdout } from 'node:process'

const ROOT = 'web/src'
const ALLOWLIST = new Set(['web/src/styles/tokens.css'])
const SCANNED_EXTENSIONS = new Set(['.css', '.ts', '.tsx'])

/**
 * 十六进制必须是 3/4/6/8 位且前后不粘连其他字符，避免把 `#root`、URL 片段、
 * 提交哈希这类非颜色内容误判成色值。
 */
const PATTERNS = [
  { name: '十六进制色值', regex: /#[0-9a-fA-F]{8}(?![0-9a-fA-F])|#[0-9a-fA-F]{6}(?![0-9a-fA-F])|#[0-9a-fA-F]{4}(?![0-9a-fA-F])|#[0-9a-fA-F]{3}(?![0-9a-fA-F])/g },
  { name: 'rgb() / rgba()', regex: /\brgba?\s*\(/g },
  { name: 'hsl() / hsla()', regex: /\bhsla?\s*\(/g },
]

function* walk(dir) {
  for (const entry of readdirSync(dir)) {
    const path = join(dir, entry)
    if (statSync(path).isDirectory()) {
      yield* walk(path)
    } else if (SCANNED_EXTENSIONS.has(extname(path))) {
      yield path
    }
  }
}

const violations = []

for (const path of walk(ROOT)) {
  const normalized = relative('.', path)
  if (ALLOWLIST.has(normalized)) {
    continue
  }

  const lines = readFileSync(path, 'utf8').split('\n')
  lines.forEach((line, index) => {
    for (const { name, regex } of PATTERNS) {
      for (const match of line.matchAll(regex)) {
        violations.push({ path: normalized, line: index + 1, name, text: match[0] })
      }
    }
  })
}

if (violations.length > 0) {
  stdout.write(`发现 ${violations.length} 处字面色值，请改用 tokens.css 中的设计令牌：\n\n`)
  for (const v of violations) {
    stdout.write(`  ${v.path}:${v.line}  ${v.name}  ${v.text}\n`)
  }
  stdout.write('\n允许出现字面色值的文件只有：' + [...ALLOWLIST].join('、') + '\n')
  exit(1)
}

/*
 * 另外两条与主题有关的检查（REQ-UI-008 AC1）。
 *
 * 没有字面色值只说明颜色都来自变量，不说明这些变量在两个主题下都有值：
 *   · 只在深色块里定义的令牌，浅色主题下会沿用深色那一档 —— 深底白字变成浅底白字。
 *   · 拼错的令牌名根本不解析，元素会退回继承色，两个主题下都不对。
 * 这两种都不会在深色下暴露，而深色是默认主题，所以只有扫描能挡住。
 */
const TOKENS = 'web/src/styles/tokens.css'
const tokensCss = readFileSync(TOKENS, 'utf8').replace(/\/\*[\s\S]*?\*\//g, '')

/** 逐个主题块读出它定义的令牌名。 */
function definitionsIn(selectorPattern) {
  const block = new RegExp(`${selectorPattern}\\s*\\{([^}]*)\\}`).exec(tokensCss)
  if (block === null) {
    stdout.write(`tokens.css 里找不到 ${selectorPattern} 这一块，主题定义已被破坏。\n`)
    exit(1)
  }
  return new Set([...block[1].matchAll(/(--[a-z0-9-]+)\s*:/g)].map((match) => match[1]))
}

const dark = definitionsIn(":root,\\s*\\[data-theme='dark'\\]")
const light = definitionsIn("\\[data-theme='light'\\]")
const onlyDark = [...dark].filter((token) => !light.has(token))
const onlyLight = [...light].filter((token) => !dark.has(token))

if (onlyDark.length > 0 || onlyLight.length > 0) {
  stdout.write('深浅两个主题定义的令牌不一致，缺的那一侧会沿用另一个主题的值：\n\n')
  for (const token of onlyDark) {
    stdout.write(`  只在深色下定义：${token}\n`)
  }
  for (const token of onlyLight) {
    stdout.write(`  只在浅色下定义：${token}\n`)
  }
  exit(1)
}

const defined = new Set([...dark, ...light, ...[...tokensCss.matchAll(/(--[a-z0-9-]+)\s*:/g)].map((m) => m[1])])
const undefinedReferences = []

for (const path of walk(ROOT)) {
  if (extname(path) !== '.css') {
    continue
  }
  const css = readFileSync(path, 'utf8')
  // 组件自己声明的局部变量（如 Passage 的 --trace-width）同样算已定义。
  const local = new Set([...css.matchAll(/(--[a-z0-9-]+)\s*:/g)].map((match) => match[1]))
  for (const match of css.matchAll(/var\((--[a-z0-9-]+)/g)) {
    if (!defined.has(match[1]) && !local.has(match[1])) {
      undefinedReferences.push(`${relative('.', path)}  ${match[1]}`)
    }
  }
}

if (undefinedReferences.length > 0) {
  stdout.write('引用了没有定义的令牌，这些元素在两个主题下都会退回继承色：\n\n')
  for (const reference of new Set(undefinedReferences)) {
    stdout.write(`  ${reference}\n`)
  }
  exit(1)
}

if (argv.includes('--verbose')) {
  stdout.write(
    `令牌扫描通过：web/src 下无字面色值；深浅主题各定义 ${dark.size} 个令牌且一一对应；引用的令牌全部有定义。\n`,
  )
}
