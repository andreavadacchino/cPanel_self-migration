package cpanel

import (
	"encoding/json"
	"strconv"
	"strings"
)

// flexInt64 is an integer that JSON-decodes from EITHER a number (123) or a
// quoted string ("123"): some cPanel builds return numeric fields as strings
// (notably Mysql::list_databases' disk_usage), and a plain int64 field would then
// fail the ENTIRE response unmarshal. It is used for informational fields, so
// null / empty / non-numeric values decode to 0 and NEVER return an error — a
// surprising value can never abort a migration over a cosmetic byte count.
type flexInt64 int64

func (f *flexInt64) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "" || s == "null" {
		return nil // leave 0
	}
	s = strings.TrimSpace(strings.Trim(s, `"`)) // unwrap a quoted "123"
	if s == "" {
		return nil
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		*f = flexInt64(n)
		return nil
	}
	// Some builds report a fractional value (e.g. Ftp::list_ftp_with_disk's
	// diskused = "57632.08" or a bare 13558.40). Truncate to the integer part
	// rather than collapse to 0, which would zero out every disk figure.
	if v, err := strconv.ParseFloat(s, 64); err == nil {
		*f = flexInt64(int64(v))
		return nil
	}
	// A non-numeric value (e.g. "" handled above, or unexpected text) stays 0:
	// this field is informational only, so we never fail the surrounding decode.
	return nil
}

// flexStringList decodes a field that a cPanel build returns as EITHER a
// single string or a JSON array of strings (notably SSL::list_certs' domains,
// a SAN list). Values are flattened to a comma-joined string — the form the
// diff/policy layers already key on — so a shape change can never fail the
// whole response and silently drop the section.
type flexStringList string

func (f *flexStringList) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "" || s == "null" {
		return nil
	}
	if s[0] == '[' {
		var arr []string
		if err := json.Unmarshal(b, &arr); err != nil {
			return nil // never fail the surrounding decode over a cosmetic field
		}
		*f = flexStringList(strings.Join(arr, ","))
		return nil
	}
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		*f = flexStringList(one)
	}
	return nil
}

// envelope is the standard UAPI result wrapper:
//
//	{"result":{"data":...,"errors":[...],"messages":[...],"status":1}}
type envelope[T any] struct {
	Result struct {
		Data     T               `json:"data"`
		Errors   json.RawMessage `json:"errors"`   // null | [] | ["msg"...]
		Messages json.RawMessage `json:"messages"` // null | [] | ["msg"...]
		Status   int             `json:"status"`   // 1 = success
	} `json:"result"`
}

// ListDomainsData is the data field of DomainInfo::list_domains.
type ListDomainsData struct {
	MainDomain    string   `json:"main_domain"`
	AddonDomains  []string `json:"addon_domains"`
	SubDomains    []string `json:"sub_domains"`
	ParkedDomains []string `json:"parked_domains"`
}

// DomainDataEntry is one domain's hosting configuration from
// DomainInfo::domains_data. It is the AUTHORITATIVE source of a domain's
// document root on a host — paths must never be guessed, since the SOURCE and
// DESTINATION cPanel accounts can lay docroots out differently (e.g. addons in
// dedicated HOME dirs on one host vs under public_html/ on the other).
type DomainDataEntry struct {
	Domain       string `json:"domain"`
	DocumentRoot string `json:"documentroot"`
	Type         string `json:"type"` // main_domain | addon_domain | sub_domain | parked_domain
	HomeDir      string `json:"homedir"`
	ServerName   string `json:"servername"`
}

// DomainsData is the data field of DomainInfo::domains_data. Unlike
// list_domains (flat name lists), each section here is a rich object: main is a
// single entry, the others are arrays of entries.
type DomainsData struct {
	MainDomain    DomainDataEntry   `json:"main_domain"`
	AddonDomains  []DomainDataEntry `json:"addon_domains"`
	SubDomains    []DomainDataEntry `json:"sub_domains"`
	ParkedDomains []DomainDataEntry `json:"parked_domains"`
}

// DatabaseEntry is one MySQL database from Mysql::list_databases. The disk usage
// is bytes on disk; users are the MySQL accounts granted on it. The database name
// carries the cPanel account prefix (e.g. "srcacct_wp694") on both sides.
type DatabaseEntry struct {
	Database  string    `json:"database"`
	DiskUsage flexInt64 `json:"disk_usage"` // number OR quoted string across cPanel builds
	Users     []string  `json:"users"`
}

// DBUserEntry is one MySQL user from Mysql::list_users. ShortUser is the name
// without the account prefix; Databases are the databases the user can access.
type DBUserEntry struct {
	User      string   `json:"user"`
	ShortUser string   `json:"shortuser"`
	Databases []string `json:"databases"`
}

// MySQLRestrictions is the data field of Mysql::get_restrictions. Prefix is nil
// when database prefixing is disabled for the cPanel account.
type MySQLRestrictions struct {
	MaxDatabaseNameLength int     `json:"max_database_name_length"`
	MaxUsernameLength     int     `json:"max_username_length"`
	Prefix                *string `json:"prefix"`
}

// CreateTokenData is the data field of Tokens::create_full_access.
//
// ExpiresAt binds the documented `expires_at` field. Expires/Expiry bind two
// alternate spellings as a best-effort TRIPWIRE: they exist NOT to be used as
// the expiry but to DETECT a field-name mismatch. If ExpiresAt is 0 (looks like
// "host ignored the expiry") yet an alternate is non-zero, the host really did
// return an expiry under a name we don't bind — a parsing bug we surface (fail
// closed) rather than silently treat as "ignored". This is not exhaustive (a
// host could use yet another name and still be masked); it is a cheap guard, and
// the real bound on a non-expiring token remains the caller's immediate revoke
// after use. See token.go and docs/DEBUGGING.md §3.
type CreateTokenData struct {
	Name      string `json:"name"`
	Token     string `json:"token"`
	ExpiresAt int64  `json:"expires_at"`
	CreatedAt int64  `json:"create_time"`
	Expires   int64  `json:"expires"`
	Expiry    int64  `json:"expiry"`
}

// hasUnboundExpiry reports that the bound expires_at is 0 but an alternate
// expiry field carries a value — i.e. the host returned an expiry under a field
// name this tool does not decode, which must NOT be mistaken for "no expiry".
func (d CreateTokenData) hasUnboundExpiry() bool {
	return d.ExpiresAt == 0 && (d.Expires != 0 || d.Expiry != 0)
}

// api2Envelope is the generic API2 response wrapper for cpapi2 CLI calls:
//
//	{"cpanelresult":{"data":...,"event":{"result":1},"error":"..."}}
type api2Envelope[T any] struct {
	CPanelResult struct {
		Data  T `json:"data"`
		Event struct {
			Result json.Number `json:"result"`
		} `json:"event"`
		Error string `json:"error"`
	} `json:"cpanelresult"`
}

// api2Response is the legacy api2 wrapper used by AddonDomain::addaddondomain:
//
//	{"cpanelresult":{"data":[{"result":1,"reason":"..."}], "event":{"result":1}}}
type api2Response struct {
	CPanelResult struct {
		Data []struct {
			Result json.Number `json:"result"`
			Reason string      `json:"reason"`
		} `json:"data"`
		Event struct {
			Result json.Number `json:"result"`
			Reason string      `json:"reason"`
		} `json:"event"`
		Error string `json:"error"`
	} `json:"cpanelresult"`
}

// errStrings best-effort decodes a UAPI errors/messages field (which may be
// null, [], or a JSON array of strings) into a slice.
func errStrings(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var arr []string
	if err := json.Unmarshal(raw, &arr); err == nil {
		return arr
	}
	// Some endpoints return a single string instead of an array.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil && s != "" {
		return []string{s}
	}
	return nil
}
