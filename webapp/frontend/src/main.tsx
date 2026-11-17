import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import '@xyflow/react/dist/style.css'
import './index.css'
import App from './App'
import { nameProjectsBy } from './api'
import { currentProject } from './address'

// Every request names the project of the open tab's page, as its address
// says.
nameProjectsBy(currentProject)

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <App />
  </StrictMode>,
)
