import type { ComparisonEntryState } from '../../lib/api'

const LABEL: Record<ComparisonEntryState, string> = {
  match: 'Allineato',
  missing_on_destination: 'Manca sulla destinazione',
  only_on_destination: 'Presente solo sulla destinazione',
  different: 'Differente',
  unknown: 'Non verificabile',
}

export default function StateBadge({ state }: { state: ComparisonEntryState }) {
  return <span className="state-badge">{LABEL[state] ?? state}</span>
}
