import type { ReactNode } from 'react'

// Small line icons drawn on a 16x16 grid in the current text color.
function Icon({ size = 16, children }: { size?: number; children: ReactNode }) {
  return (
    <svg
      viewBox="0 0 16 16"
      width={size}
      height={size}
      fill="none"
      stroke="currentColor"
      strokeWidth="1.5"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      {children}
    </svg>
  )
}

export const AgentIcon = () => (
  <Icon>
    <path d="M8 1.5l1.6 4.2 4.4 1.6-4.4 1.6L8 13.1 6.4 8.9 2 7.3l4.4-1.6z" />
    <path d="M13 11.5v3M11.5 13h3" />
  </Icon>
)

export const ScriptIcon = () => (
  <Icon>
    <path d="M5.5 4.5L2 8l3.5 3.5M10.5 4.5L14 8l-3.5 3.5" />
  </Icon>
)

export const HumanIcon = () => (
  <Icon>
    <circle cx="8" cy="5" r="2.75" />
    <path d="M2.75 14.25c.6-2.9 2.7-4.5 5.25-4.5s4.65 1.6 5.25 4.5" />
  </Icon>
)

export const ObjectIcon = () => (
  <Icon>
    <rect x="2.5" y="2.5" width="11" height="11" rx="1" />
    <path d="M5.5 6h5M5.5 8.5h5M5.5 11h3" />
  </Icon>
)

// OPM's exhibition-characterization symbol: a triangle with a smaller,
// filled one inside, linking a thing to the attributes it exhibits.
export const ExhibitionIcon = ({ size = 12 }: { size?: number }) => (
  <Icon size={size}>
    <path d="M8 2.5L14 13.5H2z" />
    <path d="M8 7.5l2.6 4.5H5.4z" fill="currentColor" strokeWidth="1" />
  </Icon>
)

export const PlayIcon = () => (
  <svg viewBox="0 0 16 16" width="14" height="14" aria-hidden="true">
    <path d="M4 2.5v11l9-5.5z" fill="currentColor" />
  </svg>
)

export const GearIcon = () => (
  <Icon>
    <circle cx="8" cy="8" r="2" />
    <path d="M8 1.75v1.5M8 12.75v1.5M14.25 8h-1.5M3.25 8h-1.5M12.4 3.6l-1.06 1.06M4.66 11.34L3.6 12.4M12.4 12.4l-1.06-1.06M4.66 4.66L3.6 3.6" />
  </Icon>
)

export const TrashIcon = () => (
  <Icon>
    <path d="M2.5 4.25h11M6.25 4.25v-1.5h3.5v1.5M4 4.25l.6 9h6.8l.6-9M6.75 7v4M9.25 7v4" />
  </Icon>
)

export const PlusIcon = ({ size = 12 }: { size?: number }) => (
  <Icon size={size}>
    <path d="M8 3v10M3 8h10" />
  </Icon>
)

export const PencilIcon = () => (
  <Icon>
    <path d="M10.5 2.5l3 3-8 8H2.5v-3z" />
  </Icon>
)

export const CopyIcon = () => (
  <Icon>
    <rect x="5.5" y="5.5" width="8" height="8" rx="1.5" />
    <path d="M3.5 10.5h-.25a.75.75 0 01-.75-.75V3.25a.75.75 0 01.75-.75h6.5a.75.75 0 01.75.75v.25" />
  </Icon>
)

export const LayoutIcon = () => (
  <Icon>
    <rect x="5.5" y="1.75" width="5" height="3.5" rx="1" />
    <rect x="1.75" y="10.75" width="5" height="3.5" rx="1" />
    <rect x="9.25" y="10.75" width="5" height="3.5" rx="1" />
    <path d="M8 5.25V8M4.25 10.75V8h7.5v2.75" />
  </Icon>
)

export const SaveIcon = () => (
  <Icon>
    <path d="M2.75 2.75h8.5l2 2v8.5h-10.5z" />
    <path d="M5.25 2.75v3h5v-3M5.25 13.25v-4h5.5v4" />
  </Icon>
)

export const HistoryIcon = () => (
  <Icon>
    <path d="M2.5 8a5.5 5.5 0 1 0 1.6-3.9" />
    <path d="M2.25 2.5v2.75H5M8 5v3l2 1.5" />
  </Icon>
)
