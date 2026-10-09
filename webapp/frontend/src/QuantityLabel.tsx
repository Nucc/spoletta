import { useState } from 'react'
import { EdgeLabelRenderer, Position, useNodesData } from '@xyflow/react'
import { useBoardActions } from './actions'
import type { ObjectNodeType } from './ObjectNode'
import { quantityText, quantityWords } from './quantity'
import QuantityInput from './QuantityInput'

// How far from the object's end of a link its quantity sits, along the link
// and beside it.
const OFFSET = { along: 16, beside: 14 }

// The quantity of an object at its end of a link, as OPM writes a link's
// multiplicity: 1, 4, 2..5, 1..* or *. Clicking it types another.
export default function QuantityLabel({ objectId, x, y, side }: { objectId: string; x: number; y: number; side: Position }) {
  const actions = useBoardActions()
  const object = useNodesData<ObjectNodeType>(objectId)
  const [editing, setEditing] = useState(false)
  if (!object || object.type !== 'object') return null
  const quantity = object.data
  const words = quantityWords(quantity)
  const at = beside(x, y, side)
  return (
    <EdgeLabelRenderer>
      <div className="quantity-label nodrag nopan" style={{ transform: `translate(-50%, -50%) translate(${at.x}px, ${at.y}px)` }}>
        {editing ? (
          <QuantityInput
            quantity={quantity}
            autoFocus
            onChange={(q) => actions.setQuantity(objectId, q)}
            onDone={() => setEditing(false)}
          />
        ) : (
          <button
            className="quantity-pill"
            disabled={!actions.editable}
            title={`${quantity.many ? `A list of ${words ? `${words} ` : 'any number of '}items` : 'One object'}${actions.editable ? '; click to change how many' : ''}`}
            onClick={() => setEditing(true)}
          >
            {quantityText(quantity)}
          </button>
        )}
      </div>
    </EdgeLabelRenderer>
  )
}

// Beside the link, a little away from where it meets the object on side.
function beside(x: number, y: number, side: Position): { x: number; y: number } {
  switch (side) {
    case Position.Top:
      return { x: x + OFFSET.beside, y: y - OFFSET.along }
    case Position.Bottom:
      return { x: x + OFFSET.beside, y: y + OFFSET.along }
    case Position.Left:
      return { x: x - OFFSET.along, y: y - OFFSET.beside }
    default:
      return { x: x + OFFSET.along, y: y - OFFSET.beside }
  }
}
