//go:build darwin

package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// clearLocale leaves the process with no locale variables at all, which is how
// launchd starts Cats.app. t.Setenv first so the test restores them afterwards.
func clearLocale(t *testing.T) {
	t.Helper()
	for _, k := range []string{"LANG", "LC_ALL", "LC_CTYPE"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
}

// childCharmap asks a child the way a pane is spawned: exec with no cmd.Env, so
// it inherits this process's environment just as cathost's buildEnv passes
// os.Environ() to every pane.
func childCharmap(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("/usr/bin/locale", "charmap").Output()
	if err != nil {
		t.Fatalf("locale charmap: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// The bug: with no locale, every child runs with an ASCII charmap. After
// hydration a child must get UTF-8, which is what a pane inherits.
func TestHydrateLocaleGivesChildrenUTF8(t *testing.T) {
	clearLocale(t)
	if got := childCharmap(t); got == "UTF-8" {
		t.Fatalf("child charmap is already UTF-8 with no locale set; the test cannot see the bug")
	}

	hydrateLocale()

	lang := os.Getenv("LANG")
	if !strings.HasSuffix(lang, ".UTF-8") {
		t.Fatalf("LANG = %q after hydration, want a UTF-8 locale", lang)
	}
	if !localeUsable(lang) {
		t.Fatalf("LANG = %q, which libc cannot load", lang)
	}
	if got := childCharmap(t); got != "UTF-8" {
		t.Fatalf("child charmap = %q after hydration (LANG=%q), want UTF-8", got, lang)
	}
}

// An inherited LANG is the user's choice and is kept, even a non-UTF-8 one.
func TestHydrateLocaleKeepsInheritedLANG(t *testing.T) {
	clearLocale(t)
	t.Setenv("LANG", "fr_FR.ISO8859-1")
	hydrateLocale()
	if got := os.Getenv("LANG"); got != "fr_FR.ISO8859-1" {
		t.Fatalf("LANG = %q, want the inherited value untouched", got)
	}
}

// LC_ALL and LC_CTYPE outrank LANG, so hydration leaves them alone. The only
// change is a LANG that fills the gap underneath them.
func TestHydrateLocaleLeavesLCVariables(t *testing.T) {
	clearLocale(t)
	t.Setenv("LC_ALL", "C")
	hydrateLocale()
	if got := os.Getenv("LC_ALL"); got != "C" {
		t.Fatalf("LC_ALL = %q, want it untouched", got)
	}
	if _, set := os.LookupEnv("LC_CTYPE"); set {
		t.Fatalf("LC_CTYPE was set, want it left unset")
	}
}

func TestPickLANG(t *testing.T) {
	has := func(names ...string) func(string) bool {
		return func(name string) bool {
			for _, n := range names {
				if n == name {
					return true
				}
			}
			return false
		}
	}
	cases := []struct {
		name, lang, region string
		usable             func(string) bool
		want               string
	}{
		{"system locale libc has", "en", "GB", has("en_GB.UTF-8"), "en_GB.UTF-8"},
		{"non-English system locale", "de", "DE", has("de_DE.UTF-8"), "de_DE.UTF-8"},
		{"language and region libc lacks", "en", "DE", has(), fallbackLANG},
		{"numeric UN region", "en", "150", has(), fallbackLANG},
		{"no region", "en", "", has("en_.UTF-8"), fallbackLANG},
		{"nothing at all", "", "", has(), fallbackLANG},
	}
	for _, c := range cases {
		if got := pickLANG(c.lang, c.region, c.usable); got != c.want {
			t.Errorf("%s: pickLANG(%q, %q) = %q, want %q", c.name, c.lang, c.region, got, c.want)
		}
	}
}

// localeUsable has to tell the locales libc ships from the names pickLANG can
// build that it does not. A LANG of the second kind is ignored by every
// program, which would leave panes in the C locale.
func TestLocaleUsable(t *testing.T) {
	for name, want := range map[string]bool{
		fallbackLANG:   true,
		"en_150.UTF-8": false,
		"xx_YY.UTF-8":  false,
	} {
		if got := localeUsable(name); got != want {
			t.Errorf("localeUsable(%q) = %v, want %v", name, got, want)
		}
	}
}

// The system locale comes back as two codes pickLANG can join. This machine's
// actual setting is not asserted, only its shape.
func TestSystemLocaleShape(t *testing.T) {
	lang, region := systemLocale()
	t.Logf("system locale: lang=%q region=%q → %s", lang, region, pickLANG(lang, region, localeUsable))
	if strings.ContainsAny(lang+region, "_.@") {
		t.Fatalf("systemLocale() = %q, %q; want bare codes", lang, region)
	}
}
