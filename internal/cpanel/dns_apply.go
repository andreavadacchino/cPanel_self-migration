package cpanel

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/tis24dev/cPanel_self-migration/internal/logx"
)

// DNS write primitives (PR 6D) — the ONLY DNS writer. Called exclusively
// by the `dns apply` subcommand against the DESTINATION host. The sole
// write API is DNS::mass_edit_zone with serial guard (optimistic locking).
// Byte-verified in PR6D_PRE_CAPTURES.md.

// MassEditAddRecord is one record to add via mass_edit_zone. Fields
// match the cPanel API: dname (relative for sub-domains, the FQDN zone name
// with trailing dot for the apex — mass_edit_zone REJECTS "@"; see
// dnsCanonToRelative), ttl, record_type, data (array of strings, e.g. TXT
// segments).
type MassEditAddRecord struct {
	DName      string   `json:"dname"`
	TTL        int      `json:"ttl"`
	RecordType string   `json:"record_type"`
	Data       []string `json:"data"`
}

// MassEditResult is the response from a successful mass_edit_zone call.
type MassEditResult struct {
	NewSerial string `json:"new_serial"`
}

// MassEditZoneAdd adds records to a DNS zone via DNS::mass_edit_zone
// with serial guard (6D-pre fact 1: add-0=, add-1=, ... indexed format).
// The serial must be from a FRESH parse_zone — a stale serial causes a
// fail-safe refusal (6D-pre fact 3).
func MassEditZoneAdd(ctx context.Context, c Runner, zone, serial string, records []MassEditAddRecord) (MassEditResult, error) {
	args := map[string]string{
		"zone":   zone,
		"serial": serial,
	}
	for i, r := range records {
		b, err := json.Marshal(r)
		if err != nil {
			return MassEditResult{}, fmt.Errorf("marshal add record %d: %w", i, err)
		}
		args[fmt.Sprintf("add-%d", i)] = string(b)
	}
	data, err := RunUAPI[MassEditResult](ctx, c, "DNS", "mass_edit_zone", args)
	if err != nil {
		return MassEditResult{}, err
	}
	logx.Debug("MassEditZoneAdd(%s, serial=%s): %d record(s) added, new_serial=%s",
		zone, serial, len(records), data.NewSerial)
	return data, nil
}

// MassEditZoneRemove removes records by line index via DNS::mass_edit_zone
// with serial guard (6D-pre: remove-0=, remove-1=, ... indexed format).
// This is the ROLLBACK primitive: the only DNS removes the tool ever
// emits are the inverses of its own applied adds.
func MassEditZoneRemove(ctx context.Context, c Runner, zone, serial string, lineIndexes []int) (MassEditResult, error) {
	args := map[string]string{
		"zone":   zone,
		"serial": serial,
	}
	for i, idx := range lineIndexes {
		args[fmt.Sprintf("remove-%d", i)] = strconv.Itoa(idx)
	}
	data, err := RunUAPI[MassEditResult](ctx, c, "DNS", "mass_edit_zone", args)
	if err != nil {
		return MassEditResult{}, err
	}
	logx.Debug("MassEditZoneRemove(%s, serial=%s): %d line(s) removed, new_serial=%s",
		zone, serial, len(lineIndexes), data.NewSerial)
	return data, nil
}

// FetchDNSZoneRaw returns the raw parse_zone UAPI response bytes
// alongside the normalized records — for the pre-write backup (the
// backup archives the verbatim server state).
func FetchDNSZoneRaw(ctx context.Context, c Runner, zone string) ([]DNSRecord, []byte, error) {
	data, raw, err := RunUAPIRaw[[]uapiDNSRawRecord](ctx, c, "DNS", "parse_zone",
		map[string]string{"zone": zone})
	if err != nil {
		return nil, nil, err
	}
	records, warns := normalizeUAPIRecords(data)
	for _, w := range warns {
		logx.Debug("FetchDNSZoneRaw(%s): %s", zone, w)
	}
	return records, raw, nil
}

// ExtractSOASerial finds the SOA serial from a parse_zone response.
// The serial is base64-encoded in data_b64[2] of the SOA record
// (6D-pre fact 4).
func ExtractSOASerial(raw []byte) (string, error) {
	type rawRec struct {
		RecordType string   `json:"record_type"`
		DataB64    []string `json:"data_b64"`
	}
	type resp struct {
		Result struct {
			Data []rawRec `json:"data"`
		} `json:"result"`
	}
	var r resp
	if err := json.Unmarshal(raw, &r); err != nil {
		return "", fmt.Errorf("parse zone response: %w", err)
	}
	for _, rec := range r.Result.Data {
		if rec.RecordType == "SOA" && len(rec.DataB64) >= 3 {
			decoded, err := base64.StdEncoding.DecodeString(rec.DataB64[2])
			if err != nil {
				return "", fmt.Errorf("decode SOA serial base64 %q: %w", rec.DataB64[2], err)
			}
			serial := strings.TrimSpace(string(decoded))
			if serial == "" {
				return "", fmt.Errorf("SOA serial is empty after decode")
			}
			return serial, nil
		}
	}
	return "", fmt.Errorf("no SOA record found in zone response")
}

// MassEditZoneBatch combines remove and add operations in a SINGLE
// mass_edit_zone call (v2 replace design: removes processed first,
// then adds, in one zone-file write). When removeLines is empty it is
// equivalent to MassEditZoneAdd; when addRecords is empty it is
// equivalent to MassEditZoneRemove.
func MassEditZoneBatch(ctx context.Context, c Runner, zone, serial string, removeLines []int, addRecords []MassEditAddRecord) (MassEditResult, error) {
	args := map[string]string{
		"zone":   zone,
		"serial": serial,
	}
	for i, idx := range removeLines {
		args[fmt.Sprintf("remove-%d", i)] = strconv.Itoa(idx)
	}
	for i, r := range addRecords {
		b, err := json.Marshal(r)
		if err != nil {
			return MassEditResult{}, fmt.Errorf("marshal add record %d: %w", i, err)
		}
		args[fmt.Sprintf("add-%d", i)] = string(b)
	}
	data, err := RunUAPI[MassEditResult](ctx, c, "DNS", "mass_edit_zone", args)
	if err != nil {
		return MassEditResult{}, err
	}
	logx.Debug("MassEditZoneBatch(%s, serial=%s): %d remove(s) + %d add(s), new_serial=%s",
		zone, serial, len(removeLines), len(addRecords), data.NewSerial)
	return data, nil
}

// IsStaleSerialError reports whether a mass_edit_zone error is the
// stale-serial refusal (6D-pre fact 3).
func IsStaleSerialError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "does not match the DNS zone")
}
