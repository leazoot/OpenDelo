/*
 * 文书草稿的自动保存（设计稿 §06 顶栏的「草稿已自动保存」）。
 *
 * 草稿留在本机 —— PRD §27 里没有存草稿的端点，而**一份存在服务端的草稿
 * 与一条已生效的规则在数据库里长得太像**：未签署的东西不该出现在决策链路
 * 能读到的地方。localStorage 是本机的、不上传的。
 *
 * 草稿里只有「起草成什么行为」一项：文书能签下去的改动只有这一处。
 */

const PREFIX = 'opendelo.manuscript.'

export interface Draft {
  readonly behavior: string
  /** 起草的时刻，顶栏据此显示「草稿已自动保存 · HH:MM」。 */
  readonly savedAt: string
}

export function loadDraft(ruleId: string): Draft | null {
  if (ruleId === '') {
    return null
  }
  const stored = readItem(PREFIX + ruleId)
  if (stored === null) {
    return null
  }
  const at = stored.indexOf(' ')
  if (at <= 0) {
    // 读不懂的草稿当作没有草稿：补一个默认值会让人以为自己起草过什么。
    return null
  }
  return { behavior: stored.slice(0, at), savedAt: stored.slice(at + 1) }
}

export function saveDraft(ruleId: string, draft: Draft): void {
  if (ruleId === '') {
    return
  }
  writeItem(PREFIX + ruleId, `${draft.behavior} ${draft.savedAt}`)
}

export function clearDraft(ruleId: string): void {
  if (ruleId === '') {
    return
  }
  removeItem(PREFIX + ruleId)
}

/** 时刻写成 HH:MM（设计稿：`14:31`）。 */
export function clockTextOf(now: number): string {
  const at = new Date(now)
  return `${pad(at.getHours())}:${pad(at.getMinutes())}`
}

function pad(value: number): string {
  return value.toString().padStart(2, '0')
}

/*
 * localStorage 在无痕模式与某些嵌入环境下会抛异常。草稿存不下不是错误，
 * 只是「这次刷新之后它不在了」—— 但**不能因此让整页崩掉**。
 */
function readItem(key: string): string | null {
  try {
    return globalThis.localStorage.getItem(key)
  } catch {
    return null
  }
}

function writeItem(key: string, value: string): void {
  try {
    globalThis.localStorage.setItem(key, value)
  } catch {
    // 存不下就只影响这一次刷新之后草稿还在不在，不影响这一页的任何判断。
  }
}

function removeItem(key: string): void {
  try {
    globalThis.localStorage.removeItem(key)
  } catch {
    // 删不掉只意味着草稿会多留一次，同样不影响这一页的任何判断。
  }
}
