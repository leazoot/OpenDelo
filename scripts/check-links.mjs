#!/usr/bin/env node
/*
 * 校验文档里的链接都还指向存在的东西（REQ-NFR-* / TASK-0713 的验收标准）。
 *
 * 只查仓库内部的引用 —— 文件路径与同一篇里的小节锚点。外部 URL 不查：
 * 那要联网，而检查链路必须能离线跑（`.claude/rules/security.md` §11 的同一条理由）。
 *
 * 失效的内部链接是文档腐坏最先出现的形状：文件改了名、小节改了标题，
 * 指过去的那一行不会有任何提示，直到有人点下去。
 */

import { readdirSync, readFileSync, statSync } from 'node:fs'
import { dirname, join, relative, resolve } from 'node:path'
import { argv, exit, stdout } from 'node:process'

/** 不扫的目录：依赖、工具、构建产物。 */
const SKIP = new Set(['node_modules', '.bin', '.git', 'dist', 'embedded', 'test-results'])

/*
 * 每一个 `](目标)` 都查，不去分辨那是链接还是图片、外层还是内层。
 *
 * 徽章是 `[![alt](图)](目标)` 这种套娃形状：只认「方括号里没有方括号」的写法，
 * 会把外层那个目标整个漏掉 —— README 顶部一排徽章的落点全在外层。
 */
const LINK = /\]\(([^)\s]+)(?:\s+"[^"]*")?\)/g

function* markdownFiles(dir) {
  for (const entry of readdirSync(dir)) {
    if (SKIP.has(entry)) {
      continue
    }
    const path = join(dir, entry)
    if (statSync(path).isDirectory()) {
      yield* markdownFiles(path)
    } else if (entry.endsWith('.md')) {
      yield path
    }
  }
}

/**
 * anchorsOf 取出一篇里所有标题对应的锚点。
 *
 * 与 GitHub 的生成规则对齐：小写、去掉标点、空格换连字符。
 * 中文字符原样保留 —— GitHub 也是这样处理的。
 */
function anchorsOf(text) {
  const anchors = new Set()
  for (const [, level, title] of text.matchAll(/^(#{1,6})\s+(.+?)\s*$/gm)) {
    void level
    anchors.add(
      title
        .replace(/`/g, '')
        .replace(/\[([^\]]*)\]\([^)]*\)/g, '$1')
        .toLowerCase()
        .replace(/[^\p{L}\p{N}\s-]/gu, '')
        .trim()
        .replace(/\s+/g, '-'),
    )
  }
  return anchors
}

const broken = []
let files = 0
let links = 0

for (const path of markdownFiles('.')) {
  files += 1
  const text = readFileSync(path, 'utf8')
  const anchors = anchorsOf(text)
  const lines = text.split('\n')

  lines.forEach((line, index) => {
    for (const [, target] of line.matchAll(LINK)) {
      // 外部链接与协议链接不查。
      if (/^(https?:|mailto:|tel:)/.test(target)) {
        continue
      }
      links += 1
      const where = `${relative('.', path)}:${index + 1}`

      if (target.startsWith('#')) {
        if (!anchors.has(target.slice(1).toLowerCase())) {
          broken.push({ where, target, why: '本篇里没有这个小节' })
        }
        continue
      }

      const [filePart, anchor] = target.split('#')
      const resolved = resolve(dirname(path), filePart ?? '')
      try {
        statSync(resolved)
      } catch {
        broken.push({ where, target, why: '指向的文件不存在' })
        continue
      }
      if (anchor !== undefined && anchor !== '' && resolved.endsWith('.md')) {
        const targetAnchors = anchorsOf(readFileSync(resolved, 'utf8'))
        if (!targetAnchors.has(anchor.toLowerCase())) {
          broken.push({ where, target, why: '目标文件里没有这个小节' })
        }
      }
    }
  })
}

if (files === 0) {
  stdout.write('一篇 Markdown 都没扫到，检查等于没做。\n')
  exit(1)
}

if (broken.length > 0) {
  stdout.write(`发现 ${broken.length} 处失效链接：\n\n`)
  for (const each of broken) {
    stdout.write(`  ${each.where}  ${each.target}  —— ${each.why}\n`)
  }
  exit(1)
}

if (argv.includes('--verbose')) {
  stdout.write(`链接检查通过：${files} 篇文档中的 ${links} 个仓库内链接全部有效。\n`)
}
