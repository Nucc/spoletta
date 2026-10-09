import { useRef, useState } from 'react'
import { parseQuantity, QUANTITY_HINT, quantityText, type Quantity } from './quantity'

// How many of an object there are, typed as its links write it. It saves
// when left or on Enter, and goes back to what was saved on Escape or when
// the server turns it down; what isn't a quantity is marked and not saved.
// Mount it with key set to the quantity's text, so a change made elsewhere
// shows.
export default function QuantityInput({
  quantity,
  disabled,
  autoFocus,
  className,
  onChange,
  onDone,
}: {
  quantity: Quantity
  disabled?: boolean
  autoFocus?: boolean
  className?: string
  // Resolves to whether the server took it.
  onChange: (quantity: Quantity) => Promise<boolean>
  // Called once saved, or given up on.
  onDone?: () => void
}) {
  const saved = quantityText(quantity)
  const [text, setText] = useState(saved)
  const parsed = parseQuantity(text)
  // Escape leaves it without saving, though it then loses focus.
  const cancelled = useRef(false)

  const commit = async () => {
    if (cancelled.current || !parsed || quantityText(parsed) === saved) {
      cancelled.current = false
      setText(saved)
      onDone?.()
      return
    }
    if (!(await onChange(parsed))) setText(saved)
    onDone?.()
  }

  return (
    <input
      className={`quantity-input${parsed ? '' : ' invalid'}${className ? ` ${className}` : ''}`}
      value={text}
      disabled={disabled}
      autoFocus={autoFocus}
      aria-label="How many"
      aria-invalid={!parsed}
      title={parsed ? QUANTITY_HINT : `Not a quantity; write ${QUANTITY_HINT}`}
      placeholder="1"
      onChange={(e) => setText(e.target.value)}
      onBlur={commit}
      onKeyDown={(e) => {
        e.stopPropagation()
        if (e.key === 'Enter') e.currentTarget.blur()
        if (e.key === 'Escape') {
          cancelled.current = true
          e.currentTarget.blur()
        }
      }}
    />
  )
}
