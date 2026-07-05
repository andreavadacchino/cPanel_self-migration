package webui

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/tis24dev/cPanel_self-migration/internal/accountinventory"
)

// act builds a minimal ManualAction for translation tests. The operator
// string is optional (title tests omit it).
func act(typ, title string, operator ...string) accountinventory.ManualAction {
	op := ""
	if len(operator) > 0 {
		op = operator[0]
	}
	return accountinventory.ManualAction{Type: typ, Title: title, OperatorAction: op}
}

// titleCase couples an engine Title (as produced by checklist.go) with the
// expected Italian rendering. Dynamic tails (domain/record names) are chosen
// arbitrarily and must survive verbatim.
func TestManualTitleIT(t *testing.T) {
	const (
		tCreate = accountinventory.MActionCreateOnDestination
		tRoute  = accountinventory.MActionConfirmEmailRouting
		tFilter = accountinventory.MActionRecreateEmailFilters
		tRedir  = accountinventory.MActionConfirmRedirect
		tCheck  = accountinventory.MActionManualCheckRequired
		tAccept = accountinventory.MActionAcceptExpectedDiff
		tSSL    = accountinventory.MActionReissueSSL
		tPHP    = accountinventory.MActionCheckPHPCompat
		tDNS    = accountinventory.MActionConfirmDNSRecord
		tVerify = accountinventory.MActionVerifyExternalSvc
		tMX     = accountinventory.MActionConfirmMXExternal
		tSPF    = accountinventory.MActionUpdateSPF
		tCron   = accountinventory.MActionRecreateCron
		tCronP  = accountinventory.MActionAdaptCronPath
	)
	cases := []struct {
		name string
		a    accountinventory.ManualAction
		want string
	}{
		// statics
		{"T1", act(tCreate, "Create the main domain on the destination"), "Crea il dominio principale sulla destinazione"},
		{"T2", act(tCreate, "Create the missing domain on the destination"), "Crea il dominio mancante sulla destinazione"},
		{"T24", act(tCron, "Recreate active cron job"), "Ricrea il cron job attivo"},
		{"T25", act(tCron, "Recreate disabled cron job (only if still needed)"), "Ricrea il cron job disabilitato (solo se ancora necessario)"},
		// noun family (T3) — all five nouns
		{"T3-mailbox", act(tCreate, "Recreate mailbox a@giorginisposi.it on the destination"), "Ricrea la casella email a@giorginisposi.it sulla destinazione"},
		{"T3-database", act(tCreate, "Recreate database wp_db on the destination"), "Ricrea il database wp_db sulla destinazione"},
		{"T3-forwarder", act(tCreate, "Recreate forwarder x@y.it on the destination"), "Ricrea l'inoltro x@y.it sulla destinazione"},
		{"T3-autoresponder", act(tCreate, "Recreate autoresponder a@b.it on the destination"), "Ricrea il risponditore automatico a@b.it sulla destinazione"},
		{"T3-ftp", act(tCreate, "Recreate FTP account ftp@z.it on the destination"), "Ricrea l'account FTP ftp@z.it sulla destinazione"},
		// prefix + tail
		{"T4", act(tRoute, "Confirm mail routing for giorginisposi.it"), "Conferma l'instradamento email per giorginisposi.it"},
		{"T5", act(tCheck, "Check the default (catch-all) address for giorginisposi.it"), "Verifica l'indirizzo predefinito (catch-all) per giorginisposi.it"},
		{"T6", act(tFilter, "Recreate email filter SpamToJunk"), "Ricrea il filtro email SpamToJunk"},
		{"T7", act(tRedir, "Confirm redirect /old → /new"), "Conferma il redirect /old → /new"},
		{"T8", act(tAccept, "Acknowledge the reissued certificate for giorginisposi.it"), "Conferma il certificato riemesso per giorginisposi.it"},
		{"T9", act(tSSL, "Verify or reissue the certificate for giorginisposi.it"), "Verifica o riemetti il certificato per giorginisposi.it"},
		{"T10", act(tAccept, "Acknowledge the regrouped certificate for giorginisposi.it"), "Conferma il certificato riorganizzato per giorginisposi.it"},
		{"T11", act(tAccept, "Acknowledge the expired source certificate for giorginisposi.it"), "Conferma il certificato sorgente scaduto per giorginisposi.it"},
		{"T12", act(tSSL, "Issue a certificate for giorginisposi.it"), "Emetti un certificato per giorginisposi.it"},
		{"T12-nolist", act(tSSL, "Issue a certificate for (no domain list)"), "Emetti un certificato per (no domain list)"},
		{"T13", act(tPHP, "Check PHP compatibility for giorginisposi.it"), "Verifica la compatibilità PHP per giorginisposi.it"},
		{"T14", act(tDNS, "Confirm the regenerated DKIM key zone giorginisposi.it TXT default._domainkey"), "Conferma la chiave DKIM rigenerata zone giorginisposi.it TXT default._domainkey"},
		{"T15", act(tVerify, "Verify the changed TXT record zone giorginisposi.it TXT giorginisposi.it."), "Verifica il record TXT modificato zone giorginisposi.it TXT giorginisposi.it."},
		{"T16", act(tMX, "Confirm mail routing (MX) for giorginisposi.it"), "Conferma l'instradamento email (MX) per giorginisposi.it"},
		{"T17", act(tDNS, "Confirm delegation (NS) for zone giorginisposi.it NS giorginisposi.it."), "Conferma la delega (NS) per zone giorginisposi.it NS giorginisposi.it."},
		{"T18", act(tCreate, "Create the missing DNS zone giorginisposi.it"), "Crea la zona DNS mancante giorginisposi.it"},
		{"T19", act(tSPF, "Rewrite the SPF TXT record giorginisposi.it."), "Riscrivi il record TXT SPF giorginisposi.it."},
		{"T20", act(tMX, "Resolve the MX record giorginisposi.it. by hand"), "Risolvi a mano il record MX giorginisposi.it."},
		{"T21", act(tDNS, "Review delegation (NS) for giorginisposi.it."), "Rivedi la delega (NS) per giorginisposi.it."},
		{"T22-A", act(tDNS, "Resolve the A record www.giorginisposi.it. by hand"), "Risolvi a mano il record A www.giorginisposi.it."},
		{"T22-CNAME", act(tDNS, "Resolve the CNAME record ftp.giorginisposi.it. by hand"), "Risolvi a mano il record CNAME ftp.giorginisposi.it."},
		{"T23-SRV", act(tDNS, "Review the SRV record _sip._tcp.giorginisposi.it. by hand"), "Rivedi a mano il record SRV _sip._tcp.giorginisposi.it."},
		// cron path-adapt uses the same title as active cron (T24) but a different type
		{"T24-adapt", act(tCronP, "Recreate active cron job"), "Ricrea il cron job attivo"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := manualTitleIT(tc.a); got != tc.want {
				t.Errorf("manualTitleIT()\n got: %q\nwant: %q", got, tc.want)
			}
		})
	}
}

func TestManualActionIT(t *testing.T) {
	cases := []struct {
		name, op, want string
	}{
		{"O1", "Create the domain (or transfer the account) so the destination serves it before cutover.", "Crea il dominio (o trasferisci l'account) così che la destinazione lo serva prima del cutover."},
		{"O2", "Create the addon/sub/parked domain on the destination or confirm it is being dropped.", "Crea il dominio addon/sub/parked sulla destinazione oppure conferma che verrà dismesso."},
		{"O3-mailbox", "Recreate the mailbox on the destination or confirm it is obsolete.", "Ricrea la casella email sulla destinazione oppure conferma che è obsoleta."},
		{"O3-database", "Recreate the database on the destination or confirm it is obsolete.", "Ricrea il database sulla destinazione oppure conferma che è obsoleto."},
		{"O3-forwarder", "Recreate the forwarder on the destination or confirm it is obsolete.", "Ricrea l'inoltro sulla destinazione oppure conferma che è obsoleto."},
		{"O3-autoresponder", "Recreate the autoresponder on the destination or confirm it is obsolete.", "Ricrea il risponditore automatico sulla destinazione oppure conferma che è obsoleto."},
		{"O3-ftp", "Recreate the FTP account on the destination or confirm it is obsolete.", "Ricrea l'account FTP sulla destinazione oppure conferma che è obsoleto."},
		{"O4", "The domain has no routing entry on the destination; set cPanel Email Routing (local/remote) before cutover.", "Il dominio non ha una voce di instradamento sulla destinazione; imposta l'Email Routing di cPanel (local/remote) prima del cutover."},
		{"O5", "Email Routing differs between source and destination; a wrong local/remote value silently breaks delivery.", "L'Email Routing differisce tra sorgente e destinazione; un valore local/remote errato interrompe silenziosamente la consegna."},
		{"O6", "The default address differs or is missing on the destination; a lost catch-all silently drops mail.", "L'indirizzo predefinito differisce o manca sulla destinazione; un catch-all perso scarta silenziosamente la posta."},
		{"O7", "The filter exists only on the source; recreate it on the destination or confirm it is obsolete — filters change mail handling silently.", "Il filtro esiste solo sulla sorgente; ricrealo sulla destinazione oppure conferma che è obsoleto — i filtri cambiano silenziosamente la gestione della posta."},
		{"O8", "A genuine redirect differs or is missing on the destination; verify it after the web files migration (its .htaccess rule travels with the files).", "Un redirect vero differisce o manca sulla destinazione; verificalo dopo la migrazione dei file web (la sua regola .htaccess viaggia con i file)."},
		{"O9", "The destination certificate differs from the source but is currently valid; acknowledge or investigate.", "Il certificato sulla destinazione differisce da quello sorgente ma è attualmente valido; conferma o approfondisci."},
		{"O10", "The destination certificate differs and its validity could not be confirmed; verify it, reissue via AutoSSL if needed.", "Il certificato sulla destinazione differisce e non se ne è potuta confermare la validità; verificalo, riemettilo via AutoSSL se necessario."},
		{"O11", "The source certificate no longer exists as-is, but a valid destination certificate covers all of its domains.", "Il certificato sorgente non esiste più così com'era, ma un certificato valido sulla destinazione copre tutti i suoi domini."},
		{"O12", "All source certificates for these domains were already expired before the migration; issue a destination certificate only if the domains must serve HTTPS.", "Tutti i certificati sorgente per questi domini erano già scaduti prima della migrazione; emetti un certificato sulla destinazione solo se i domini devono servire HTTPS."},
		{"O13", "Issue or install a certificate on the destination (AutoSSL or manual) before cutover.", "Emetti o installa un certificato sulla destinazione (AutoSSL o manuale) prima del cutover."},
		{"O14", "Test the site against the destination PHP configuration before cutover.", "Testa il sito con la configurazione PHP della destinazione prima del cutover."},
		{"O15", "The destination regenerated this DKIM TXT (plan: replace). Decide which key is authoritative: keep the destination's regenerated key (and update any external DNS copies) or restore the source key via the plan.", "La destinazione ha rigenerato questo TXT DKIM (piano: replace). Decidi quale chiave è autoritativa: mantieni la chiave rigenerata dalla destinazione (e aggiorna eventuali copie DNS esterne) oppure ripristina la chiave sorgente tramite il piano."},
		{"O16", "TXT records often bind external services (SPF/DKIM/verification); confirm the destination value is intended.", "I record TXT spesso legano servizi esterni (SPF/DKIM/verifiche); conferma che il valore sulla destinazione sia quello voluto."},
		{"O17", "MX records differ between source and destination; confirm external mail (e.g. Microsoft 365 / Google Workspace) keeps working before cutover.", "I record MX differiscono tra sorgente e destinazione; conferma che la posta esterna (es. Microsoft 365 / Google Workspace) continui a funzionare prima del cutover."},
		{"O18", "NS records differ; confirm the intended delegation at the registrar/WHM level.", "I record NS differiscono; conferma la delega voluta a livello di registrar/WHM."},
		{"O19", "The destination does not serve this zone; create it via WHM/park, then re-run the inventory.", "La destinazione non serve questa zona; creala via WHM/park, poi ri-esegui l'inventario."},
		{"O20", "Rewrite the SPF value by hand replacing the old server address, then create it on the destination.", "Riscrivi a mano il valore SPF sostituendo il vecchio indirizzo del server, poi crealo sulla destinazione."},
		{"O21", "The plan refuses to touch this MX rrset; confirm mail routing manually.", "Il piano si rifiuta di toccare questo rrset MX; conferma manualmente l'instradamento della posta."},
		{"O22", "NS/delegation is registrar/WHM territory; review it manually.", "NS/delega sono territorio del registrar/WHM; rivedili manualmente."},
		{"O23", "The plan cannot translate this record; without it the destination will not serve it — resolve before cutover.", "Il piano non può tradurre questo record; senza di esso la destinazione non lo servirà — risolvi prima del cutover."},
		{"O24", "The plan does not support this record type; recreate it manually if still needed.", "Il piano non supporta questo tipo di record; ricrealo manualmente se ancora necessario."},
		{"O25", "Recreate this cron job on the destination before cutover.", "Ricrea questo cron job sulla destinazione prima del cutover."},
		{"O26", "Recreate this cron job on the destination adapting the /home/<user> paths to the new account.", "Ricrea questo cron job sulla destinazione adattando i percorsi /home/<user> al nuovo account."},
		{"O27", "The job was disabled on the source; recreate it only if you plan to re-enable it.", "Il job era disabilitato sulla sorgente; ricrealo solo se intendi riabilitarlo."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := manualActionIT(act("", "", tc.op)); got != tc.want {
				t.Errorf("manualActionIT()\n got: %q\nwant: %q", got, tc.want)
			}
		})
	}
}

// Unknown strings fall back to the raw value (graceful, like overallLabelIT).
func TestManualITFallbackRaw(t *testing.T) {
	if got := manualTitleIT(act("X", "Totally unknown title xyz", "")); got != "Totally unknown title xyz" {
		t.Errorf("title fallback = %q", got)
	}
	if got := manualActionIT(act("X", "", "Totally unknown operator xyz")); got != "Totally unknown operator xyz" {
		t.Errorf("operator fallback = %q", got)
	}
	// Empty stays empty.
	if got := manualActionIT(act("X", "", "")); got != "" {
		t.Errorf("empty operator = %q", got)
	}
}

// Drift guard: every recognizer anchor must still be present verbatim in the
// engine source. If the engine rewords a manual-action string, its anchor
// disappears here and the test fails loudly, forcing a translator update.
func TestManualITAnchorsPresentInEngineSource(t *testing.T) {
	_, thisFile, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))
	src, err := os.ReadFile(filepath.Join(root, "internal", "accountinventory", "checklist.go"))
	if err != nil {
		t.Fatalf("read checklist.go: %v", err)
	}
	s := string(src)
	anchors := []string{
		// title anchors
		"Create the main domain on the destination",
		"Create the missing domain on the destination",
		"Recreate %s %s on the destination",
		"Confirm mail routing for ",
		"Check the default (catch-all) address for ",
		"Recreate email filter ",
		"Confirm redirect ",
		"Acknowledge the reissued certificate for ",
		"Verify or reissue the certificate for ",
		"Acknowledge the regrouped certificate for ",
		"Acknowledge the expired source certificate for ",
		"Issue a certificate for ",
		"Check PHP compatibility for ",
		"Confirm the regenerated DKIM key ",
		"Verify the changed TXT record ",
		"Confirm mail routing (MX) for ",
		"Confirm delegation (NS) for ",
		"Create the missing DNS zone ",
		"Rewrite the SPF TXT record ",
		"Resolve the MX record ",
		"Review delegation (NS) for ",
		"Resolve the %s record %s by hand",
		"Review the %s record %s by hand",
		"Recreate active cron job",
		"Recreate disabled cron job (only if still needed)",
		// operator anchors (exact)
		"Create the domain (or transfer the account) so the destination serves it before cutover.",
		"Create the addon/sub/parked domain on the destination or confirm it is being dropped.",
		"Recreate the %s on the destination or confirm it is obsolete.",
		"The domain has no routing entry on the destination; set cPanel Email Routing (local/remote) before cutover.",
		"Email Routing differs between source and destination; a wrong local/remote value silently breaks delivery.",
		"The default address differs or is missing on the destination; a lost catch-all silently drops mail.",
		"The filter exists only on the source; recreate it on the destination or confirm it is obsolete — filters change mail handling silently.",
		"A genuine redirect differs or is missing on the destination; verify it after the web files migration (its .htaccess rule travels with the files).",
		"The destination certificate differs from the source but is currently valid; acknowledge or investigate.",
		"The destination certificate differs and its validity could not be confirmed; verify it, reissue via AutoSSL if needed.",
		"The source certificate no longer exists as-is, but a valid destination certificate covers all of its domains.",
		"All source certificates for these domains were already expired before the migration; issue a destination certificate only if the domains must serve HTTPS.",
		"Issue or install a certificate on the destination (AutoSSL or manual) before cutover.",
		"Test the site against the destination PHP configuration before cutover.",
		"The destination regenerated this DKIM TXT (plan: replace). Decide which key is authoritative: keep the destination's regenerated key (and update any external DNS copies) or restore the source key via the plan.",
		"TXT records often bind external services (SPF/DKIM/verification); confirm the destination value is intended.",
		"MX records differ between source and destination; confirm external mail (e.g. Microsoft 365 / Google Workspace) keeps working before cutover.",
		"NS records differ; confirm the intended delegation at the registrar/WHM level.",
		"The destination does not serve this zone; create it via WHM/park, then re-run the inventory.",
		"Rewrite the SPF value by hand replacing the old server address, then create it on the destination.",
		"The plan refuses to touch this MX rrset; confirm mail routing manually.",
		"NS/delegation is registrar/WHM territory; review it manually.",
		"The plan cannot translate this record; without it the destination will not serve it — resolve before cutover.",
		"The plan does not support this record type; recreate it manually if still needed.",
		"Recreate this cron job on the destination before cutover.",
		"Recreate this cron job on the destination adapting the /home/<user> paths to the new account.",
		"The job was disabled on the source; recreate it only if you plan to re-enable it.",
	}
	for _, a := range anchors {
		if !strings.Contains(s, a) {
			t.Errorf("engine source no longer contains anchor %q — reword drifted; update manualaction_it.go", a)
		}
	}
}

// Drift guard (ADD, not reword): the recreate-noun set is interpolated into
// both the T3 title and the O3 operator, so a new noun added to the engine's
// map would fall back to raw English SILENTLY. Extract the engine noun map and
// assert every noun is translatable in both the title-noun map and the
// operator map.
func TestManualITNounSetCoveredBothWays(t *testing.T) {
	_, thisFile, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))
	src, err := os.ReadFile(filepath.Join(root, "internal", "accountinventory", "checklist.go"))
	if err != nil {
		t.Fatalf("read checklist.go: %v", err)
	}
	s := string(src)
	// Isolate the `noun := map[string]string{ ... }` block in evalRecreateSection.
	start := strings.Index(s, "noun := map[string]string{")
	if start < 0 {
		t.Fatal("noun map not found in checklist.go — evalRecreateSection changed shape; revisit the noun translators")
	}
	end := strings.IndexByte(s[start:], '}')
	if end < 0 {
		t.Fatal("noun map block not terminated")
	}
	block := s[start : start+end]
	// Values are the nouns: `"section": "noun"`.
	re := regexp.MustCompile(`"[^"]*":\s*"([^"]*)"`)
	nouns := re.FindAllStringSubmatch(block, -1)
	if len(nouns) == 0 {
		t.Fatal("no nouns parsed from the engine map")
	}
	for _, m := range nouns {
		noun := m[1]
		if _, ok := manualTitleNounIT[noun]; !ok {
			t.Errorf("engine noun %q missing from manualTitleNounIT — new noun added, T3 title falls back to English", noun)
		}
		op := "Recreate the " + noun + " on the destination or confirm it is obsolete."
		if got := manualActionIT(act("", "", op)); got == op {
			t.Errorf("engine noun %q: O3 operator %q not translated (falls back to raw)", noun, op)
		}
	}
	// And no stale extra entries in the title-noun map.
	if len(manualTitleNounIT) != len(nouns) {
		t.Errorf("manualTitleNounIT has %d nouns, engine has %d — sets drifted", len(manualTitleNounIT), len(nouns))
	}
}
