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

createRoot(container).render(
  <StrictMode>
    <QueryClientProvider client={createQueryClient()}>
      <ThemeProvider>
        <App />
      </ThemeProvider>
    </QueryClientProvider>
  </StrictMode>,
)
