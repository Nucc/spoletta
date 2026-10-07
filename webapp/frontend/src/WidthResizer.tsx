import { NodeResizeControl, ResizeControlVariant } from '@xyflow/react'
import { useBoardActions } from './actions'

// Must match minWidth and maxWidth in processes.go.
const MAX_WIDTH = 2000

// Lines along a card's left and right edges that drag its width. Only the
// width changes, so a card's height still follows its content, as an object
// growing with its states does.
export default function WidthResizer({ id, minWidth }: { id: string; minWidth: number }) {
  const actions = useBoardActions()
  if (!actions.editable || id.startsWith('draft-')) return null
  return (
    <>
      {(['left', 'right'] as const).map((position) => (
        <NodeResizeControl
          key={position}
          variant={ResizeControlVariant.Line}
          position={position}
          resizeDirection="horizontal"
          minWidth={minWidth}
          maxWidth={MAX_WIDTH}
          className="width-resizer"
          onResizeEnd={(_, p) => actions.resize(id, p.x, p.width)}
        />
      ))}
    </>
  )
}
