import { QueryClientProvider } from '@tanstack/react-query'
import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'

import './styles/fonts.css'
import './styles/tokens.css'
import './styles/reset.css'

import { App } from './app/App'
import { ThemeProvider } from './app/ThemeProvider'
import { createQueryClient } from './data/queryClient'

const container = document.getElementById('root')
if (!container) {
  throw new Error('缺少 #root 挂载点')
}

// 「这个浏览器打不开」的提示是给跑不到这一行的浏览器看的（REQ-NFR-003 AC2）。
// 跑到了就把它拿掉，别留在无障碍树里让读屏念出一段不成立的话。
document.getElementById('unsupported')?.remove()

createRoot(container).render(
  <StrictMode>
    <QueryClientProvider client={createQueryClient()}>
      <ThemeProvider>
        <App />
      </ThemeProvider>
    </QueryClientProvider>
  </StrictMode>,
)
