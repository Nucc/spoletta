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

// Issues coming in from a source: an arrow into a tray.
export const SentryIcon = () => (
  <Icon>
    <path d="M8 1.75v7M5 6l3 3 3-3" />
    <path d="M1.75 9.5v3.25c0 .55.45 1 1 1h10.5c.55 0 1-.45 1-1V9.5" />
  </Icon>
)

// A plug, for a process carried out by a connector built into the webapp.
export const ConnectorIcon = () => (
  <Icon>
    <path d="M5.5 1.75v3M10.5 1.75v3" />
    <path d="M3.75 4.75h8.5v2.5a4.25 4.25 0 0 1-8.5 0z" />
    <path d="M8 11.5v2.75" />
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

// A database: a stack of disks, on a persistent object.
export const DatabaseIcon = ({ size = 12 }: { size?: number }) => (
  <Icon size={size}>
    <ellipse cx="8" cy="3.75" rx="5.25" ry="2" />
    <path d="M2.75 3.75v8.5c0 1.1 2.35 2 5.25 2s5.25-.9 5.25-2v-8.5" />
    <path d="M2.75 8c0 1.1 2.35 2 5.25 2s5.25-.9 5.25-2" />
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

// OPM's aggregation-participation symbol: a filled triangle, linking a whole
// to the parts it aggregates.
export const AggregationIcon = ({ size = 12 }: { size?: number }) => (
  <Icon size={size}>
    <path d="M8 2.5L14 13.5H2z" fill="currentColor" />
  </Icon>
)

// OPM's generalization-specialization symbol: an empty triangle, linking a
// general to the objects specializing it.
export const GeneralizationIcon = ({ size = 12 }: { size?: number }) => (
  <Icon size={size}>
    <path d="M8 2.5L14 13.5H2z" />
  </Icon>
)

// The app's mark: a start node feeding a process, which hands its result
// on to the next step, drawn white on a filled tile. public/favicon.svg
// is the same drawing.
export const FlowBuilderIcon = ({ size = 24 }: { size?: number }) => (
  <svg viewBox="0 0 24 24" width={size} height={size} aria-hidden="true">
    <rect width="24" height="24" rx="6" fill="#0969da" />
    <g fill="none" stroke="#fff" strokeWidth="1.75" strokeLinecap="round" strokeLinejoin="round">
      <path d="M8.5 7.5h4M15.5 10v6.5h-4" />
      <rect x="12.5" y="5" width="6" height="5" rx="1.5" />
      <rect x="5.5" y="14" width="6" height="5" rx="1.5" />
    </g>
    <circle cx="6.5" cy="7.5" r="2" fill="#fff" />
  </svg>
)

// A service's own logo, filled on a 24x24 grid in the current text color.
function BrandIcon({ size = 20, children }: { size?: number; children: ReactNode }) {
  return (
    <svg viewBox="0 0 24 24" width={size} height={size} fill="currentColor" aria-hidden="true">
      {children}
    </svg>
  )
}

// From Simple Icons (CC0).
export const SentryLogo = ({ size }: { size?: number }) => (
  <BrandIcon size={size}>
    <path d="M13.91 2.505c-.873-1.448-2.972-1.448-3.844 0L6.904 7.92a15.478 15.478 0 0 1 8.53 12.811h-2.221A13.301 13.301 0 0 0 5.784 9.814l-2.926 5.06a7.65 7.65 0 0 1 4.435 5.848H2.194a.365.365 0 0 1-.298-.534l1.413-2.402a5.16 5.16 0 0 0-1.614-.913L.296 19.275a2.182 2.182 0 0 0 .812 2.999 2.24 2.24 0 0 0 1.086.288h6.983a9.322 9.322 0 0 0-3.845-8.318l1.11-1.922a11.47 11.47 0 0 1 4.95 10.24h5.915a17.242 17.242 0 0 0-7.885-15.28l2.244-3.845a.37.37 0 0 1 .504-.13c.255.14 9.75 16.708 9.928 16.9a.365.365 0 0 1-.327.543h-2.287c.029.612.029 1.223 0 1.831h2.297a2.206 2.206 0 0 0 1.922-3.31z" />
  </BrandIcon>
)

export const GitHubLogo = ({ size }: { size?: number }) => (
  <BrandIcon size={size}>
    <path d="M12 .297c-6.63 0-12 5.373-12 12 0 5.303 3.438 9.8 8.205 11.385.6.113.82-.258.82-.577 0-.285-.01-1.04-.015-2.04-3.338.724-4.042-1.61-4.042-1.61C4.422 18.07 3.633 17.7 3.633 17.7c-1.087-.744.084-.729.084-.729 1.205.084 1.838 1.236 1.838 1.236 1.07 1.835 2.809 1.305 3.495.998.108-.776.417-1.305.76-1.605-2.665-.3-5.466-1.332-5.466-5.93 0-1.31.465-2.38 1.235-3.22-.135-.303-.54-1.523.105-3.176 0 0 1.005-.322 3.3 1.23.96-.267 1.98-.399 3-.405 1.02.006 2.04.138 3 .405 2.28-1.552 3.285-1.23 3.285-1.23.645 1.653.24 2.873.12 3.176.765.84 1.23 1.91 1.23 3.22 0 4.61-2.805 5.625-5.475 5.92.42.36.81 1.096.81 2.22 0 1.606-.015 2.896-.015 3.286 0 .315.21.69.825.57C20.565 22.092 24 17.592 24 12.297c0-6.627-5.373-12-12-12" />
  </BrandIcon>
)

export const ChevronIcon = () => (
  <Icon size={12}>
    <path d="M6 3.5L10.5 8 6 12.5" />
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

export const ClockIcon = () => (
  <Icon>
    <circle cx="8" cy="8" r="6" />
    <path d="M8 4.75V8l2.25 1.5" />
  </Icon>
)

export const CodeIcon = () => (
  <Icon>
    <path d="M5.5 4.5L2 8l3.5 3.5M10.5 4.5L14 8l-3.5 3.5" />
  </Icon>
)

export const DownloadIcon = () => (
  <Icon>
    <path d="M8 2v8M4.75 6.75L8 10l3.25-3.25M2.5 12.5v1h11v-1" />
  </Icon>
)

export const UploadIcon = () => (
  <Icon>
    <path d="M8 10V2M4.75 5.25L8 2l3.25 3.25M2.5 12.5v1h11v-1" />
  </Icon>
)

export const TrashIcon = () => (
  <Icon>
    <path d="M2.5 4.25h11M6.25 4.25v-1.5h3.5v1.5M4 4.25l.6 9h6.8l.6-9M6.75 7v4M9.25 7v4" />
  </Icon>
)

export const RerunIcon = () => (
  <Icon>
    <path d="M13 8a5 5 0 1 1-1.46-3.54M13 2.5v3h-3" />
  </Icon>
)

export const StopIcon = () => (
  <Icon>
    <rect x="3.5" y="3.5" width="9" height="9" rx="1.5" />
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

// A clipboard, for pasting what was copied.
export const PasteIcon = () => (
  <Icon>
    <path d="M5.5 2.75H4a1 1 0 00-1 1v9.5a1 1 0 001 1h8a1 1 0 001-1v-9.5a1 1 0 00-1-1h-1.5" />
    <rect x="5.5" y="1.5" width="5" height="2.5" rx=".75" />
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

// Processes inside a process: OPM's in-zoomed process.
export const FlowIcon = () => (
  <Icon>
    <rect x="1.5" y="2.5" width="13" height="11" rx="5.5" />
    <circle cx="5.5" cy="8" r="1.5" />
    <circle cx="10.5" cy="8" r="1.5" />
  </Icon>
)

// A flow kept elsewhere: a node leading to two steps.
export const SubflowIcon = () => (
  <Icon>
    <circle cx="4" cy="4" r="2" />
    <rect x="9.5" y="2.25" width="5" height="3.5" rx="1" />
    <rect x="9.5" y="10.25" width="5" height="3.5" rx="1" />
    <path d="M6 4h3.5M4 6v6h5.5" />
  </Icon>
)

// UML activity final node: a ring around a dot.
export const EndIcon = () => (
  <Icon>
    <circle cx="8" cy="8" r="6" />
    <circle cx="8" cy="8" r="3" fill="currentColor" />
  </Icon>
)

export const ZoomInIcon = () => (
  <Icon>
    <circle cx="7" cy="7" r="4.5" />
    <path d="M10.5 10.5l4 4M5 7h4M7 5v4" />
  </Icon>
)

// A plug: what connects agents to MCP servers.
export const PlugIcon = () => (
  <Icon>
    <path d="M6 1.75v3M10 1.75v3" />
    <path d="M4 4.75h8v2.5a4 4 0 0 1-8 0z" />
    <path d="M8 11.25v3" />
  </Icon>
)

// A speech bubble with a spark: the assistant to talk to.
export const AssistantIcon = ({ size = 16 }: { size?: number }) => (
  <Icon size={size}>
    <path d="M2.5 3.5a1.5 1.5 0 0 1 1.5-1.5h8a1.5 1.5 0 0 1 1.5 1.5v6a1.5 1.5 0 0 1-1.5 1.5H7l-3 2.5V11H4a1.5 1.5 0 0 1-1.5-1.5z" />
    <path d="M8 4.25l.6 1.65 1.65.6-1.65.6L8 8.75l-.6-1.65-1.65-.6 1.65-.6z" />
  </Icon>
)

// A table of rows and columns: what a flow's runs held.
export const TableIcon = () => (
  <Icon>
    <rect x="1.5" y="2.5" width="13" height="11" rx="1.5" />
    <path d="M1.5 6.5h13M1.5 10h13M6 6.5v7" />
  </Icon>
)
