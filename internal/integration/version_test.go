package integration

import (
	"strings"
	"testing"
)

func TestExtractVersionTripleParsesCommonOutputs(t *testing.T) {
	cases := []struct {
		in    string
		want  versionTriple
		found bool
	}{
		{"0.14.0", versionTriple{0, 14, 0}, true},
		{"v1.2.3", versionTriple{1, 2, 3}, true},
		{"kimi-code 0.14.0 (linux/x64)", versionTriple{0, 14, 0}, true},
		{"0.14", versionTriple{0, 14, 0}, true},
		{"0.14.1-beta.2", versionTriple{0, 14, 1}, true},
		{"no version here", versionTriple{}, false},
		{"", versionTriple{}, false},
	}
	for _, tc := range cases {
		got, found := extractVersionTriple(tc.in)
		if found != tc.found || got != tc.want {
			t.Errorf("extractVersionTriple(%q) = %v, %v; want %v, %v", tc.in, got, found, tc.want, tc.found)
		}
	}
}

func TestExtractVersionTripleOrdersVersions(t *testing.T) {
	old, _ := extractVersionTriple("0.12.1")
	min, _ := extractVersionTriple(kimiMinVersion)
	newer, _ := extractVersionTriple("0.15.0")
	if !old.less(min) {
		t.Error("expected 0.12.1 < min")
	}
	if min.less(min) {
		t.Error("expected min == min, not less")
	}
	if !min.less(newer) {
		t.Error("expected min < 0.15.0")
	}
}

func TestVersionRequirementOnlySetForKimi(t *testing.T) {
	req := versionRequirementFor(TargetKimi)
	if req == nil {
		t.Fatal("kimi must have a version requirement")
	}
	if req.binary != "kimi" || req.minVersion != kimiMinVersion {
		t.Fatalf("unexpected kimi requirement: %+v", req)
	}
	if versionRequirementFor(TargetClaude) != nil || versionRequirementFor(TargetCodex) != nil {
		t.Error("only kimi should carry a version requirement")
	}
}

func TestEnforceAgentVersionWarnsWhenBinaryMissing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	warning, err := enforceAgentVersion(versionRequirementFor(TargetKimi))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "warning: could not run `kimi --version` to verify the installed version; hooks require kimi code 0.14.0 or newer"
	if warning != want {
		t.Fatalf("warning = %q, want %q", warning, want)
	}
}

func TestEnforceAgentVersionWarnsOnUnparsableOutput(t *testing.T) {
	fakeBinary(t, "kimi", "#!/bin/sh\necho 'no version here'\n")
	warning, err := enforceAgentVersion(versionRequirementFor(TargetKimi))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "warning: could not parse the kimi code version from `kimi --version` output; hooks require kimi code 0.14.0 or newer"
	if warning != want {
		t.Fatalf("warning = %q, want %q", warning, want)
	}
}

func TestEnforceAgentVersionRejectsOldVersion(t *testing.T) {
	fakeBinary(t, "kimi", "#!/bin/sh\necho 'kimi-code 0.12.1 (darwin/arm64)'\n")
	_, err := enforceAgentVersion(versionRequirementFor(TargetKimi))
	if err == nil {
		t.Fatal("expected error for old version")
	}
	want := "kimi code 0.12.1 is too old: cats hooks require kimi code 0.14.0 or newer. upgrade kimi code, then re-run install"
	if err.Error() != want {
		t.Fatalf("error = %q, want %q", err.Error(), want)
	}
}

func TestEnforceAgentVersionAcceptsCurrentVersion(t *testing.T) {
	fakeBinary(t, "kimi", "#!/bin/sh\necho 'kimi-code 0.14.0'\n")
	warning, err := enforceAgentVersion(versionRequirementFor(TargetKimi))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if warning != "" {
		t.Fatalf("unexpected warning: %q", warning)
	}
}

func TestParseIntegrationVersionMarkerForms(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    int
		found   bool
	}{
		{"slash comment", "// managed by cats\n// CATS_INTEGRATION_VERSION=2\n", 2, true},
		{"hash comment", "#!/bin/sh\n# CATS_INTEGRATION_VERSION=3\n", 3, true},
		{"hash no space", "#CATS_INTEGRATION_VERSION=4\n", 4, true},
		{"bare marker", "CATS_INTEGRATION_VERSION=5\n", 5, true},
		{"padded value", "# CATS_INTEGRATION_VERSION= 7 \n", 7, true},
		{"missing marker", "#!/bin/sh\necho hi\n", 0, false},
		{"garbage value", "# CATS_INTEGRATION_VERSION=abc\n", 0, false},
		{"empty", "", 0, false},
	}
	for _, tc := range cases {
		got, found := parseIntegrationVersion(tc.content)
		if found != tc.found || (found && got != tc.want) {
			t.Errorf("%s: parseIntegrationVersion = %d, %v; want %d, %v", tc.name, got, found, tc.want, tc.found)
		}
	}
}

func TestUpdateInstructions(t *testing.T) {
	if got := UpdateInstructions(nil); got != "" {
		t.Errorf("empty: %q", got)
	}
	if got := UpdateInstructions([]Target{TargetPi}); got != "run `catctl integration install pi`" {
		t.Errorf("one: %q", got)
	}
	got := UpdateInstructions([]Target{TargetPi, TargetOmp, TargetClaude})
	want := "run `catctl integration install pi`, `catctl integration install omp` and `catctl integration install claude`"
	if got != want {
		t.Errorf("many: %q, want %q", got, want)
	}
}

func TestOutdatedUpdateNoticeAndLegacyMarker(t *testing.T) {
	home := testHome(t)

	// A pi extension without a version marker is a legacy install: Outdated.
	piPath := home + "/.pi/agent/extensions/" + piExtensionInstallName
	mustWriteFile(t, piPath, "// managed by cats\nexport {}\n")

	var pi *Status
	for _, status := range InstalledIntegrationStatuses() {
		if status.Target == TargetPi {
			s := status
			pi = &s
		}
	}
	if pi == nil {
		t.Fatal("pi status missing")
	}
	if pi.State != StatusOutdated || pi.InstalledVersion != -1 {
		t.Fatalf("legacy pi status = %+v", pi)
	}
	if pi.ExpectedVersion != piIntegrationVersion {
		t.Fatalf("expected version %d, got %d", piIntegrationVersion, pi.ExpectedVersion)
	}

	notice, ok := OutdatedUpdateNotice()
	if !ok {
		t.Fatal("expected outdated notice")
	}
	if !strings.Contains(notice, "installed cats integrations need updating; run catctl integration install pi.") {
		t.Fatalf("notice = %q", notice)
	}

	// A current marker flips the state and silences the notice.
	mustWriteFile(t, piPath, "// CATS_INTEGRATION_VERSION=2\nexport {}\n")
	for _, status := range InstalledIntegrationStatuses() {
		if status.Target == TargetPi && status.State != StatusCurrent {
			t.Fatalf("current pi status = %+v", status)
		}
	}
	if _, ok := OutdatedUpdateNotice(); ok {
		t.Fatal("expected no notice when current")
	}
}

func TestRecommendationLabelsAndNeedsInstall(t *testing.T) {
	cases := []struct {
		available    bool
		state        StatusKind
		label        string
		needsInstall bool
	}{
		{true, StatusCurrent, "installed", false},
		{false, StatusCurrent, "installed", false},
		{true, StatusOutdated, "update available", true},
		{false, StatusOutdated, "update available", true},
		{true, StatusNotInstalled, "available", true},
		{false, StatusNotInstalled, "not found", false},
	}
	for _, tc := range cases {
		rec := Recommendation{Available: tc.available, State: tc.state}
		if rec.StatusLabel() != tc.label {
			t.Errorf("StatusLabel(%v, %v) = %q, want %q", tc.available, tc.state, rec.StatusLabel(), tc.label)
		}
		if rec.NeedsInstall() != tc.needsInstall {
			t.Errorf("NeedsInstall(%v, %v) = %v, want %v", tc.available, tc.state, rec.NeedsInstall(), tc.needsInstall)
		}
	}
}

func TestPaneEnv(t *testing.T) {
	got := PaneEnv("/tmp/h.sock", 7, "")
	want := []string{"CATS_SOCKET_PATH=/tmp/h.sock", "CATS_PANE_ID=p_7", "CATS_ENV=1"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("PaneEnv = %v, want %v", got, want)
		}
	}
	got = PaneEnv("/tmp/h.sock", 7, "p_pub")
	if got[1] != "CATS_PANE_ID=p_pub" {
		t.Fatalf("public id not honored: %v", got)
	}
}
