interface Props {
  configured: boolean
  ready: boolean
  running: boolean
  hasRun: boolean
  rerunState?: 'not_needed' | 'recommended' | 'required'
  onStart: () => void
  error: string | null
}

export default function PreflightPanel({
  configured,
  ready,
  running,
  hasRun,
  rerunState = 'required',
  onStart,
  error,
}: Props) {
  return (
    <section className="panel preflight">
      <div>
        <div className="panel__title">Preflight</div>
        <p className="hint">
          {ready
            ? 'Sorgente e destinazione connesse. Il preflight legge l’inventario (sola lettura) di entrambe.'
            : configured
              ? 'Testa e collega entrambi gli endpoint prima di avviare il preflight.'
              : 'Configura sorgente e destinazione per abilitare il preflight.'}
        </p>
        {error && <div className="state-msg state-msg--error">{error}</div>}
      </div>
      <div className="preflight__actions">
        {hasRun && rerunState === 'not_needed' && <div className="rerun-guidance rerun-guidance--ok"><strong>Non necessario</strong><span>Le evidenze correnti sono ancora valide.</span></div>}
        {hasRun && rerunState === 'recommended' && <div className="rerun-guidance rerun-guidance--warn"><strong>Consigliato</strong><span>Il job precedente richiede una nuova verifica.</span></div>}
        {hasRun && rerunState === 'required' && <div className="rerun-guidance rerun-guidance--danger"><strong>Obbligatorio</strong><span>Le modifiche a monte invalidano le fasi successive.</span></div>}
        {(!hasRun || rerunState !== 'not_needed') && <button
          className="btn btn--primary"
          onClick={onStart}
          disabled={!ready || running}
        >
          {running ? 'Avvio…' : hasRun ? 'Aggiorna preflight' : 'Avvia preflight'}
        </button>}
        {hasRun && rerunState === 'not_needed' && <details className="secondary-actions"><summary>Altre azioni</summary><button className="btn" onClick={onStart} disabled={!ready || running} type="button">Riesegui comunque</button><p>Usalo solo se i contenuti cPanel sono cambiati dopo l’ultima lettura.</p></details>}
      </div>
    </section>
  )
}
