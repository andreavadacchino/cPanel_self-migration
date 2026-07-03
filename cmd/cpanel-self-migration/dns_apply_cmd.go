package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tis24dev/cPanel_self-migration/internal/accountinventory"
	"github.com/tis24dev/cPanel_self-migration/internal/config"
	"github.com/tis24dev/cPanel_self-migration/internal/cpanel"
	"github.com/tis24dev/cPanel_self-migration/internal/sshx"
)

// runDNSApplyCmd implements `cpanel-self-migration dns apply`: the DNS
// config writer (PR 6D). It consumes an offline dns_import_plan.json
// and writes DNS records onto the DESTINATION account via
// DNS::mass_edit_zone with serial guard (optimistic locking).
//
// v1 implements `add` only. `replace` ops get status skipped_replace_v1.
// `manual` and `skip` ops are non-writable (skipped/manual).
//
// House contract (docs/dev/PR6D_DNS_APPLY_DESIGN.md):
//   - without --yes-apply-writes: fully offline preview, ZERO connections;
//   - backup-or-nothing before the first write, bidirectionally paired
//     with the report;
//   - serial guard: stale serial → all ops for that zone get
//     refused_precondition;
//   - unconditional per-op verify-after (re-fetch zone, match by
//     type+name+data);
//   - --rollback <backup>: report-driven inverse ops, removes ONLY the
//     tool's own applied adds by line index.
//
// Exit codes: 0 ok; 1 input/runtime/write failure (report still written
// when the run got that far); 2 flags; 3 gated refusal (one or more
// refused_precondition ops).
func runDNSApplyCmd(args []string) int {
	fs := flag.NewFlagSet("dns apply", flag.ContinueOnError)
	planPath := fs.String("plan", "", "path to dns_import_plan.json (required unless --rollback)")
	cfgFlag := fs.String("config", "", "path to host.yaml (default: configs/host.yaml or host.yaml)")
	yes := fs.Bool("yes-apply-writes", false, "actually write to the DESTINATION (default: fully offline preview, zero connections)")
	rollbackPath := fs.String("rollback", "", "path to a dns apply backup JSON: roll back that run instead of applying")
	reportFlag := fs.String("report", "", "with --rollback: explicit path of the paired apply report (overrides the backup's recorded pairing)")
	acceptReportLoss := fs.Bool("accept-report-loss", false, "with --rollback: proceed WITHOUT the paired report — documented degradation: ALL ops become MANUAL")
	backupFlag := fs.String("backup", "", "pre-write backup path (default: dns_backup_<timestamp>.json in the report directory)")
	outJSON := fs.String("output-json", "dns_apply_report.json", "report JSON path")
	outMD := fs.String("output-md", "", "report Markdown path (default: derived from --output-json)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: cpanel-self-migration dns apply --plan dns_import_plan.json [--yes-apply-writes] [--config host.yaml] [--backup PATH]")
		fmt.Fprintln(os.Stderr, "       cpanel-self-migration dns apply --rollback dns_backup_….json [--report REPORT.json|--accept-report-loss] [--yes-apply-writes]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}

	if *rollbackPath != "" {
		if *planPath != "" {
			fmt.Fprintln(os.Stderr, "error: --plan and --rollback are mutually exclusive")
			return 2
		}
		return runDNSRollback(*rollbackPath, *reportFlag, *acceptReportLoss, *yes, *cfgFlag, *outJSON, *outMD)
	}
	if *reportFlag != "" || *acceptReportLoss {
		fmt.Fprintln(os.Stderr, "error: --report/--accept-report-loss only apply to --rollback")
		return 2
	}
	if *planPath == "" {
		fmt.Fprintln(os.Stderr, "error: --plan is required")
		fs.Usage()
		return 1
	}

	plan, err := loadDNSPlanFile(*planPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if plan.FormatVersion != 1 {
		fmt.Fprintf(os.Stderr, "error: %s: unsupported plan format_version %d (this build understands 1)\n", *planPath, plan.FormatVersion)
		return 1
	}

	if !*yes {
		printDNSApplyDryRun(plan)
		return 0
	}
	if *outMD == "" {
		*outMD = deriveMDPath(*outJSON)
	}
	return runDNSApplyWrites(plan, *planPath, *cfgFlag, *backupFlag, *outJSON, *outMD)
}

// printDNSApplyDryRun is the offline preview: no config, no SSH, no
// artifact files.
func printDNSApplyDryRun(plan accountinventory.DNSPlan) {
	fmt.Println("dns apply — DRY-RUN (fully offline: no connection was opened, nothing was written).")
	fmt.Println("NOTE: this preview renders the PLAN-recorded ops, not the live zone state —")
	fmt.Println("the live preview is `dns verify`.")
	fmt.Println()
	writes := 0
	for _, z := range plan.Zones {
		for _, op := range z.Ops {
			switch op.Action {
			case accountinventory.ActionAdd:
				fmt.Printf("  [%s] add  %s %s (%d record(s))\n", z.Zone, op.Type, op.Name, len(op.Records))
				writes++
			case accountinventory.ActionReplace:
				fmt.Printf("  [%s] replace  %s %s (%d record(s))\n", z.Zone, op.Type, op.Name, len(op.Records))
				writes++
			case accountinventory.ActionManual:
				fmt.Printf("  [%s] manual  %s %s — %s\n", z.Zone, op.Type, op.Name, op.Reason)
			}
		}
	}
	if writes == 0 {
		fmt.Println("  (no writable ops in this plan)")
	}
	fmt.Printf("\nplan summary: %d add, %d replace, %d manual, %d skip, %d informational\n",
		plan.Summary.Add, plan.Summary.Replace, plan.Summary.Manual,
		plan.Summary.Skip, plan.Summary.Informational)
	fmt.Println("to apply: re-run with --yes-apply-writes")
}

// dnsCanonToRelative converts a canonical name (absolute FQDN with
// trailing dot) to the relative form mass_edit_zone expects: "@" for
// the apex, otherwise strip the ".zone." suffix.
func dnsCanonToRelative(canonical, zone string) string {
	zDot := strings.ToLower(zone) + "."
	c := strings.ToLower(canonical)
	if c == zDot {
		return "@"
	}
	suffix := "." + zDot
	if strings.HasSuffix(c, suffix) {
		return strings.TrimSuffix(c, suffix)
	}
	return canonical
}

// toDNSRecordEntries converts cpanel.DNSRecord to
// accountinventory.DNSRecordEntry for use in the backup.
func toDNSRecordEntriesFromCpanel(records []cpanel.DNSRecord) []accountinventory.DNSRecordEntry {
	out := make([]accountinventory.DNSRecordEntry, 0, len(records))
	for _, r := range records {
		out = append(out, accountinventory.DNSRecordEntry{
			Type:     r.Type,
			Name:     r.Name,
			TTL:      r.TTL,
			Value:    r.Value,
			Priority: r.Priority,
			Exchange: r.Exchange,
			Address:  r.Address,
			Target:   r.Target,
			TxtData:  r.TxtData,
			Class:    r.Class,
			Line:     r.Line,
			Raw:      r.Raw,
		})
	}
	return out
}

// dialDNSDest resolves the config and dials the DESTINATION.
func dialDNSDest(ctx context.Context, cfgFlag string) (*sshx.Client, error) {
	path, alternates, err := resolveConfigPath(cfgFlag)
	if err != nil {
		return nil, err
	}
	for _, alt := range alternates {
		fmt.Fprintf(os.Stderr, "note: multiple host.yaml candidates, using %s (ignoring %s)\n", path, alt)
	}
	cfg, err := config.Load(path)
	if err != nil {
		return nil, err
	}
	if !cfg.DestConfigured() {
		return nil, fmt.Errorf("the dns commands need the DESTINATION host configured in %s", path)
	}
	client, err := sshx.DialDest(ctx, cfg, "")
	if err != nil {
		return nil, fmt.Errorf("dial destination: %w", err)
	}
	return client, nil
}

// writableZones returns the zones that have at least one add or replace op.
func writableZones(plan accountinventory.DNSPlan) []string {
	seen := map[string]bool{}
	var zones []string
	for _, z := range plan.Zones {
		for _, op := range z.Ops {
			if (op.Action == accountinventory.ActionAdd || op.Action == accountinventory.ActionReplace) && !seen[z.Zone] {
				seen[z.Zone] = true
				zones = append(zones, z.Zone)
			}
		}
	}
	sort.Strings(zones)
	return zones
}

// runDNSApplyWrites is the write path: fetch zones → backup-or-nothing
// → mass_edit_zone per zone → verify-after → report.
func runDNSApplyWrites(plan accountinventory.DNSPlan, planPath, cfgFlag, backupFlag, outJSON, outMD string) int {
	planSHA, err := fileSHA256(planPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}

	wZones := writableZones(plan)

	ctx := context.Background()
	var client *sshx.Client
	if len(wZones) > 0 {
		client, err = dialDNSDest(ctx, cfgFlag)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 1
		}
		defer func() { _ = client.Close() }()
	}

	// Fetch the live state for every writable zone: records + raw + serial.
	type zoneLiveState struct {
		records []cpanel.DNSRecord
		raw     []byte
		serial  string
	}
	liveState := map[string]zoneLiveState{}
	for _, zone := range wZones {
		records, raw, err := cpanel.FetchDNSZoneRaw(ctx, client, zone)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: fetch zone %s: %v\n", zone, err)
			return 1
		}
		serial, err := cpanel.ExtractSOASerial(raw)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: extract serial for %s: %v\n", zone, err)
			return 1
		}
		liveState[zone] = zoneLiveState{records: records, raw: raw, serial: serial}
	}

	report := accountinventory.DNSApplyReport{
		Mode: "dns-apply-report", FormatVersion: 1, RunMode: "apply",
		PlanFile: planPath, PlanSHA256: planSHA,
	}

	// Backup-or-nothing: written BEFORE the first write.
	if len(wZones) > 0 {
		backupPath := backupFlag
		if backupPath == "" {
			backupPath = filepath.Join(filepath.Dir(outJSON),
				fmt.Sprintf("dns_backup_%s.json", time.Now().UTC().Format("20060102-150405")))
		}
		var backupZones []accountinventory.DNSBackupZone
		for _, zone := range wZones {
			ls := liveState[zone]
			backupZones = append(backupZones, accountinventory.DNSBackupZone{
				Zone:    zone,
				Records: toDNSRecordEntriesFromCpanel(ls.records),
				Raw:     json.RawMessage(ls.raw),
				Serial:  ls.serial,
			})
		}
		backup := accountinventory.DNSApplyBackup{
			Mode: "dns-apply-backup", FormatVersion: 1,
			GeneratedAt: time.Now().UTC().Format(time.RFC3339),
			PlanFile:    planPath, PlanSHA256: planSHA,
			ReportFile: outJSON,
			Zones:      backupZones,
		}
		if err := accountinventory.WriteDNSApplyBackupJSON(backupPath, backup); err != nil {
			fmt.Fprintln(os.Stderr, "error: backup-or-nothing — backup write failed, NOTHING was written:", err)
			return 1
		}
		backupSHA, err := fileSHA256(backupPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error: backup-or-nothing — cannot hash the backup, NOTHING was written:", err)
			return 1
		}
		report.BackupFile, report.BackupSHA256 = backupPath, backupSHA
		fmt.Fprintf(os.Stderr, "wrote %s (pre-write backup)\n", backupPath)
	} else {
		report.BackupNote = "no write was decided (every op skipped or manual) — nothing to back up"
	}

	// Process each zone in the plan.
	for _, pz := range plan.Zones {
		zr := accountinventory.DNSApplyZoneResult{Zone: pz.Zone}

		// Classify each op.
		type pendingWrite struct {
			op     accountinventory.PlanOp
			idx    int
			action string // "add" or "replace"
		}
		var writes []pendingWrite

		for _, op := range pz.Ops {
			res := accountinventory.DNSApplyOpResult{PlanOp: op}
			switch op.Action {
			case accountinventory.ActionSkip:
				res.Status = accountinventory.DNSOpSkipped
			case accountinventory.ActionManual:
				res.Status = accountinventory.DNSOpManual
			case accountinventory.ActionReplace:
				res.Status = accountinventory.DNSOpApplied // placeholder, overwritten below
				writes = append(writes, pendingWrite{op: op, idx: len(zr.Ops), action: "replace"})
			case accountinventory.ActionAdd:
				res.Status = accountinventory.DNSOpApplied // placeholder, overwritten below
				writes = append(writes, pendingWrite{op: op, idx: len(zr.Ops), action: "add"})
			default:
				res.Status = accountinventory.DNSOpFailed
				res.StatusReason = fmt.Sprintf("unknown plan action %q — malformed or hand-edited plan", op.Action)
			}
			zr.Ops = append(zr.Ops, res)
		}

		// If this zone has writable ops, process them.
		if len(writes) > 0 {
			ls, ok := liveState[pz.Zone]
			if !ok {
				for _, w := range writes {
					zr.Ops[w.idx].Status = accountinventory.DNSOpFailed
					zr.Ops[w.idx].StatusReason = "zone live state not fetched"
				}
			} else {
				// Resolve replace preconditions and collect batch params.
				var removeLines []int
				var addRecords []cpanel.MassEditAddRecord
				hasReplace := false

				for i, w := range writes {
					if w.action != "replace" {
						// Add ops: just collect records.
						for _, rec := range w.op.Records {
							addRecords = append(addRecords, cpanel.MassEditAddRecord{
								DName:      dnsCanonToRelative(rec.Name, pz.Zone),
								TTL:        rec.TTL,
								RecordType: rec.Type,
								Data:       rec.Data,
							})
						}
						continue
					}
					hasReplace = true

					// Replace precondition: check the live zone.
					status, reason, lines := dnsReplacePrecondition(w.op, ls.records, pz.Zone)
					if status == accountinventory.DNSOpSkipped {
						// Already present — no write needed.
						zr.Ops[w.idx].Status = status
						zr.Ops[w.idx].StatusReason = reason
						writes[i].action = "" // mark as resolved
						continue
					}
					if status == accountinventory.DNSOpRefused {
						zr.Ops[w.idx].Status = status
						zr.Ops[w.idx].StatusReason = reason
						writes[i].action = "" // mark as resolved
						continue
					}

					// Precondition met: queue remove (old lines) + add (new records).
					removeLines = append(removeLines, lines...)
					for _, rec := range w.op.Records {
						addRecords = append(addRecords, cpanel.MassEditAddRecord{
							DName:      dnsCanonToRelative(rec.Name, pz.Zone),
							TTL:        rec.TTL,
							RecordType: rec.Type,
							Data:       rec.Data,
						})
					}
				}

				// Filter to only the active writes.
				var activeWrites []pendingWrite
				for _, w := range writes {
					if w.action != "" {
						activeWrites = append(activeWrites, w)
					}
				}

				if len(activeWrites) > 0 && len(addRecords) > 0 {
					var result cpanel.MassEditResult
					var writeErr error

					if hasReplace || len(removeLines) > 0 {
						result, writeErr = cpanel.MassEditZoneBatch(ctx, client, pz.Zone, ls.serial, removeLines, addRecords)
					} else {
						result, writeErr = cpanel.MassEditZoneAdd(ctx, client, pz.Zone, ls.serial, addRecords)
					}

					if writeErr != nil {
						status := accountinventory.DNSOpFailed
						reason := writeErr.Error()
						if cpanel.IsStaleSerialError(writeErr) {
							status = accountinventory.DNSOpRefused
							reason = "stale serial — zone was modified since fetch"
						}
						for _, w := range activeWrites {
							zr.Ops[w.idx].Status = status
							zr.Ops[w.idx].StatusReason = reason
						}
					} else {
						zr.NewSerial = result.NewSerial

						freshRecords, _, fetchErr := cpanel.FetchDNSZoneRaw(ctx, client, pz.Zone)
						if fetchErr != nil {
							for _, w := range activeWrites {
								zr.Ops[w.idx].Status = accountinventory.DNSOpFailed
								zr.Ops[w.idx].StatusReason = "verify-after re-fetch failed: " + fetchErr.Error()
							}
						} else {
							for _, w := range activeWrites {
								if !dnsVerifyAddOpPresent(w.op, freshRecords, pz.Zone) {
									zr.Ops[w.idx].Status = accountinventory.DNSOpFailed
									zr.Ops[w.idx].StatusReason = "write reported success but the records are not observable in the fresh zone"
									continue
								}
								if w.action == "replace" && dnsOldValuesStillPresent(w.op, freshRecords, pz.Zone) {
									zr.Ops[w.idx].Status = accountinventory.DNSOpFailed
									zr.Ops[w.idx].StatusReason = "new values present but old values still in zone — remove may have failed"
									continue
								}
								zr.Ops[w.idx].Status = accountinventory.DNSOpApplied
							}
						}
					}
				}

			}
		}

		report.Zones = append(report.Zones, zr)
	}

	report.GeneratedAt = time.Now().UTC().Format(time.RFC3339)
	report.Summary = accountinventory.SummarizeDNSResults(report.Zones)
	return finishDNSReport(report, outJSON, outMD)
}

// dnsVerifyAddOpPresent checks that every planned record in an add op
// is present in the fresh zone (match by type+name+data).
func dnsVerifyAddOpPresent(op accountinventory.PlanOp, fresh []cpanel.DNSRecord, zone string) bool {
	for _, rec := range op.Records {
		if !dnsRecordPresent(rec, fresh, zone) {
			return false
		}
	}
	return true
}

// dnsCanonLiveName canonicalizes a DNS name from a parse_zone response
// (which can be "@", relative, or absolute FQDN) to an absolute FQDN.
func dnsCanonLiveName(name, zone string) string {
	n := strings.ToLower(name)
	if n == "@" || n == "" {
		return strings.ToLower(zone) + "."
	}
	if strings.HasSuffix(n, ".") {
		return n
	}
	return n + "." + strings.ToLower(zone) + "."
}

// dnsRecordPresent checks if a single planned record exists in the
// live zone.
func dnsRecordPresent(rec accountinventory.PlanRecord, fresh []cpanel.DNSRecord, zone string) bool {
	canonName := strings.ToLower(rec.Name)
	for _, f := range fresh {
		if !strings.EqualFold(f.Type, rec.Type) {
			continue
		}
		if dnsCanonLiveName(f.Name, zone) != canonName {
			continue
		}
		if dnsDataMatch(rec, f) {
			return true
		}
	}
	return false
}

// dnsDataMatch compares the planned record's Data with the live
// record's type-specific field.
func dnsDataMatch(planned accountinventory.PlanRecord, live cpanel.DNSRecord) bool {
	switch planned.Type {
	case "A", "AAAA":
		if len(planned.Data) < 1 {
			return false
		}
		return strings.EqualFold(planned.Data[0], live.Address)
	case "CNAME":
		if len(planned.Data) < 1 {
			return false
		}
		pTarget := strings.ToLower(planned.Data[0])
		lTarget := strings.ToLower(live.Target)
		// Normalize trailing dots for comparison.
		pTarget = strings.TrimSuffix(pTarget, ".")
		lTarget = strings.TrimSuffix(lTarget, ".")
		return pTarget == lTarget
	case "MX":
		if len(planned.Data) < 2 {
			return false
		}
		if fmt.Sprintf("%d", live.Priority) != planned.Data[0] {
			return false
		}
		pExchange := strings.TrimSuffix(strings.ToLower(planned.Data[1]), ".")
		lExchange := strings.TrimSuffix(strings.ToLower(live.Exchange), ".")
		return pExchange == lExchange
	case "TXT":
		// Planned Data is pre-split segments; live TxtData is joined.
		joined := strings.Join(planned.Data, "")
		return joined == live.TxtData
	}
	return false
}

// dnsReplacePrecondition checks whether the live zone state allows the
// replace op to proceed. Returns (status, reason, lineIndexes).
//   - skipped + "already_present": live already has the desired values
//   - refused_precondition: live has neither desired nor plan-time dest values
//   - "" (empty status): precondition met, proceed with remove+add
func dnsReplacePrecondition(op accountinventory.PlanOp, live []cpanel.DNSRecord, zone string) (string, string, []int) {
	canonName := strings.ToLower(op.Name)

	// Collect live records matching type+name.
	var matching []cpanel.DNSRecord
	for _, f := range live {
		if !strings.EqualFold(f.Type, op.Type) {
			continue
		}
		if dnsCanonLiveName(f.Name, zone) == canonName {
			matching = append(matching, f)
		}
	}

	if len(matching) == 0 {
		return accountinventory.DNSOpRefused, "rrset not found on destination — cannot replace what is absent", nil
	}

	if len(op.DestinationValues) == 0 {
		return accountinventory.DNSOpRefused, "plan has no destination values for this replace op — re-plan required", nil
	}

	if len(matching) != len(op.DestinationValues) {
		return accountinventory.DNSOpRefused,
			fmt.Sprintf("rrset has %d record(s) but plan expected %d — drift detected (records added or removed since plan-time), re-plan required",
				len(matching), len(op.DestinationValues)),
			nil
	}

	// Check if ALL desired records are already present.
	allDesiredPresent := true
	for _, rec := range op.Records {
		if !dnsRecordPresent(rec, matching, zone) {
			allDesiredPresent = false
			break
		}
	}
	if allDesiredPresent {
		return accountinventory.DNSOpSkipped, "already_present: destination already has the desired values", nil
	}

	// Check if live values match plan-time DestinationValues — precondition.
	var lines []int
	usedLines := map[int]bool{}
	for _, dv := range op.DestinationValues {
		found := false
		for _, f := range matching {
			if usedLines[f.Line] {
				continue
			}
			if dnsLiveMatchesCanonValue(f, dv) {
				lines = append(lines, f.Line)
				usedLines[f.Line] = true
				found = true
				break
			}
		}
		if !found {
			return accountinventory.DNSOpRefused,
				fmt.Sprintf("plan-time destination value %q not found in the live zone — drift detected, re-plan required", dv),
				nil
		}
	}
	return "", "", lines
}

// dnsLiveMatchesCanonValue checks if a live DNS record matches a
// canonical plan value (as produced by planValue in dnsplan.go).
func dnsLiveMatchesCanonValue(live cpanel.DNSRecord, canonValue string) bool {
	switch live.Type {
	case "A", "AAAA":
		return strings.EqualFold(live.Address, canonValue)
	case "CNAME":
		lTarget := strings.TrimSuffix(strings.ToLower(live.Target), ".")
		cTarget := strings.TrimSuffix(strings.ToLower(canonValue), ".")
		return lTarget == cTarget
	case "MX":
		parts := strings.SplitN(canonValue, "\x00", 2)
		if len(parts) != 2 {
			return false
		}
		if fmt.Sprintf("%d", live.Priority) != parts[0] {
			return false
		}
		lExch := strings.TrimSuffix(strings.ToLower(live.Exchange), ".")
		cExch := strings.TrimSuffix(strings.ToLower(parts[1]), ".")
		return lExch == cExch
	case "TXT":
		return live.TxtData == canonValue
	}
	return false
}

// dnsOldValuesStillPresent checks whether any plan-time destination
// values (the pre-replace state) are still observable in the fresh zone.
// Used by verify-after to detect a failed remove in a replace op.
func dnsOldValuesStillPresent(op accountinventory.PlanOp, fresh []cpanel.DNSRecord, zone string) bool {
	canonName := strings.ToLower(op.Name)
	for _, dv := range op.DestinationValues {
		for _, f := range fresh {
			if !strings.EqualFold(f.Type, op.Type) {
				continue
			}
			if dnsCanonLiveName(f.Name, zone) != canonName {
				continue
			}
			if dnsLiveMatchesCanonValue(f, dv) {
				return true
			}
		}
	}
	return false
}

// finishDNSReport writes both report artifacts and translates the
// summary into the exit code.
func finishDNSReport(report accountinventory.DNSApplyReport, outJSON, outMD string) int {
	if err := accountinventory.WriteDNSApplyReportJSON(outJSON, report); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if err := accountinventory.WriteDNSApplyReportMarkdown(outMD, report); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	s := report.Summary
	fmt.Printf("dns %s: %d applied, %d skipped, %d manual, %d failed, %d refused\n",
		report.RunMode, s.Applied, s.Skipped, s.Manual, s.Failed, s.Refused)
	fmt.Fprintf(os.Stderr, "wrote %s\nwrote %s\n", outJSON, outMD)
	switch {
	case s.Failed > 0:
		fmt.Fprintln(os.Stderr, "one or more ops FAILED — exiting 1 (see the report)")
		return 1
	case s.Refused > 0:
		fmt.Fprintln(os.Stderr, "one or more ops were refused by the serial guard — exiting 3 (re-fetch and retry)")
		return exitDriftGate
	}
	return 0
}

// --- rollback ----------------------------------------------------------------

// loadDNSBackupFile reads and minimally validates a DNS apply backup.
func loadDNSBackupFile(path string) (accountinventory.DNSApplyBackup, error) {
	var b accountinventory.DNSApplyBackup
	raw, err := os.ReadFile(path) // #nosec G304 -- user-supplied CLI input path, read-only
	if err != nil {
		return b, fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(raw, &b); err != nil {
		return b, fmt.Errorf("parse %s: %w", path, err)
	}
	if b.Mode != "dns-apply-backup" {
		return b, fmt.Errorf("%s: not a DNS apply backup (mode %q)", path, b.Mode)
	}
	return b, nil
}

// loadDNSReportFile reads and minimally validates a DNS apply report.
func loadDNSReportFile(path string) (accountinventory.DNSApplyReport, error) {
	var r accountinventory.DNSApplyReport
	if path == "" {
		return r, fmt.Errorf("the backup records no paired report path")
	}
	raw, err := os.ReadFile(path) // #nosec G304 -- user-supplied CLI input path, read-only
	if err != nil {
		return r, fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return r, fmt.Errorf("parse %s: %w", path, err)
	}
	if r.Mode != "dns-apply-report" {
		return r, fmt.Errorf("%s: not a DNS apply report (mode %q)", path, r.Mode)
	}
	return r, nil
}

// runDNSRollback drives `dns apply --rollback <backup>`.
func runDNSRollback(backupPath, reportFlag string, acceptLoss, yes bool, cfgFlag, outJSON, outMD string) int {
	backup, err := loadDNSBackupFile(backupPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if backup.FormatVersion != 1 {
		fmt.Fprintf(os.Stderr, "error: %s: unsupported backup format_version %d\n", backupPath, backup.FormatVersion)
		return 1
	}
	if outMD == "" {
		outMD = deriveMDPath(outJSON)
	}

	reportPath := reportFlag
	if reportPath == "" {
		reportPath = backup.ReportFile
	}

	applyReport, reportErr := loadDNSReportFile(reportPath)
	if reportErr != nil && !acceptLoss {
		fmt.Fprintf(os.Stderr, "error: the paired apply report is a REQUIRED rollback input and could not be read (%v).\n", reportErr)
		fmt.Fprintln(os.Stderr, "Pass --report <path> if it lives elsewhere, or --accept-report-loss for the documented degradation (ALL ops become MANUAL).")
		return 1
	}

	isDegraded := reportErr != nil && acceptLoss
	if isDegraded {
		fmt.Fprintf(os.Stderr, "warning: paired report unavailable (%v) — DEGRADED rollback: ALL ops are MANUAL, no writes\n", reportErr)
	}

	// Collect zones that have applied adds or replaces in the report.
	type rollbackTarget struct {
		zone       string
		action     string // original action: "add" or "replace"
		opType     string
		opName     string
		records    []accountinventory.PlanRecord // records written by apply (to remove)
		oldRecords []accountinventory.PlanRecord // pre-apply records (to restore, replace only)
	}
	var targets []rollbackTarget
	if !isDegraded {
		for _, zr := range applyReport.Zones {
			for _, op := range zr.Ops {
				if op.Status != accountinventory.DNSOpApplied {
					continue
				}
				switch op.Action {
				case accountinventory.ActionAdd:
					targets = append(targets, rollbackTarget{
						zone:    zr.Zone,
						action:  "add",
						opType:  op.Type,
						opName:  op.Name,
						records: op.Records,
					})
				case accountinventory.ActionReplace:
					old := dnsBackupRecordsForOp(backup, zr.Zone, op.Type, op.Name)
					targets = append(targets, rollbackTarget{
						zone:       zr.Zone,
						action:     "replace",
						opType:     op.Type,
						opName:     op.Name,
						records:    op.Records,
						oldRecords: old,
					})
				}
			}
		}
	}

	if !yes {
		fmt.Println("dns rollback — DRY-RUN (fully offline: no connection was opened, nothing was written).")
		if isDegraded {
			fmt.Println("  DEGRADED: all zones are MANUAL (no report available to identify applied ops)")
		} else {
			for _, t := range targets {
				if t.action == "replace" {
					fmt.Printf("  restore [%s] %s %s (remove %d new, add %d old)\n", t.zone, t.opType, t.opName, len(t.records), len(t.oldRecords))
				} else {
					fmt.Printf("  remove  [%s] %s %s (%d record(s))\n", t.zone, t.opType, t.opName, len(t.records))
				}
			}
			if len(targets) == 0 {
				fmt.Println("  (nothing to roll back: no applied ops in the report)")
			}
		}
		fmt.Println("to roll back: re-run with --yes-apply-writes")
		return 0
	}

	// Build the rollback report.
	report := accountinventory.DNSApplyReport{
		Mode: "dns-apply-report", FormatVersion: 1, RunMode: "rollback",
		BackupFile: backupPath,
	}
	if sha, err := fileSHA256(backupPath); err == nil {
		report.BackupSHA256 = sha
	}

	if isDegraded {
		// All zones become manual.
		for _, bz := range backup.Zones {
			zr := accountinventory.DNSApplyZoneResult{Zone: bz.Zone}
			zr.Ops = append(zr.Ops, accountinventory.DNSApplyOpResult{
				PlanOp: accountinventory.PlanOp{
					Action: accountinventory.ActionManual,
					Reason: "degraded rollback — no report available",
				},
				Status: accountinventory.DNSOpManual,
			})
			report.Zones = append(report.Zones, zr)
		}
		report.GeneratedAt = time.Now().UTC().Format(time.RFC3339)
		report.Summary = accountinventory.SummarizeDNSResults(report.Zones)
		return finishDNSReport(report, outJSON, outMD)
	}

	if len(targets) == 0 {
		report.BackupNote = "no applied ops found in the report — nothing to roll back"
		report.GeneratedAt = time.Now().UTC().Format(time.RFC3339)
		report.Summary = accountinventory.SummarizeDNSResults(nil)
		return finishDNSReport(report, outJSON, outMD)
	}

	ctx := context.Background()
	client, err := dialDNSDest(ctx, cfgFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	defer func() { _ = client.Close() }()

	// Group targets by zone.
	targetsByZone := map[string][]rollbackTarget{}
	var zoneOrder []string
	for _, t := range targets {
		if _, ok := targetsByZone[t.zone]; !ok {
			zoneOrder = append(zoneOrder, t.zone)
		}
		targetsByZone[t.zone] = append(targetsByZone[t.zone], t)
	}

	for _, zone := range zoneOrder {
		zt := targetsByZone[zone]
		zr := accountinventory.DNSApplyZoneResult{Zone: zone}

		// Single fetch: records for line-index matching + raw for serial.
		records, raw, fetchErr := cpanel.FetchDNSZoneRaw(ctx, client, zone)
		if fetchErr != nil {
			for _, t := range zt {
				zr.Ops = append(zr.Ops, accountinventory.DNSApplyOpResult{
					PlanOp:       accountinventory.PlanOp{Action: t.opType, Type: t.opType, Name: t.opName},
					Status:       accountinventory.DNSOpFailed,
					StatusReason: "rollback re-fetch failed: " + fetchErr.Error(),
				})
			}
			report.Zones = append(report.Zones, zr)
			continue
		}
		serial, serialErr := cpanel.ExtractSOASerial(raw)
		if serialErr != nil {
			for _, t := range zt {
				zr.Ops = append(zr.Ops, accountinventory.DNSApplyOpResult{
					PlanOp:       accountinventory.PlanOp{Action: t.opType, Type: t.opType, Name: t.opName},
					Status:       accountinventory.DNSOpFailed,
					StatusReason: "rollback serial extraction failed: " + serialErr.Error(),
				})
			}
			report.Zones = append(report.Zones, zr)
			continue
		}

		// For each target, find the line indexes of records to remove and
		// collect old records to restore (for replace ops).
		var allRemoveLines []int
		var allAddRecords []cpanel.MassEditAddRecord
		needsBatch := false
		for _, t := range zt {
			action := accountinventory.ActionAdd
			if t.action == "replace" {
				action = accountinventory.ActionReplace
			}
			res := accountinventory.DNSApplyOpResult{
				PlanOp: accountinventory.PlanOp{Action: action, Type: t.opType, Name: t.opName, Records: t.records},
			}
			var lines []int
			for _, planned := range t.records {
				line := findRecordLine(planned, records, zone)
				if line >= 0 {
					lines = append(lines, line)
				}
			}
			if len(lines) == 0 && t.action == "add" {
				res.Status = accountinventory.DNSOpSkipped
				res.StatusReason = "records already absent — nothing to remove"
			} else if len(lines) == 0 && t.action == "replace" {
				res.Status = accountinventory.DNSOpRefused
				res.StatusReason = "applied records not found in zone — cannot reverse replace"
			} else if t.action == "replace" && len(t.oldRecords) == 0 {
				res.Status = accountinventory.DNSOpRefused
				res.StatusReason = "backup has no pre-apply records for this rrset — refusing to leave the zone without old or new values"
			} else {
				res.Status = accountinventory.DNSOpApplied // placeholder
				allRemoveLines = append(allRemoveLines, lines...)
				if t.action == "replace" {
					needsBatch = true
					for _, old := range t.oldRecords {
						allAddRecords = append(allAddRecords, cpanel.MassEditAddRecord{
							DName:      dnsCanonToRelative(old.Name, zone),
							TTL:        old.TTL,
							RecordType: old.Type,
							Data:       old.Data,
						})
					}
				}
			}
			zr.Ops = append(zr.Ops, res)
		}

		if len(allRemoveLines) > 0 {
			var writeErr error
			if needsBatch {
				_, writeErr = cpanel.MassEditZoneBatch(ctx, client, zone, serial, allRemoveLines, allAddRecords)
			} else {
				_, writeErr = cpanel.MassEditZoneRemove(ctx, client, zone, serial, allRemoveLines)
			}
			if writeErr != nil {
				status := accountinventory.DNSOpFailed
				reason := writeErr.Error()
				if cpanel.IsStaleSerialError(writeErr) {
					status = accountinventory.DNSOpRefused
					reason = "stale serial — zone was modified since fetch"
				}
				for i := range zr.Ops {
					if zr.Ops[i].Status == accountinventory.DNSOpApplied {
						zr.Ops[i].Status = status
						zr.Ops[i].StatusReason = reason
					}
				}
			} else {
				freshRecords, _, verifyErr := cpanel.FetchDNSZoneRaw(ctx, client, zone)
				if verifyErr != nil {
					for i := range zr.Ops {
						if zr.Ops[i].Status == accountinventory.DNSOpApplied {
							zr.Ops[i].Status = accountinventory.DNSOpFailed
							zr.Ops[i].StatusReason = "rollback verify-after re-fetch failed: " + verifyErr.Error()
						}
					}
				} else {
					for i, t := range zt {
						if zr.Ops[i].Status != accountinventory.DNSOpApplied {
							continue
						}
						// New records (written by apply) must be gone.
						stillPresent := false
						for _, rec := range t.records {
							if dnsRecordPresent(rec, freshRecords, zone) {
								stillPresent = true
								break
							}
						}
						if stillPresent {
							zr.Ops[i].Status = accountinventory.DNSOpFailed
							zr.Ops[i].StatusReason = "remove reported success but records are still present"
							continue
						}
						// For replace: old records must be restored.
						if t.action == "replace" && len(t.oldRecords) > 0 {
							allRestored := true
							for _, old := range t.oldRecords {
								if !dnsRecordPresent(old, freshRecords, zone) {
									allRestored = false
									break
								}
							}
							if !allRestored {
								zr.Ops[i].Status = accountinventory.DNSOpFailed
								zr.Ops[i].StatusReason = "old records not observable after restore"
							}
						}
					}
				}
			}
		}

		report.Zones = append(report.Zones, zr)
	}

	report.GeneratedAt = time.Now().UTC().Format(time.RFC3339)
	report.Summary = accountinventory.SummarizeDNSResults(report.Zones)
	return finishDNSReport(report, outJSON, outMD)
}

// dnsBackupRecordsForOp extracts the pre-apply records for a specific
// type+name from the backup zone. Used by replace rollback to know what
// values to restore.
func dnsBackupRecordsForOp(backup accountinventory.DNSApplyBackup, zone, opType, opName string) []accountinventory.PlanRecord {
	canonName := strings.ToLower(opName)
	for _, bz := range backup.Zones {
		if bz.Zone != zone {
			continue
		}
		var out []accountinventory.PlanRecord
		for _, r := range bz.Records {
			if !strings.EqualFold(r.Type, opType) {
				continue
			}
			if dnsCanonLiveName(r.Name, zone) != canonName {
				continue
			}
			out = append(out, backupEntryToPlanRecord(r, zone))
		}
		return out
	}
	return nil
}

// backupEntryToPlanRecord converts a backup record to PlanRecord for
// use in mass_edit_zone restore.
func backupEntryToPlanRecord(entry accountinventory.DNSRecordEntry, zone string) accountinventory.PlanRecord {
	rec := accountinventory.PlanRecord{
		Name: dnsCanonLiveName(entry.Name, zone),
		Type: entry.Type,
		TTL:  entry.TTL,
	}
	switch entry.Type {
	case "A", "AAAA":
		rec.Data = []string{entry.Address}
	case "CNAME":
		rec.Data = []string{dnsCanonLiveName(entry.Target, zone)}
	case "MX":
		rec.Data = []string{fmt.Sprintf("%d", entry.Priority), dnsCanonLiveName(entry.Exchange, zone)}
	case "TXT":
		rec.Data = []string{entry.TxtData}
	}
	return rec
}

// findRecordLine finds the line index of a planned record in the live
// zone. Returns -1 if not found.
func findRecordLine(planned accountinventory.PlanRecord, live []cpanel.DNSRecord, zone string) int {
	canonName := strings.ToLower(planned.Name)
	for _, f := range live {
		if !strings.EqualFold(f.Type, planned.Type) {
			continue
		}
		if dnsCanonLiveName(f.Name, zone) != canonName {
			continue
		}
		if dnsDataMatch(planned, f) {
			return f.Line
		}
	}
	return -1
}
