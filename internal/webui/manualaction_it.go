package webui

import (
	"sort"
	"strings"

	"github.com/tis24dev/cPanel_self-migration/internal/accountinventory"
)

// Italian localisation of manual-action prose, applied at PRESENTATION time.
//
// Manual-action Title/OperatorAction are composed in the engine
// (internal/accountinventory/checklist.go) in English and stored VERBATIM in
// the frozen migration_checklist.json artifact — the acceptance key is a
// sha256 over type/section/title/detail, so translating at the source would
// change every AK-* key and break stored acceptances. The webui re-renders the
// frozen JSON on every view, so translating here shows Italian without
// touching keys, the JSON, the .md artifact or golden tests, and it applies
// retroactively to already-frozen sessions.
//
// Scope: Title and OperatorAction only. Detail (a value diff `source →
// destination`, technical data) and Type (a taxonomy code) stay verbatim, like
// the other raw technical references. Dynamic tails of a title (domain/record
// names) are preserved verbatim; only the static scaffolding is translated.
//
// Same pattern as statusLabelIT/stepLabelIT/overallLabelIT/sectionLabelIT
// (workbench_view.go). Unknown strings fall back to the raw English value
// (graceful); TestManualITAnchorsPresentInEngineSource guards against the
// engine rewording a known string out from under this map.

// manualActionOperatorIT maps a full OperatorAction sentence to Italian.
// Every OperatorAction produced by the engine is a static sentence (the five
// noun variants of the "Recreate the <noun>…" line are enumerated), so an
// exact-match map covers 100% with no interpolation.
var manualActionOperatorIT = map[string]string{
	"Create the domain (or transfer the account) so the destination serves it before cutover.":                                                                                                                          "Crea il dominio (o trasferisci l'account) così che la destinazione lo serva prima del cutover.",
	"Create the addon/sub/parked domain on the destination or confirm it is being dropped.":                                                                                                                             "Crea il dominio addon/sub/parked sulla destinazione oppure conferma che verrà dismesso.",
	"Recreate the mailbox on the destination or confirm it is obsolete.":                                                                                                                                                "Ricrea la casella email sulla destinazione oppure conferma che è obsoleta.",
	"Recreate the database on the destination or confirm it is obsolete.":                                                                                                                                               "Ricrea il database sulla destinazione oppure conferma che è obsoleto.",
	"Recreate the forwarder on the destination or confirm it is obsolete.":                                                                                                                                              "Ricrea l'inoltro sulla destinazione oppure conferma che è obsoleto.",
	"Recreate the autoresponder on the destination or confirm it is obsolete.":                                                                                                                                          "Ricrea il risponditore automatico sulla destinazione oppure conferma che è obsoleto.",
	"Recreate the FTP account on the destination or confirm it is obsolete.":                                                                                                                                            "Ricrea l'account FTP sulla destinazione oppure conferma che è obsoleto.",
	"The domain has no routing entry on the destination; set cPanel Email Routing (local/remote) before cutover.":                                                                                                       "Il dominio non ha una voce di instradamento sulla destinazione; imposta l'Email Routing di cPanel (local/remote) prima del cutover.",
	"Email Routing differs between source and destination; a wrong local/remote value silently breaks delivery.":                                                                                                        "L'Email Routing differisce tra sorgente e destinazione; un valore local/remote errato interrompe silenziosamente la consegna.",
	"The default address differs or is missing on the destination; a lost catch-all silently drops mail.":                                                                                                               "L'indirizzo predefinito differisce o manca sulla destinazione; un catch-all perso scarta silenziosamente la posta.",
	"The filter exists only on the source; recreate it on the destination or confirm it is obsolete — filters change mail handling silently.":                                                                           "Il filtro esiste solo sulla sorgente; ricrealo sulla destinazione oppure conferma che è obsoleto — i filtri cambiano silenziosamente la gestione della posta.",
	"A genuine redirect differs or is missing on the destination; verify it after the web files migration (its .htaccess rule travels with the files).":                                                                 "Un redirect vero differisce o manca sulla destinazione; verificalo dopo la migrazione dei file web (la sua regola .htaccess viaggia con i file).",
	"The destination certificate differs from the source but is currently valid; acknowledge or investigate.":                                                                                                           "Il certificato sulla destinazione differisce da quello sorgente ma è attualmente valido; conferma o approfondisci.",
	"The destination certificate differs and its validity could not be confirmed; verify it, reissue via AutoSSL if needed.":                                                                                            "Il certificato sulla destinazione differisce e non se ne è potuta confermare la validità; verificalo, riemettilo via AutoSSL se necessario.",
	"The source certificate no longer exists as-is, but a valid destination certificate covers all of its domains.":                                                                                                     "Il certificato sorgente non esiste più così com'era, ma un certificato valido sulla destinazione copre tutti i suoi domini.",
	"All source certificates for these domains were already expired before the migration; issue a destination certificate only if the domains must serve HTTPS.":                                                        "Tutti i certificati sorgente per questi domini erano già scaduti prima della migrazione; emetti un certificato sulla destinazione solo se i domini devono servire HTTPS.",
	"Issue or install a certificate on the destination (AutoSSL or manual) before cutover.":                                                                                                                             "Emetti o installa un certificato sulla destinazione (AutoSSL o manuale) prima del cutover.",
	"Test the site against the destination PHP configuration before cutover.":                                                                                                                                           "Testa il sito con la configurazione PHP della destinazione prima del cutover.",
	"The destination regenerated this DKIM TXT (plan: replace). Decide which key is authoritative: keep the destination's regenerated key (and update any external DNS copies) or restore the source key via the plan.": "La destinazione ha rigenerato questo TXT DKIM (piano: replace). Decidi quale chiave è autoritativa: mantieni la chiave rigenerata dalla destinazione (e aggiorna eventuali copie DNS esterne) oppure ripristina la chiave sorgente tramite il piano.",
	"TXT records often bind external services (SPF/DKIM/verification); confirm the destination value is intended.":                                                                                                      "I record TXT spesso legano servizi esterni (SPF/DKIM/verifiche); conferma che il valore sulla destinazione sia quello voluto.",
	"MX records differ between source and destination; confirm external mail (e.g. Microsoft 365 / Google Workspace) keeps working before cutover.":                                                                     "I record MX differiscono tra sorgente e destinazione; conferma che la posta esterna (es. Microsoft 365 / Google Workspace) continui a funzionare prima del cutover.",
	"NS records differ; confirm the intended delegation at the registrar/WHM level.":                                                                                                                                    "I record NS differiscono; conferma la delega voluta a livello di registrar/WHM.",
	"The destination does not serve this zone; create it via WHM/park, then re-run the inventory.":                                                                                                                      "La destinazione non serve questa zona; creala via WHM/park, poi ri-esegui l'inventario.",
	"Rewrite the SPF value by hand replacing the old server address, then create it on the destination.":                                                                                                                "Riscrivi a mano il valore SPF sostituendo il vecchio indirizzo del server, poi crealo sulla destinazione.",
	"The plan refuses to touch this MX rrset; confirm mail routing manually.":                                                                                                                                           "Il piano si rifiuta di toccare questo rrset MX; conferma manualmente l'instradamento della posta.",
	"NS/delegation is registrar/WHM territory; review it manually.":                                                                                                                                                     "NS/delega sono territorio del registrar/WHM; rivedili manualmente.",
	"The plan cannot translate this record; without it the destination will not serve it — resolve before cutover.":                                                                                                     "Il piano non può tradurre questo record; senza di esso la destinazione non lo servirà — risolvi prima del cutover.",
	"The plan does not support this record type; recreate it manually if still needed.":                                                                                                                                 "Il piano non supporta questo tipo di record; ricrealo manualmente se ancora necessario.",
	"Recreate this cron job on the destination before cutover.":                                                                                                                                                         "Ricrea questo cron job sulla destinazione prima del cutover.",
	"Recreate this cron job on the destination adapting the /home/<user> paths to the new account.":                                                                                                                     "Ricrea questo cron job sulla destinazione adattando i percorsi /home/<user> al nuovo account.",
	"The job was disabled on the source; recreate it only if you plan to re-enable it.":                                                                                                                                 "Il job era disabilitato sulla sorgente; ricrealo solo se intendi riabilitarlo.",
}

// manualTitleStaticIT: titles with no dynamic tail (exact match).
var manualTitleStaticIT = map[string]string{
	"Create the main domain on the destination":         "Crea il dominio principale sulla destinazione",
	"Create the missing domain on the destination":      "Crea il dominio mancante sulla destinazione",
	"Recreate active cron job":                          "Ricrea il cron job attivo",
	"Recreate disabled cron job (only if still needed)": "Ricrea il cron job disabilitato (solo se ancora necessario)",
}

// manualTitlePrefixIT: titles of the form "<English prefix><dynamic tail>".
// The tail (domain/record name) is preserved verbatim. Rules are matched
// longest-prefix-first so a shorter prefix never shadows a longer one.
var manualTitlePrefixIT = []struct{ en, it string }{
	{"Confirm mail routing (MX) for ", "Conferma l'instradamento email (MX) per "},
	{"Confirm mail routing for ", "Conferma l'instradamento email per "},
	{"Check the default (catch-all) address for ", "Verifica l'indirizzo predefinito (catch-all) per "},
	{"Recreate email filter ", "Ricrea il filtro email "},
	{"Confirm redirect ", "Conferma il redirect "},
	{"Acknowledge the reissued certificate for ", "Conferma il certificato riemesso per "},
	{"Verify or reissue the certificate for ", "Verifica o riemetti il certificato per "},
	{"Acknowledge the regrouped certificate for ", "Conferma il certificato riorganizzato per "},
	{"Acknowledge the expired source certificate for ", "Conferma il certificato sorgente scaduto per "},
	{"Issue a certificate for ", "Emetti un certificato per "},
	{"Check PHP compatibility for ", "Verifica la compatibilità PHP per "},
	{"Confirm the regenerated DKIM key ", "Conferma la chiave DKIM rigenerata "},
	{"Verify the changed TXT record ", "Verifica il record TXT modificato "},
	{"Confirm delegation (NS) for ", "Conferma la delega (NS) per "},
	{"Create the missing DNS zone ", "Crea la zona DNS mancante "},
	{"Rewrite the SPF TXT record ", "Riscrivi il record TXT SPF "},
	{"Review delegation (NS) for ", "Rivedi la delega (NS) per "},
}

// manualTitleNounIT maps the recreate-noun of the "Recreate <noun> <ref> on
// the destination" title. Multi-word nouns are matched with their trailing
// space so the ref boundary is unambiguous.
var manualTitleNounIT = map[string]string{
	"mailbox":       "la casella email",
	"database":      "il database",
	"forwarder":     "l'inoltro",
	"autoresponder": "il risponditore automatico",
	"FTP account":   "l'account FTP",
}

func init() {
	// Enforce longest-prefix-first regardless of source order.
	sort.SliceStable(manualTitlePrefixIT, func(i, j int) bool {
		return len(manualTitlePrefixIT[i].en) > len(manualTitlePrefixIT[j].en)
	})
}

// manualActionIT returns the Italian OperatorAction, or the raw value if
// unknown. Registered as a template func.
func manualActionIT(a accountinventory.ManualAction) string {
	if it, ok := manualActionOperatorIT[a.OperatorAction]; ok {
		return it
	}
	return a.OperatorAction
}

// manualTitleIT returns the Italian Title, preserving the dynamic tail, or the
// raw value if unrecognised. Registered as a template func.
func manualTitleIT(a accountinventory.ManualAction) string {
	t := a.Title
	if it, ok := manualTitleStaticIT[t]; ok {
		return it
	}
	// "Recreate <noun> <ref> on the destination"
	if mid, ok := strings.CutPrefix(t, "Recreate "); ok {
		if mid, ok := strings.CutSuffix(mid, " on the destination"); ok {
			for noun, nounIT := range manualTitleNounIT {
				if ref, ok := strings.CutPrefix(mid, noun+" "); ok {
					return "Ricrea " + nounIT + " " + ref + " sulla destinazione"
				}
			}
		}
	}
	// "Resolve the <T> record <name> by hand" / "Review the <T> record <name> by hand"
	if it, ok := byHandTitleIT(t, "Resolve the ", "Risolvi a mano il record "); ok {
		return it
	}
	if it, ok := byHandTitleIT(t, "Review the ", "Rivedi a mano il record "); ok {
		return it
	}
	// prefix + verbatim tail
	for _, r := range manualTitlePrefixIT {
		if tail, ok := strings.CutPrefix(t, r.en); ok {
			return r.it + tail
		}
	}
	return t
}

// byHandTitleIT handles the "<verb> the <TYPE> record <name> by hand" family:
// the record TYPE and name sit in the middle, so a plain prefix rule cannot
// translate the interposed " record " word.
func byHandTitleIT(title, enPrefix, itPrefix string) (string, bool) {
	mid, ok := strings.CutPrefix(title, enPrefix)
	if !ok {
		return "", false
	}
	mid, ok = strings.CutSuffix(mid, " by hand")
	if !ok {
		return "", false
	}
	typ, name, ok := strings.Cut(mid, " record ")
	if !ok {
		return "", false
	}
	return itPrefix + typ + " " + name, true
}
