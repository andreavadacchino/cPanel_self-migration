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
				fmt.Printf("  [%s] replace  %s %s → skipped_replace_v1\n", z.Zone, op.Type, op.Name)
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

// writableZones returns the zones that have at least one add op.
func writableZones(plan accountinventory.DNSPlan) []string {
	seen := map[string]bool{}
	var zones []string
	for _, z := range plan.Zones {
		for _, op := range z.Ops {
			if op.Action == accountinventory.ActionAdd && !seen[z.Zone] {
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
		report.BackupNote = "no write was decided (every op skipped, manual, replace_v1 or skip) — nothing to back up"
	}

	// Process each zone in the plan.
	for _, pz := range plan.Zones {
		zr := accountinventory.DNSApplyZoneResult{Zone: pz.Zone}

		// Classify each op.
		type pendingAdd struct {
			op  accountinventory.PlanOp
			idx int
		}
		var adds []pendingAdd

		for _, op := range pz.Ops {
			res := accountinventory.DNSApplyOpResult{PlanOp: op}
			switch op.Action {
			case accountinventory.ActionSkip:
				res.Status = accountinventory.DNSOpSkipped
			case accountinventory.ActionManual:
				res.Status = accountinventory.DNSOpManual
			case accountinventory.ActionReplace:
				res.Status = accountinventory.DNSOpSkippedReplaceV1
				res.StatusReason = "replace is not implemented in v1 — re-plan after the v2 upgrade"
			case accountinventory.ActionAdd:
				res.Status = accountinventory.DNSOpApplied // placeholder, overwritten below
				adds = append(adds, pendingAdd{op: op, idx: len(zr.Ops)})
			default:
				res.Status = accountinventory.DNSOpFailed
				res.StatusReason = fmt.Sprintf("unknown plan action %q — malformed or hand-edited plan", op.Action)
			}
			zr.Ops = append(zr.Ops, res)
		}

		// If this zone has add ops, batch them and call mass_edit_zone.
		if len(adds) > 0 {
			ls, ok := liveState[pz.Zone]
			if !ok {
				// Should not happen: writableZones guarantees the zone was fetched.
				for _, a := range adds {
					zr.Ops[a.idx].Status = accountinventory.DNSOpFailed
					zr.Ops[a.idx].StatusReason = "zone live state not fetched"
				}
			} else {
				// Build the batch.
				var batch []cpanel.MassEditAddRecord
				for _, a := range adds {
					for _, rec := range a.op.Records {
						batch = append(batch, cpanel.MassEditAddRecord{
							DName:      dnsCanonToRelative(rec.Name, pz.Zone),
							TTL:        rec.TTL,
							RecordType: rec.Type,
							Data:       rec.Data,
						})
					}
				}

				result, writeErr := cpanel.MassEditZoneAdd(ctx, client, pz.Zone, ls.serial, batch)
				if writeErr != nil {
					status := accountinventory.DNSOpFailed
					reason := writeErr.Error()
					if cpanel.IsStaleSerialError(writeErr) {
						status = accountinventory.DNSOpRefused
						reason = "stale serial — zone was modified since fetch"
					}
					for _, a := range adds {
						zr.Ops[a.idx].Status = status
						zr.Ops[a.idx].StatusReason = reason
					}
				} else {
					zr.NewSerial = result.NewSerial

					// Verify-after: re-fetch the zone and check each
					// planned record is present (match by type+name+data).
					freshRecords, _, fetchErr := cpanel.FetchDNSZoneRaw(ctx, client, pz.Zone)
					if fetchErr != nil {
						for _, a := range adds {
							zr.Ops[a.idx].Status = accountinventory.DNSOpFailed
							zr.Ops[a.idx].StatusReason = "verify-after re-fetch failed: " + fetchErr.Error()
						}
					} else {
						for _, a := range adds {
							if dnsVerifyAddOpPresent(a.op, freshRecords, pz.Zone) {
								zr.Ops[a.idx].Status = accountinventory.DNSOpApplied
							} else {
								zr.Ops[a.idx].Status = accountinventory.DNSOpFailed
								zr.Ops[a.idx].StatusReason = "write reported success but the records are not observable in the fresh zone"
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

// dnsRecordPresent checks if a single planned record exists in the
// live zone.
func dnsRecordPresent(rec accountinventory.PlanRecord, fresh []cpanel.DNSRecord, zone string) bool {
	canonName := strings.ToLower(rec.Name)
	for _, f := range fresh {
		if !strings.EqualFold(f.Type, rec.Type) {
			continue
		}
		fCanon := strings.ToLower(f.Name)
		// parse_zone names can be relative or absolute; canonicalize both.
		if !strings.HasSuffix(fCanon, ".") {
			fCanon = fCanon + "." + strings.ToLower(zone) + "."
		}
		if fCanon != canonName {
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

	// Collect zones that have applied adds in the report.
	type rollbackTarget struct {
		zone    string
		opType  string
		opName  string
		records []accountinventory.PlanRecord
	}
	var targets []rollbackTarget
	if !isDegraded {
		for _, zr := range applyReport.Zones {
			for _, op := range zr.Ops {
				if op.Status == accountinventory.DNSOpApplied && op.Action == accountinventory.ActionAdd {
					targets = append(targets, rollbackTarget{
						zone:    zr.Zone,
						opType:  op.Type,
						opName:  op.Name,
						records: op.Records,
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
				fmt.Printf("  remove  [%s] %s %s (%d record(s))\n", t.zone, t.opType, t.opName, len(t.records))
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

		// Fetch the current zone to find line indexes.
		records, _, fetchErr := cpanel.FetchDNSZoneRaw(ctx, client, zone)
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
		serial, serialErr := cpanel.ExtractSOASerial(func() []byte {
			// Re-fetch raw for the serial.
			_, raw, err := cpanel.FetchDNSZoneRaw(ctx, client, zone)
			if err != nil {
				return nil
			}
			return raw
		}())
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

		// For each target, find the line indexes of matching records.
		var allLineIndexes []int
		for _, t := range zt {
			res := accountinventory.DNSApplyOpResult{
				PlanOp: accountinventory.PlanOp{Action: accountinventory.ActionAdd, Type: t.opType, Name: t.opName, Records: t.records},
			}
			var lines []int
			for _, planned := range t.records {
				line := findRecordLine(planned, records, zone)
				if line >= 0 {
					lines = append(lines, line)
				}
			}
			if len(lines) == 0 {
				res.Status = accountinventory.DNSOpSkipped
				res.StatusReason = "records already absent — nothing to remove"
			} else {
				res.Status = accountinventory.DNSOpApplied // placeholder
				allLineIndexes = append(allLineIndexes, lines...)
			}
			zr.Ops = append(zr.Ops, res)
		}

		if len(allLineIndexes) > 0 {
			_, removeErr := cpanel.MassEditZoneRemove(ctx, client, zone, serial, allLineIndexes)
			if removeErr != nil {
				status := accountinventory.DNSOpFailed
				reason := removeErr.Error()
				if cpanel.IsStaleSerialError(removeErr) {
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
				// Verify-after: re-fetch and check records are gone.
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
						}
						// else: stays applied (records are gone).
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

// findRecordLine finds the line index of a planned record in the live
// zone. Returns -1 if not found.
func findRecordLine(planned accountinventory.PlanRecord, live []cpanel.DNSRecord, zone string) int {
	canonName := strings.ToLower(planned.Name)
	for _, f := range live {
		if !strings.EqualFold(f.Type, planned.Type) {
			continue
		}
		fCanon := strings.ToLower(f.Name)
		if !strings.HasSuffix(fCanon, ".") {
			fCanon = fCanon + "." + strings.ToLower(zone) + "."
		}
		if fCanon != canonName {
			continue
		}
		if dnsDataMatch(planned, f) {
			return f.Line
		}
	}
	return -1
}
