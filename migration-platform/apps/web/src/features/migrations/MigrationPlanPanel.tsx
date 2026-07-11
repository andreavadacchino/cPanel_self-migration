import { useEffect, useState } from 'react'
import { fetchMigrationPlan, generateMigrationPlan, type MigrationPlan } from '../../lib/api'

const MODE: Record<string, string> = {
  automatic: 'Automatico', approval: 'Da approvare', secret_required: 'Nuova password',
  manual: 'Manuale', excluded: 'Escluso',
}

export default function MigrationPlanPanel({ migrationId, onPlanChanged }: { migrationId: number; onPlanChanged?: (plan: MigrationPlan) => void }) {
  const [plan, setPlan] = useState<MigrationPlan | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => { void fetchMigrationPlan(migrationId).then(setPlan) }, [migrationId])

  async function generate() {
    setLoading(true); setError(null)
    try {
      const generated = await generateMigrationPlan(migrationId)
      setPlan(generated)
      onPlanChanged?.(generated)
    }
    catch (err) { setError(err instanceof Error ? err.message : 'Errore piano') }
    finally { setLoading(false) }
  }

  return <section className="panel">
    <div className="panel__head">
      <div><div className="panel__title">Piano di migrazione</div><p className="hint">Classifica le differenze senza eseguire scritture.</p></div>
      <button className="btn btn--primary" onClick={() => void generate()} disabled={loading}>{loading ? 'Generazione…' : plan ? 'Aggiorna piano' : 'Genera piano'}</button>
    </div>
    {error && <div className="state-msg state-msg--error">{error}</div>}
    {plan && <>
      <div className="summary-cards">
        {['automatic','approval','secret_required','manual','excluded'].map((mode) => <div className="summary-card" key={mode}><div className="summary-card__value">{plan.summary[mode] ?? 0}</div><div className="summary-card__label">{MODE[mode]}</div></div>)}
      </div>
      <div className="cmp-table-wrap"><table className="cmp-table"><thead><tr><th>Modalità</th><th>Categoria</th><th>Elemento</th><th>Motivo</th></tr></thead><tbody>
        {plan.steps.map((step) => <tr key={step.id}><td><span className="badge">{MODE[step.mode]}</span></td><td>{step.category}</td><td className="cmp-table__key">{step.key}</td><td>{step.reason}</td></tr>)}
      </tbody></table></div>
    </>}
  </section>
}
