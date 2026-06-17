// Package version exposes the binary's build metadata. The variables are
// populated at build time via -ldflags (GoReleaser injects them); they fall
// back to sensible development defaults otherwise.
package version

import (
	"runtime/debug"
	"strings"
)

// readBuildInfo is a seam for testing.
var readBuildInfo = debug.ReadBuildInfo

// devPlaceholder is the version reported for a plain development build — when
// neither ldflags nor the Go build info supply a real version.
const devPlaceholder = "0.0.0-dev"

// These variables are populated at build time via -ldflags. GoReleaser injects:
//
//	-X github.com/tis24dev/cPanel_self-migration/internal/version.Version=v0.9.0
//	-X github.com/tis24dev/cPanel_self-migration/internal/version.Commit=abcdef123
//	-X github.com/tis24dev/cPanel_self-migration/internal/version.Date=2025-01-01T12:34:56Z
var (
	// Version holds the semantic version of the binary, injected by the build
	// system. Left at devPlaceholder for a plain build; String() then prefers the
	// Go build-info version (set for `go install module@vX.Y.Z`) when available.
	Version = devPlaceholder

	// Commit holds the VCS commit hash used to build the binary (optional).
	Commit = ""

	// Date holds the build timestamp (optional).
	Date = ""
)

// String returns the effective version string. Preference order:
//  1. an explicit version injected into Version via ldflags (i.e. not the
//     development placeholder),
//  2. the main module version from debug.ReadBuildInfo, e.g. for a
//     `go install module@vX.Y.Z` build (if not "(devel)"),
//  3. the development placeholder.
//
// Build info is consulted both when Version is empty AND when it is still the
// placeholder: a `go install @tag` build does not set the ldflags Version but
// does record the tag in build info, so without this it would misreport the
// placeholder instead of the real tag.
//
// Any leading "v" is stripped.
func String() string {
	v := strings.TrimSpace(Version)

	if v == "" || v == devPlaceholder {
		if info, ok := readBuildInfo(); ok {
			if mv := strings.TrimSpace(info.Main.Version); mv != "" && mv != "(devel)" {
				v = mv
			}
		}
	}

	if v == "" {
		v = devPlaceholder
	}

	return strings.TrimPrefix(v, "v")
}
