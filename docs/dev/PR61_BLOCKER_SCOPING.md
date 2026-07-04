# PR 61 — Blocker Scoping Design

## Problema

Dogfooding #1 ha dimostrato che `POL-DNS-NS-CHANGED` blocca l'INTERO
ciclo (overall=BLOCKED) anche quando l'operatore vuole solo applicare le
configurazioni email/cron/dns al dest. La condizione (NS diversi) è
ATTESA pre-cutover e si risolve naturalmente con `synczone`.

## Design decision

I blocker della policy acquisiscono uno **scope**:
- `blocks_apply`: condizioni che rendono PERICOLOSO scrivere config ORA
- `blocks_cutover`: condizioni che diventano vere solo allo switch del traffico

**Principio**: l'exec handler rifiuta apply/content actions solo se ci sono
`blocks_apply` irrisolti. Il `ready_for_cutover` resta gateato da TUTTI
i blocker (sia apply che cutover). La governance non si svuota mai.

## Classificazione per-regola

### blocks_apply (2 regole)

| Rule ID | Motivazione |
|---------|-------------|
| `POL-DOMAIN-MAIN-REMOVED` | Main domain assente su dest = account broken, cPanel ops fallirebbero |
| `POL-DNS-ZONE-REMOVED` | Zona inesistente su dest = dns apply non ha target |

### blocks_cutover (8 regole)

| Rule ID | Motivazione |
|---------|-------------|
| `POL-DNS-NS-CHANGED` | NS divergono = cluster-managed, resolves at synczone |
| `POL-DNS-NS-REMOVED` | NS assente = atteso, il peer non ha ancora i NS source |
| `POL-DNS-MX-CHANGED` | MX routing differente = il piano gestisce il replace |
| `POL-DNS-MX-REMOVED` | MX assente su dest = il piano lo aggiunge |
| `POL-MAILBOX-REMOVED` | Mailbox assente = creata dal migration runner |
| `POL-DB-REMOVED` | Database assente = creato dal migration runner |
| `POL-SSL-REMOVED` | Cert assente = AutoSSL post-cutover |
| `POL-CRON-ENABLED-REMOVED` | Cron assente = cron apply lo crea (è il suo scopo) |

### Default conservativo

Qualsiasi NUOVA regola policy aggiunta in futuro con severity=blocker
che NON appare nella tabella `blocks_cutover` è automaticamente
`blocks_apply` (default conservativo).

## Implementazione

### Dove: lookup table in checklist.go (NOT policy.go)

Lo scope NON va su `PolicyFinding` — la policy resta pura (valuta rischio
senza sapere del workflow). Lo scoping è un concetto del CHECKLIST layer
che sa come la governance orchestra il ciclo.

```go
// blockerScopeTable classifies blocker-severity policy rules.
// Rules NOT listed here default to "apply" (conservative).
var blockerScopeTable = map[string]string{
    "POL-DNS-NS-CHANGED":      "cutover",
    "POL-DNS-NS-REMOVED":      "cutover",
    "POL-DNS-MX-CHANGED":      "cutover",
    "POL-DNS-MX-REMOVED":      "cutover",
    "POL-MAILBOX-REMOVED":     "cutover",
    "POL-DB-REMOVED":          "cutover",
    "POL-SSL-REMOVED":         "cutover",
    "POL-CRON-ENABLED-REMOVED":"cutover",
}
```

### Campi aggiunti alla ChecklistSection

```go
type ChecklistSection struct {
    // ... campi esistenti ...
    BlockersApply   []string `json:"blockers_apply"`
    BlockersCutover []string `json:"blockers_cutover"`
}
```

Il campo `Blockers []string` esistente resta (backward compat) come union.

### Campo aggiunto a MigrationChecklist

```go
type MigrationChecklist struct {
    // ... campi esistenti ...
    ApplyBlocked bool `json:"apply_blocked"`
}
```

`ApplyBlocked = true` se QUALSIASI sezione ha `len(BlockersApply) > 0`.

### Exec handler gate

In `handleExec`, PRIMA di lanciare un'azione write:
1. Legge `migration_checklist.json` dal dir
2. Se `ApplyBlocked == true` → HTTP 403 "apply is blocked by policy"
3. Se `ApplyBlocked == false` → procede normalmente

Le azioni read-only (verify, plans) NON sono gatate dal checklist.

### ready_for_cutover invariato

`tryAutoTransitionReadyForCutover` continua a richiedere:
- All 3 verify reports CLEAN
- Il session status == verification_required

Il campo `OverallStatus` della checklist continua a usare TUTTI i
blocker (apply + cutover) per determinare BLOCKED. Ma l'exec handler
usa SOLO `ApplyBlocked`.

## Cosa NON cambia

- policy.go (zero modifiche al motore di policy)
- PolicyFinding struct (zero nuovi campi)
- OverallStatus logic (BLOCKED se qualsiasi blocker, backward compat)
- ManualAction flow (acceptances invariate)
- CLI behavior (inventory checklist output ha campi AGGIUNTIVI, non rimossi)
