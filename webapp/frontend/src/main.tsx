import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { ReactFlowProvider } from '@xyflow/react'
import '@xyflow/react/dist/style.css'
import './index.css'
import Board from './Board'

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <ReactFlowProvider>
      <Board />
    </ReactFlowProvider>
  </StrictMode>,
)
