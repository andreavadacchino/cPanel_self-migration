import { useEffect, useState } from 'react'
import {
  fetchPlan,
  generatePlan,
  type MigrationPlan,
  type PlanItem,
  type PlanSections,
} from '../../lib/api'

// Order and copy of the plan sections. Each maps to a key in PlanSections.
const SECTIONS: {
  key: keyof PlanSections
  label: string
  hint: string
  tone: 'blocker' | 'warning' | 'info' | 'ok'
}[] = [
  {
    key: 'blockers',
    label: 'Blocchi critici',
    hint: 'Vanno risolti prima di una futura migrazione.',
    tone: 'blocker',
  },
  {
    key: 'manual_tasks',
    label: 'Attività manuali',
    hint: 'Da eseguire a mano: non vengono automatizzate.',
    tone: 'warning',
  },
  {
    key: 'unknowns',
    label: 'Sconosciuti — da verificare',
    hint: 'Non leggibili in automatico: verifica manuale necessaria.',
    tone: 'warning',
  },
  {
    key: 'warnings',
    label: 'Avvisi',
    hint: 'Differenze non bloccanti da tenere presenti.',
    tone: 'info',
  },
  {
    key: 'ready_steps',
    label: 'Già allineati',
    hint: 'Risultano coerenti tra sorgente e destinazione.',
    tone: 'ok',
  },
  {
    key: 'cutover_notes',
    label: 'Note per il cutover',
    hint: 'Promemoria operativi per il momento dello switch.',
    tone: 'info',
  },
]

const STATUS_LABEL: Record<string, string> = {
  blocked: 'Bloccato',
  ready_for_review: 'Pronto per revisione',
  failed: 'Errore',
}

function PlanSectionBlock({
  label,
  hint,
  tone,
  items,
}: {
  label: string
  hint: string
  tone: string
  items: PlanItem[]
}) {
  if (items.length === 0) return null
  return (
    <div className={`plan-section plan-section--${tone}`}>
      <h3 className="plan-section__title">
        {label} <span className="plan-section__count">{items.length}</span>
      </h3>
      <p className="hint">{hint}</p>
      <ul className="plan-section__list">
        {items.map((item, i) => (
          <li className="plan-item" key={`${item.category}-${item.key}-${i}`}>
            <span className="plan-item__title">{item.title}</span>
            <span className="plan-item__message">{item.message}</span>
          </li>
        ))}
      </ul>
    </div>
  )
}

export default function MigrationPlanPanel({
  migrationId,
}: {
  migrationId: number
}) {
  const [plan, setPlan] = useState<MigrationPlan | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    let active = true
    // fetchPlan returns null only on 404 (no plan yet); any other failure
    // (500, network, …) rejects and must be shown, not silently swallowed.
    fetchPlan(migrationId)
      .then((p) => {
        if (active) setPlan(p)
      })
      .catch((err: unknown) => {
        if (active)
          setError(err instanceof Error ? err.message : 'Errore sconosciuto')
      })
    return () => {
      active = false
    }
  }, [migrationId])

  async function handleGenerate() {
    setLoading(true)
    setError(null)
    try {
      setPlan(await generatePlan(migrationId))
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Errore sconosciuto')
    } finally {
      setLoading(false)
    }
  }

  const summary = plan?.summary
  const sections = plan?.sections

  return (
    <section className="panel">
      <div className="panel__head">
        <div className="panel__title">Piano di migrazione (sola lettura)</div>
        <button
          className="btn btn--primary"
          onClick={handleGenerate}
          disabled={loading}
        >
          {loading ? 'Generazione…' : 'Genera piano'}
        </button>
      </div>

      <p className="hint">
        Questo piano è read-only. Non esegue modifiche sui server.
      </p>

      {error && <div className="state-msg state-msg--error">{error}</div>}

      {!plan && !error && (
        <p className="hint">
          Genera un piano operativo a partire dall'ultima comparativa: mostra
          cosa è già allineato, cosa manca, cosa blocca, cosa richiede intervento
          manuale e cosa non è verificabile. Serve una comparativa generata.
        </p>
      )}

      {plan && (
        <>
          <div className="plan-status">
            <span className={`badge badge--${plan.status}`}>
              {STATUS_LABEL[plan.status] ?? plan.status}
            </span>
          </div>

          {summary && (
            <dl className="plan-summary">
              <div>
                <dt>Blocchi</dt>
                <dd>{summary.blockers_count}</dd>
              </div>
              <div>
                <dt>Attività manuali</dt>
                <dd>{summary.manual_tasks_count}</dd>
              </div>
              <div>
                <dt>Sconosciuti</dt>
                <dd>{summary.unknowns_count}</dd>
              </div>
              <div>
                <dt>Avvisi</dt>
                <dd>{summary.warnings_count}</dd>
              </div>
              <div>
                <dt>Allineati</dt>
                <dd>{summary.ready_steps_count}</dd>
              </div>
            </dl>
          )}

          {sections &&
            SECTIONS.map((s) => (
              <PlanSectionBlock
                key={s.key}
                label={s.label}
                hint={s.hint}
                tone={s.tone}
                items={sections[s.key] ?? []}
              />
            ))}
        </>
      )}
    </section>
  )
}
