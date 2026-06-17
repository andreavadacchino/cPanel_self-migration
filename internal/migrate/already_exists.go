package migrate

import "strings"

// alreadyExistsMarkers are case-insensitive substrings that indicate an "already
// exists" condition in a cPanel error message, across locales. cPanel LOCALIZES
// these messages, so matching only the English phrase is unreliable: the
// destination host in this project answers in Polish ("… już istnieje").
//
// These strings are NOT guesses — each was taken from cPanel's official locale
// repository (github.com/CpanelInc/cplocales), reading the real translations of
// the source phrases the destination's create calls produce. For databases/users
// (Cpanel/Admin/Base/DB.pm):
//
//	"The user “[_1]” cannot be created because it already exists."
//	"A [asis,MySQL] database with the name “[_1]” already exists."
//
// and for domains the equivalent is "The domain “[_1]” already exists in the
// userdata." (PL "Domena „[_1]” już istnieje w danych użytkownika.") plus the
// api2 addon "The domain “[_1]” already exists." — all of which still end in the
// per-locale "already exists" phrase captured here.
//
// Several languages phrase these messages DIFFERENTLY (e.g. Italian "esiste già"
// vs "già esistente"; Dutch "al bestaat" vs "bestaat al"; Ukrainian "вже існує"
// vs "уже існує"), so those locales contribute more than one marker. cPanel
// ships no German translation for these keys, so there is deliberately no German
// entry.
//
// IMPORTANT: this list is used ONLY to classify log severity and as a
// NON-AUTHORITATIVE fallback signal — never as the sole basis for control flow.
// Both database provisioning (provisionDest) and domain creation
// (reconcileDomainErrors) decide success from real STATE (a follow-up write, or
// re-reading whether the object now exists), not from this text. So an
// unrecognized locale at worst produces a slightly less specific log line or
// skips a fallback hint; it can never, on its own, turn a real failure into a
// success or vice versa.
var alreadyExistsMarkers = []string{
	"already exists", // English (source)
	"już istnieje",   // Polish — user + database + domain (verified live on the destination)
	"ya existe",      // Spanish — user + database
	"existe déjà",    // French — user + database
	"esiste già",     // Italian — user message
	"già esist",      // Italian — database message ("già esistente")
	"já existe",      // Portuguese (pt_BR) — user + database
	"al bestaat",     // Dutch — user message ("… al bestaat")
	"bestaat al",     // Dutch — database message ("bestaat al een …")
	"вже існує",      // Ukrainian — user message
	"уже існує",      // Ukrainian — database message (у-, not в-)
	"既に存在",           // Japanese — user + database ("既に存在")
	"已存在",            // Chinese — user + database
}

// isAlreadyExists reports whether a cPanel error looks like an "already exists"
// condition, in any of the recognized locales (matched case-insensitively).
// Shared by the database flow (apply_dbs.go) and the domain flow
// (apply_domains.go); see alreadyExistsMarkers for why it is only a
// log-severity / fallback hint and never the authority for control flow.
func isAlreadyExists(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, m := range alreadyExistsMarkers {
		if strings.Contains(msg, strings.ToLower(m)) {
			return true
		}
	}
	return false
}
