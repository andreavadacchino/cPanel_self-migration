package cpanel

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/tis24dev/cPanel_self-migration/internal/logx"
)

// DNSRecord is a single normalized DNS record, produced from either UAPI
// DNS::parse_zone or API2 ZoneEdit::fetchzone_records. Type-specific fields
// are populated when applicable; unknown record types preserve their raw API
// response in the Raw field.
type DNSRecord struct {
	Type     string          `json:"type"`
	Name     string          `json:"name"`
	TTL      int             `json:"ttl"`
	Value    string          `json:"value"`
	Priority int             `json:"priority,omitempty"`
	Exchange string          `json:"exchange,omitempty"`
	Address  string          `json:"address,omitempty"`
	Target   string          `json:"target,omitempty"`
	TxtData  string          `json:"txtdata,omitempty"`
	Class    string          `json:"class,omitempty"`
	Line     int             `json:"line,omitempty"`
	Raw      json.RawMessage `json:"raw,omitempty"`
}

// ---------------------------------------------------------------------------
// UAPI DNS::parse_zone (cPanel >= v136)
// ---------------------------------------------------------------------------

type uapiDNSRawRecord struct {
	DNameB64   string    `json:"dname_b64"`
	RecordType string    `json:"record_type"`
	DataB64    []string  `json:"data_b64"`
	TTL        flexInt64 `json:"ttl"` // number OR quoted string across cPanel builds
	LineIndex  int       `json:"line_index"`
	Type       string    `json:"type"` // "record", "control", "comment"
}

func FetchDNSZoneUAPI(ctx context.Context, c Runner, zone string) ([]DNSRecord, error) {
	data, err := RunUAPI[[]uapiDNSRawRecord](ctx, c, "DNS", "parse_zone",
		map[string]string{"zone": zone})
	if err != nil {
		return nil, err
	}
	records, warns := normalizeUAPIRecords(data)
	for _, w := range warns {
		logx.Debug("FetchDNSZoneUAPI(%s): %s", zone, w)
	}
	logx.Debug("FetchDNSZoneUAPI(%s): %d record(s)", zone, len(records))
	return records, nil
}

func normalizeUAPIRecords(raw []uapiDNSRawRecord) ([]DNSRecord, []string) {
	var records []DNSRecord
	var warnings []string
	for _, r := range raw {
		if r.Type != "record" {
			continue
		}
		dname, err := decodeB64Field(r.DNameB64)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("line %d: dname decode: %v", r.LineIndex, err))
			dname = r.DNameB64
		}

		var decodedData []string
		for _, d := range r.DataB64 {
			val, err := decodeB64Field(d)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("line %d: data decode: %v", r.LineIndex, err))
				val = d
			}
			decodedData = append(decodedData, val)
		}

		rec := DNSRecord{
			Type: r.RecordType,
			Name: dname,
			TTL:  int(r.TTL),
			Line: r.LineIndex,
		}

		switch r.RecordType {
		case "A", "AAAA":
			if len(decodedData) > 0 {
				rec.Address = decodedData[0]
				rec.Value = decodedData[0]
			}
		case "CNAME":
			if len(decodedData) > 0 {
				rec.Target = decodedData[0]
				rec.Value = decodedData[0]
			}
		case "MX":
			if len(decodedData) > 1 {
				rec.Exchange = decodedData[1]
				rec.Value = decodedData[1]
				if p, err := parseInt(decodedData[0]); err == nil {
					rec.Priority = p
				}
			} else if len(decodedData) > 0 {
				rec.Exchange = decodedData[0]
				rec.Value = decodedData[0]
			}
		case "TXT":
			// Long TXT values (DKIM keys) arrive split into 255-char
			// segments; RFC 1035 semantics concatenate them.
			if len(decodedData) > 0 {
				joined := strings.Join(decodedData, "")
				rec.TxtData = joined
				rec.Value = joined
			}
		case "NS":
			if len(decodedData) > 0 {
				rec.Target = decodedData[0]
				rec.Value = decodedData[0]
			}
		default:
			if len(decodedData) > 0 {
				rec.Value = decodedData[0]
			}
			rawBytes, _ := json.Marshal(r)
			rec.Raw = rawBytes
		}

		records = append(records, rec)
	}
	sort.SliceStable(records, func(i, j int) bool { return records[i].Line < records[j].Line })
	return records, warnings
}

func decodeB64Field(s string) (string, error) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return "", fmt.Errorf("base64 decode: %w", err)
	}
	if utf8.Valid(b) {
		return string(b), nil
	}
	return fmt.Sprintf("%x", b), nil
}

func parseInt(s string) (int, error) {
	var n int
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("not a number: %q", s)
		}
		n = n*10 + int(c-'0')
	}
	return n, nil
}

// parseUAPIDNSZone is exposed for unit testing against fixture bytes.
func parseUAPIDNSZone(out []byte) ([]DNSRecord, error) {
	data, err := parseUAPI[[]uapiDNSRawRecord]("DNS", "parse_zone", out)
	if err != nil {
		return nil, err
	}
	records, _ := normalizeUAPIRecords(data)
	return records, nil
}

// ---------------------------------------------------------------------------
// API2 ZoneEdit::fetchzone_records (legacy fallback)
// ---------------------------------------------------------------------------

// api2DNSRawRecord is one entry of ZoneEdit::fetchzone_records. Verified
// against a live cPanel 110.0 (build 131): numeric fields arrive as EITHER
// bare numbers or quoted strings depending on the field and record type
// (e.g. the $TTL pseudo-record carries ttl:"14400" while an A record carries
// ttl:14400; MX preference and all SOA counters are quoted strings), so every
// numeric field uses flexInt64 — a plain int would fail the WHOLE zone decode.
type api2DNSRawRecord struct {
	Line       flexInt64 `json:"line"`
	Type       string    `json:"type"`
	Name       string    `json:"name"`
	TTL        flexInt64 `json:"ttl"`
	Class      string    `json:"class"`
	Record     string    `json:"record"`
	Address    string    `json:"address,omitempty"`
	Cname      string    `json:"cname,omitempty"`
	Exchange   string    `json:"exchange,omitempty"`
	Preference flexInt64 `json:"preference,omitempty"`
	TxtData    string    `json:"txtdata,omitempty"`
	NSDName    string    `json:"nsdname,omitempty"`
	MName      string    `json:"mname,omitempty"`
	RName      string    `json:"rname,omitempty"`
	Serial     flexInt64 `json:"serial,omitempty"`
	Refresh    flexInt64 `json:"refresh,omitempty"`
	Retry      flexInt64 `json:"retry,omitempty"`
	Expire     flexInt64 `json:"expire,omitempty"`
	Minimum    flexInt64 `json:"minimum,omitempty"`
	RawField   string    `json:"raw,omitempty"`
}

func FetchDNSZoneAPI2(ctx context.Context, c Runner, domain string) ([]DNSRecord, error) {
	data, err := RunAPI2[[]api2DNSRawRecord](ctx, c, "ZoneEdit", "fetchzone_records",
		map[string]string{"domain": domain})
	if err != nil {
		return nil, err
	}
	records := normalizeAPI2Records(data)
	logx.Debug("FetchDNSZoneAPI2(%s): %d record(s)", domain, len(records))
	return records, nil
}

func normalizeAPI2Records(raw []api2DNSRawRecord) []DNSRecord {
	var records []DNSRecord
	for _, r := range raw {
		rec := DNSRecord{
			Type:  r.Type,
			Name:  r.Name,
			TTL:   int(r.TTL),
			Class: r.Class,
			Line:  int(r.Line),
		}

		switch r.Type {
		case "A", "AAAA":
			rec.Address = r.Address
			rec.Value = r.Address
		case "CNAME":
			rec.Target = r.Cname
			rec.Value = r.Cname
		case "MX":
			rec.Exchange = r.Exchange
			rec.Priority = int(r.Preference)
			rec.Value = r.Exchange
		case "TXT":
			rec.TxtData = r.TxtData
			rec.Value = r.TxtData
		case "NS":
			rec.Target = r.NSDName
			rec.Value = r.NSDName
		case "SOA":
			rec.Value = fmt.Sprintf("%s %s %d", r.MName, r.RName, int64(r.Serial))
			rawBytes, _ := json.Marshal(r)
			rec.Raw = rawBytes
		case ":RAW":
			rec.Value = r.RawField
			rawBytes, _ := json.Marshal(r)
			rec.Raw = rawBytes
		default:
			rec.Value = r.Record
			rawBytes, _ := json.Marshal(r)
			rec.Raw = rawBytes
		}

		records = append(records, rec)
	}
	sort.SliceStable(records, func(i, j int) bool { return records[i].Line < records[j].Line })
	return records
}

// parseAPI2DNSZone is exposed for unit testing against fixture bytes.
func parseAPI2DNSZone(out []byte) ([]DNSRecord, error) {
	data, err := parseAPI2[[]api2DNSRawRecord]("ZoneEdit", "fetchzone_records", out)
	if err != nil {
		return nil, err
	}
	return normalizeAPI2Records(data), nil
}
