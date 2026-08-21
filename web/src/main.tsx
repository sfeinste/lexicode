import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'

import { App } from './App'
import './index.css'

const container = document.getElementById('root')
if (!container) {
  throw new Error('index.html is missing #root; the app has nowhere to mount')
}

createRoot(container).render(
  <StrictMode>
    <App />
  </StrictMode>,
)
