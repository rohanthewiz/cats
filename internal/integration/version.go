package integration

// Minimum-agent-version gates. Only kimi has one today: its hook schema
// stabilized in kimi code 0.14.0, so installing against an older binary would
// wedge the agent. An unrunnable/unparsable `kimi --version` degrades to a
// warning (install proceeds); a confirmed-older version is a hard error.

import (
	"fmt"
	"os/exec"
	"strings"
)

type agentVersionRequirement struct {
	label      string
	binary     string
	args       []string
	minVersion string
}

// versionRequirementFor returns the gate for a target, or nil.
func versionRequirementFor(target Target) *agentVersionRequirement {
	if target == TargetKimi {
		return &agentVersionRequirement{
			label:      "kimi code",
			binary:     "kimi",
			args:       []string{"--version"},
			minVersion: kimiMinVersion,
		}
	}
	return nil
}

type versionTriple struct {
	major, minor, patch uint64
}

func (v versionTriple) less(other versionTriple) bool {
	if v.major != other.major {
		return v.major < other.major
	}
	if v.minor != other.minor {
		return v.minor < other.minor
	}
	return v.patch < other.patch
}

// extractVersionTriple finds the first whitespace-separated token that parses
// as `[v]major.minor[.patch...]`; a missing or non-numeric patch is 0 (so
// "0.14" and "0.14.1-beta.2" both parse).
func extractVersionTriple(text string) (versionTriple, bool) {
	for _, token := range strings.Fields(text) {
		token = strings.TrimLeft(token, "v")
		parts := strings.SplitN(token, ".", 3)
		if len(parts) < 2 {
			continue
		}
		major, ok := parseVersionComponent(parts[0])
		if !ok {
			continue
		}
		minor, ok := parseVersionComponent(parts[1])
		if !ok {
			continue
		}
		var patch uint64
		if len(parts) == 3 {
			digits := leadingDigits(parts[2])
			if p, ok := parseVersionComponent(digits); ok {
				patch = p
			}
		}
		return versionTriple{major, minor, patch}, true
	}
	return versionTriple{}, false
}

func parseVersionComponent(s string) (uint64, bool) {
	if s == "" {
		return 0, false
	}
	var n uint64
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return 0, false
		}
		n = n*10 + uint64(ch-'0')
	}
	return n, true
}

func leadingDigits(s string) string {
	for i, ch := range s {
		if ch < '0' || ch > '9' {
			return s[:i]
		}
	}
	return s
}

// enforceAgentVersion returns "" when the installed agent satisfies the
// requirement, a warning line when the version cannot be determined (install
// proceeds), and an error when the installed agent is too old.
func enforceAgentVersion(requirement *agentVersionRequirement) (string, error) {
	probe := requirement.binary + " " + strings.Join(requirement.args, " ")
	output, err := exec.Command(requirement.binary, requirement.args...).Output()
	if err != nil {
		return fmt.Sprintf(
			"%s could not run `%s` to verify the installed version; hooks require %s %s or newer",
			InstallWarningPrefix, probe, requirement.label, requirement.minVersion), nil
	}

	found, ok := extractVersionTriple(string(output))
	if !ok {
		return fmt.Sprintf(
			"%s could not parse the %s version from `%s` output; hooks require %s %s or newer",
			InstallWarningPrefix, requirement.label, probe, requirement.label, requirement.minVersion), nil
	}
	required, ok := extractVersionTriple(requirement.minVersion)
	if !ok {
		return "", fmt.Errorf("static min version %q must be a valid version triple", requirement.minVersion)
	}

	if found.less(required) {
		return "", fmt.Errorf(
			"%s %d.%d.%d is too old: herdr hooks require %s %s or newer. upgrade %s, then re-run install",
			requirement.label, found.major, found.minor, found.patch,
			requirement.label, requirement.minVersion, requirement.label)
	}
	return "", nil
}
