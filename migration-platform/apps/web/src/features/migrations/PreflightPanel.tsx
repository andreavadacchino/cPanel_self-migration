interface Props {
  configured: boolean
  ready: boolean
  running: boolean
  hasRun: boolean
  onStart: () => void
  error: string | null
}

export default function PreflightPanel({
  configured,
  ready,
  running,
  hasRun,
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
      <button
        className="btn btn--primary"
        onClick={onStart}
        disabled={!ready || running}
      >
        {running ? 'Avvio…' : hasRun ? 'Riesegui preflight' : 'Avvia preflight'}
      </button>
    </section>
  )
}
