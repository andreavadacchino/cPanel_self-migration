package webfiles

import (
	"context"
	"fmt"
	"strings"
)

// DestTarget is one actionable destination docroot after remote canonicalization.
type DestTarget struct {
	Domain    string
	Raw       string
	Canonical string
}

// DestTargetIssue is a preflight failure for one destination docroot.
type DestTargetIssue struct {
	Domain string
	Raw    string
	Reason string
}

// ValidateDestTargets canonicalizes every actionable destination docroot on the
// destination host and rejects duplicate resolved targets before Step 11 mutates
// any filesystem state. Skipped/no-destination plan items are ignored.
func ValidateDestTargets(ctx context.Context, r Runner, items []WebPlanItem) ([]DestTarget, []DestTargetIssue) {
	targets := make([]DestTarget, 0, len(items))
	issues := make([]DestTargetIssue, 0)
	seen := make(map[string]DestTarget, len(items))
	duplicateReported := make(map[string]bool, len(items))

	for _, it := range items {
		if it.Skip || it.DestDocroot == "" {
			continue
		}
		canon, err := CanonicalDestDocroot(ctx, r, it.DestDocroot, it.AllowDestPublicHTMLRoot)
		if err != nil {
			issues = append(issues, DestTargetIssue{
				Domain: it.Domain,
				Raw:    it.DestDocroot,
				Reason: err.Error(),
			})
			continue
		}

		cur := DestTarget{Domain: it.Domain, Raw: it.DestDocroot, Canonical: canon}
		if prev, ok := seen[canon]; ok {
			reason := fmt.Sprintf("duplicate destination docroot: %s (%s) and %s (%s) resolve to %s",
				prev.Domain, prev.Raw, it.Domain, it.DestDocroot, canon)
			if !duplicateReported[canon] {
				issues = append(issues, DestTargetIssue{
					Domain: prev.Domain,
					Raw:    prev.Raw,
					Reason: reason,
				})
				duplicateReported[canon] = true
			}
			issues = append(issues, DestTargetIssue{
				Domain: it.Domain,
				Raw:    it.DestDocroot,
				Reason: reason,
			})
			continue
		}
		seen[canon] = cur
		targets = append(targets, cur)
	}
	return targets, issues
}

// CanonicalDestDocroot returns the destination host's canonical path for docroot,
// after applying the same containment guard used by empty/backup/extract.
// allowRoot is the per-docroot opt-in (WebPlanItem.AllowDestPublicHTMLRoot) that
// lets the guard accept ~/public_html itself as the target.
func CanonicalDestDocroot(ctx context.Context, r Runner, docroot string, allowRoot bool) (string, error) {
	out, err := r.RunScript(ctx, canonicalDestDocrootScript(), destDocrootEnv(docroot, allowRoot))
	if err != nil {
		return "", fmt.Errorf("canonicalize destination docroot %q: %w", docroot, err)
	}
	canon := strings.TrimSpace(string(out))
	if canon == "" {
		return "", fmt.Errorf("canonicalize destination docroot %q: no canonical path returned", docroot)
	}
	if strings.Contains(canon, "\n") {
		return "", fmt.Errorf("canonicalize destination docroot %q: unexpected output %q", docroot, canon)
	}
	return canon, nil
}

func canonicalDestDocrootScript() string {
	return destDocrootGuardScript() + `d="$(guard_dest_docroot "$DEST_DOCROOT")" || exit $?
printf '%s\n' "$d"
`
}

// destDocrootEnv is the single source for the guarded destination scripts' env:
// the docroot plus, only when the plan item opted in, the guard's
// ALLOW_PUBLIC_HTML_ROOT flag. Never set the flag unconditionally — its absence
// is what keeps the public_html root refusal active for every other docroot.
func destDocrootEnv(docroot string, allowRoot bool) map[string]string {
	env := map[string]string{"DEST_DOCROOT": docroot}
	if allowRoot {
		env["ALLOW_PUBLIC_HTML_ROOT"] = "1"
	}
	return env
}
