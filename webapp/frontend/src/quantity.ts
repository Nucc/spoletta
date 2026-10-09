// How many of an object there are, as OPM writes a link's multiplicity at
// the object's end, the same way the server reads it (see items.go): one
// object, or a list of items holding at least min of them, and at most max
// unless it is null. Runs check that a list has as many as it says.
export type Quantity = { many: boolean; min: number; max: number | null }

// The quantity as its links write it: 1, 4, 2..5, 1..* or * for any number.
export function quantityText({ many, min, max }: Quantity): string {
  if (!many) return '1'
  if (max === null) return min === 0 ? '*' : `${min}..*`
  return max === min ? String(min) : `${min}..${max}`
}

// How many items a list must have, in words, or empty for any number.
export function quantityWords({ many, min, max }: Quantity): string {
  if (!many) return 'one'
  if (max === null) return min === 0 ? '' : `at least ${min}`
  if (max === min) return `exactly ${min}`
  return min === 0 ? `at most ${max}` : `between ${min} and ${max}`
}

const count = (s: string): number | null => (/^\d+$/.test(s) ? Number(s) : null)

// The quantity written as quantityText writes it, or with many for *; null
// when it isn't one, or is a list of at most one item, which would read as one
// object.
export function parseQuantity(text: string): Quantity | null {
  const t = text.trim().toLowerCase().replace(/\s+/g, '')
  if (t === '1') return { many: false, min: 0, max: null }
  if (t === '*' || t === 'many' || t === 'n') return { many: true, min: 0, max: null }
  const [a, b, ...rest] = t.split('..')
  if (rest.length > 0) return null
  const min = count(a)
  if (min === null) return null
  const max = b === undefined ? min : b === '*' || b === 'n' ? null : count(b)
  if (b !== undefined && b !== '*' && b !== 'n' && max === null) return null
  if (max !== null && (max < 2 || max < min)) return null
  return { many: true, min, max }
}

// What to write instead of text that isn't a quantity.
export const QUANTITY_HINT = '1, a number such as 4, a range such as 2..5, at least as in 1..*, or * for any number'
