import { create } from 'zustand'

/*
 * 当前选中的 Passage，属于跨页偏好那一类状态。
 *
 * 放在 store 而不是 Gate 的局部状态里：Inspector、键盘决策与日后的命令面板
 * 都要知道「现在说的是哪一条」，从一个地方读比一路传下去少一份走样的机会。
 */

interface SelectionState {
  /** 选中的能力请求主键；没有选中时为空串。 */
  selectedPassageId: string
  select: (id: string) => void
  clear: () => void
}

export const useSelectionStore = create<SelectionState>((set) => ({
  selectedPassageId: '',
  select: (id) => {
    set({ selectedPassageId: id })
  },
  clear: () => {
    set({ selectedPassageId: '' })
  },
}))
