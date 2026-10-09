import { BaseEdge, getSmoothStepPath, Position, type Edge, type EdgeProps } from '@xyflow/react'

// The OPM structural link an edge draws on an unfolded object's board.
export type StructureKind = 'exhibition' | 'aggregation' | 'generalization'

export type StructureEdgeData = { kind: StructureKind }

export type StructureEdgeType = Edge<StructureEdgeData, 'structure'>

// The triangle's height and half its base.
const TRIANGLE = { height: 16, half: 9 }

// An OPM structural link: from the whole, or the general, down to what makes
// it up, or what specializes it. Its triangle sits under the whole, its apex
// towards it: filled for a part it aggregates, with a filled triangle inside
// for an attribute it exhibits, and empty for a specialization.
export default function StructureEdge({ sourceX, sourceY, targetX, targetY, targetPosition, data }: EdgeProps<StructureEdgeType>) {
  const kind = data?.kind ?? 'exhibition'
  const base = sourceY + TRIANGLE.height
  const [path] = getSmoothStepPath({
    sourceX,
    sourceY: base,
    sourcePosition: Position.Bottom,
    targetX,
    targetY,
    targetPosition,
    borderRadius: 8,
  })
  const outline = `M${sourceX},${sourceY} L${sourceX + TRIANGLE.half},${base} L${sourceX - TRIANGLE.half},${base} Z`
  const inner = `M${sourceX},${sourceY + 7} L${sourceX + 4.5},${base - 2.5} L${sourceX - 4.5},${base - 2.5} Z`
  return (
    <>
      <BaseEdge path={path} className="structure-path" />
      <path d={outline} className={`structure-symbol ${kind}`} />
      {kind === 'exhibition' && <path d={inner} className="structure-symbol-inner" />}
    </>
  )
}
