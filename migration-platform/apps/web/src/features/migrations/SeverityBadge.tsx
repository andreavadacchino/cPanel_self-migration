import type { Severity } from '../../lib/api'

const LABEL: Record<Severity, string> = {
  blocker: 'Blocco critico',
  warning: 'Avviso',
  info: 'Informazione',
}

export default function SeverityBadge({ severity }: { severity: Severity }) {
  return (
    <span className={`sev-badge sev-badge--${severity}`}>{LABEL[severity]}</span>
  )
}
