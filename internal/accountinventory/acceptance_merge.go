package accountinventory

// MergeAcceptance upserts one operator acceptance into an acceptance file,
// binding the whole file to the checklist the operator was looking at. It
// is the write-side counterpart of the CLI's acceptance loader (PR 7D) and
// is used by the browser accept flow (UI 2b).
//
//   - existing may be nil (a fresh file); its prior entries are preserved
//     in order.
//   - an entry with the same ActionKey is UPDATED in place (reason/author/
//     date), never duplicated — re-accepting an action revises it.
//   - the whole file is (re)stamped with the CURRENT checklistFile /
//     checklistSHA256, so the strict hash check in loadAcceptancesFile
//     keeps matching after the checklist is regenerated with these
//     acceptances applied.
func MergeAcceptance(existing *AcceptanceFile, checklistFile, checklistSHA256 string, acc OperatorAcceptance) AcceptanceFile {
	out := AcceptanceFile{
		Mode:            AcceptanceFileMode,
		FormatVersion:   1,
		ChecklistFile:   checklistFile,
		ChecklistSHA256: checklistSHA256,
	}
	if existing != nil {
		out.Acceptances = append(out.Acceptances, existing.Acceptances...)
	}
	for i := range out.Acceptances {
		if out.Acceptances[i].ActionKey == acc.ActionKey {
			out.Acceptances[i] = acc
			return out
		}
	}
	out.Acceptances = append(out.Acceptances, acc)
	return out
}
