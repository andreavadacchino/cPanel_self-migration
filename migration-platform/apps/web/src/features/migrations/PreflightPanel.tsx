interface Props {
  ready: boolean
  running: boolean
  onStart: () => void
  error: string | null
}

export default function PreflightPanel({
  ready,
  running,
  onStart,
  error,
}: Props) {
  return (
    <section className="panel preflight">
      <div>
        <div className="panel__title">Preflight</div>
        <p className="hint">
          {ready
            ? 'Sorgente e destinazione configurate. Il preflight legge l’inventario (sola lettura) di entrambe.'
            : 'Configura sorgente e destinazione per abilitare il preflight.'}
        </p>
        {error && <div className="state-msg state-msg--error">{error}</div>}
      </div>
      <button
        className="btn btn--primary"
        onClick={onStart}
        disabled={!ready || running}
      >
        {running ? 'Avvio…' : 'Avvia preflight'}
      </button>
    </section>
  )
}
