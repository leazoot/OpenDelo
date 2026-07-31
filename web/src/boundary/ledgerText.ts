/** 只留时分。账本条上的都是刚刚发生的事，年月日是噪音。 */
export function clockTimeOf(iso: string): string {
  const at = new Date(iso)
  if (Number.isNaN(at.getTime())) {
    return ''
  }
  const pad = (value: number) => String(value).padStart(2, '0')
  return `${pad(at.getHours())}:${pad(at.getMinutes())}`
}
