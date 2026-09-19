import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { BrowserRouter } from 'react-router-dom'
import App from './App'
import { Providers } from './app/providers'

const root = document.getElementById('root')
if (!root) {
    throw new Error('Root element #root not found')
}

createRoot(root).render(
    <StrictMode>
        <Providers>
            <BrowserRouter>
                <App />
            </BrowserRouter>
        </Providers>
    </StrictMode>,
)
