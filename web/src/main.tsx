import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'

import { App } from './app/App'
import './styles/tokens.css'
import './styles/reset.css'

const container = document.getElementById('root')
if (!container) {
  throw new Error('index.html is missing #root; the app has nowhere to mount')
}

createRoot(container).render(
  <StrictMode>
    <App />
  </StrictMode>,
)
