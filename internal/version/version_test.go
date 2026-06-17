package version

import (
	"runtime/debug"
	"testing"
)

func TestStringFromLdflags(t *testing.T) {
	orig := Version
	t.Cleanup(func() { Version = orig })

	Version = "v1.2.3"
	if got := String(); got != "1.2.3" {
		t.Errorf("String() = %q, want %q (leading v stripped)", got, "1.2.3")
	}
}

func TestStringFallbackToBuildInfo(t *testing.T) {
	origVer := Version
	origRead := readBuildInfo
	t.Cleanup(func() { Version = origVer; readBuildInfo = origRead })

	Version = ""
	readBuildInfo = func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{Main: debug.Module{Version: "v2.0.0"}}, true
	}
	if got := String(); got != "2.0.0" {
		t.Errorf("String() = %q, want %q", got, "2.0.0")
	}
}

// TestStringDefaultPlaceholderPrefersBuildInfo uses the REAL default (the
// placeholder, not ""): a `go install module@vX.Y.Z` build leaves Version at the
// placeholder but records the tag in build info, which must then be reported.
func TestStringDefaultPlaceholderPrefersBuildInfo(t *testing.T) {
	origVer := Version
	origRead := readBuildInfo
	t.Cleanup(func() { Version = origVer; readBuildInfo = origRead })

	Version = "0.0.0-dev" // the shipped default, not ""
	readBuildInfo = func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{Main: debug.Module{Version: "v2.0.0"}}, true
	}
	if got := String(); got != "2.0.0" {
		t.Errorf("String() with default placeholder + build-info tag = %q, want %q", got, "2.0.0")
	}
}

func TestStringDevPlaceholder(t *testing.T) {
	origVer := Version
	origRead := readBuildInfo
	t.Cleanup(func() { Version = origVer; readBuildInfo = origRead })

	Version = ""
	readBuildInfo = func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}, true
	}
	if got := String(); got != "0.0.0-dev" {
		t.Errorf("String() = %q, want %q", got, "0.0.0-dev")
	}
}
