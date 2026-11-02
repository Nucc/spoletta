import type { ReactNode } from 'react'
import { useHelp } from './Hints'

// The head of each part of the settings: its name, what it is for in a
// sentence, and the button adding to it on the right. The rest of what it
// means, how it works and what it takes, is folded behind a ? beside the
// name, as a panel's settings fold theirs (useHelp), so the part opens on
// what it holds rather than on its help. What is folded shows in a
// paragraph, so it holds text and code, never paragraphs or lists.
export function SettingsHead({
  title,
  lead,
  more,
  action,
  level = 2,
  id,
}: {
  title: string
  lead?: ReactNode
  more?: ReactNode
  action?: ReactNode
  level?: 2 | 3
  // The heading's, for a control it names.
  id?: string
}) {
  const help = useHelp()
  const Heading = level === 2 ? 'h2' : 'h3'
  return (
    <header className={level === 2 ? 'settings-head' : 'settings-head sub'}>
      <div className="settings-head-row">
        <Heading id={id}>{title}</Heading>
        {more && help.button('more', title)}
        {action && <span className="settings-head-action">{action}</span>}
      </div>
      {lead && <p className="settings-lead">{lead}</p>}
      {more && help.note('more', more)}
    </header>
  )
}

// What a part of the settings with nothing in it says in place of its
// list: what would go there and why, and the button adding the first.
export function SettingsEmpty({ children, action }: { children: ReactNode; action?: ReactNode }) {
  return (
    <div className="settings-empty">
      <p>{children}</p>
      {action}
    </div>
  )
}
