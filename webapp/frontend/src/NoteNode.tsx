import { useEffect, useRef, useState } from 'react'
import Markdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { Handle, NodeResizer, NodeToolbar, Position, type Node, type NodeProps } from '@xyflow/react'
import { useBoardActions } from './actions'
import { NOTE_COLOURS, type NoteColour } from './api'
import { TrashIcon } from './icons'
import { MAC } from './Keyboard'
import { shortcutOf, shortcutText } from './shortcuts'

export type NoteNodeData = {
  text: string
  colour: NoteColour
  // Whether it points at a process or an object.
  pointing: boolean
  // Just added, so it opens for writing, and goes if left empty.
  fresh?: boolean
}

export type NoteNodeType = Node<NoteNodeData, 'note'>

// Must match the bounds in notes.go.
const MIN_WIDTH = 120
const MIN_HEIGHT = 60
const MAX_SIZE = 2000

// How long after typing stops the text is saved, as a panel saves its fields.
const SAVE_DELAY_MS = 500

const COLOUR_LABELS: Record<NoteColour, string> = {
  yellow: 'Yellow',
  blue: 'Blue',
  green: 'Green',
  pink: 'Pink',
  purple: 'Purple',
  grey: 'Grey',
}

// A note: Markdown on a coloured card of the size it is given. Double-clicked,
// it is written in place, saved as typing stops and once it loses focus;
// Escape or Cmd-Enter stops writing (noteDone in shortcuts.ts). Selected,
// it shows its colours above it and the corners that size it; the dot on
// its edge is dragged onto a process or an object to point at it.
export default function NoteNode({ id, data, selected }: NodeProps<NoteNodeType>) {
  const actions = useBoardActions()
  const editable = actions.editable && !actions.runShown
  const [writing, setWriting] = useState(Boolean(data.fresh) && editable)
  const [text, setText] = useState(data.text)
  const saved = useRef(data.text)
  const timer = useRef<number>(undefined)
  const ref = useRef<HTMLTextAreaElement>(null)

  // What another person wrote shows, unless this one is writing.
  useEffect(() => {
    if (!writing) {
      setText(data.text)
      saved.current = data.text
    }
  }, [data.text, writing])

  const save = (value: string) => {
    window.clearTimeout(timer.current)
    if (value === saved.current) return
    saved.current = value
    actions.setNoteText(id, value)
  }

  const stop = () => {
    save(text)
    setWriting(false)
    // One just added and left empty was added by mistake.
    if (data.fresh && text.trim() === '') actions.remove(id)
  }

  useEffect(() => {
    if (!writing) return
    // React Flow hides a new node until it has measured it, and hidden
    // fields can't take focus, so keep trying for a few frames.
    let tries = 0
    let frame = 0
    const focus = () => {
      const area = ref.current
      if (!area) return
      area.focus()
      if (document.activeElement !== area && ++tries < 10) frame = requestAnimationFrame(focus)
    }
    focus()
    return () => cancelAnimationFrame(frame)
  }, [writing])

  // What is typed and not saved yet is saved as the note goes.
  const latest = useRef({ text, save })
  latest.current = { text, save }
  useEffect(() => () => latest.current.save(latest.current.text), [])

  return (
    <div
      className={`note-node note-${data.colour}${selected ? ' selected' : ''}${writing ? ' writing' : ''}`}
      onDoubleClick={(e) => {
        if (!editable) return
        e.stopPropagation()
        setWriting(true)
      }}
    >
      <NodeResizer
        isVisible={selected && editable && !writing}
        minWidth={MIN_WIDTH}
        minHeight={MIN_HEIGHT}
        maxWidth={MAX_SIZE}
        maxHeight={MAX_SIZE}
        lineClassName="note-resizer-line"
        handleClassName="note-resizer-handle"
        onResizeEnd={(_, p) => actions.resizeNote(id, p)}
      />
      {editable && (
        <NodeToolbar isVisible={selected && !writing} position={Position.Top} className="note-toolbar">
          {NOTE_COLOURS.map((c) => (
            <button
              key={c}
              className={`note-swatch note-${c}${c === data.colour ? ' current' : ''}`}
              title={COLOUR_LABELS[c]}
              aria-label={COLOUR_LABELS[c]}
              aria-pressed={c === data.colour}
              onClick={() => actions.setNoteColour(id, c)}
            />
          ))}
          {data.pointing && (
            <button className="note-tool" title="Point at nothing" onClick={() => actions.pointNote(id, null)}>
              Unlink
            </button>
          )}
          <button className="note-tool" title="Delete the note" aria-label="Delete the note" onClick={() => actions.remove(id)}>
            <TrashIcon />
          </button>
        </NodeToolbar>
      )}
      <Handle
        type="source"
        position={Position.Right}
        className="note-handle"
        isConnectable={editable}
        title="Drag onto a process or an object to point at it"
      />
      {writing ? (
        <textarea
          ref={ref}
          className="note-text nodrag nowheel"
          value={text}
          placeholder={`Write in Markdown; ${shortcutText('noteDone', MAC)} when done`}
          aria-label="Note"
          onChange={(e) => {
            const value = e.target.value
            setText(value)
            window.clearTimeout(timer.current)
            timer.current = window.setTimeout(() => save(value), SAVE_DELAY_MS)
          }}
          onBlur={stop}
          onKeyDown={(e) => {
            if (e.nativeEvent.isComposing || shortcutOf(e, ['noteDone'], MAC, false) === null) return
            e.preventDefault()
            e.stopPropagation()
            ref.current?.blur()
          }}
        />
      ) : text.trim() === '' ? (
        <p className="note-empty">{editable ? 'Double-click to write a note' : 'An empty note'}</p>
      ) : (
        <div className="note-markdown data-markdown nowheel">
          <Markdown
            remarkPlugins={[remarkGfm]}
            components={{ a: ({ node: _, ...props }) => <a {...props} target="_blank" rel="noreferrer" /> }}
          >
            {text}
          </Markdown>
        </div>
      )}
    </div>
  )
}
