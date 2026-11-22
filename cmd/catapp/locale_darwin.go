//go:build darwin

package main

/*
#cgo darwin LDFLAGS: -framework CoreFoundation
#include <stdlib.h>
#include <xlocale.h>
#include <CoreFoundation/CoreFoundation.h>

// catappSystemLocale writes the language and region from System Settings ›
// General › Language & Region ("en" and "GB", say) into lang and region. Either
// can come back empty: a language-only locale has no region, and a value that
// does not fit is dropped rather than truncated.
static void catappSystemLocale(char *lang, char *region, int n) {
	lang[0] = 0;
	region[0] = 0;
	CFLocaleRef loc = CFLocaleCopyCurrent();
	if (loc == NULL) {
		return;
	}
	CFStringRef l = (CFStringRef)CFLocaleGetValue(loc, kCFLocaleLanguageCode);
	CFStringRef r = (CFStringRef)CFLocaleGetValue(loc, kCFLocaleCountryCode);
	if (l == NULL || !CFStringGetCString(l, lang, n, kCFStringEncodingUTF8)) {
		lang[0] = 0;
	}
	if (r == NULL || !CFStringGetCString(r, region, n, kCFStringEncodingUTF8)) {
		region[0] = 0;
	}
	CFRelease(loc);
}

// catappLocaleUsable reports whether libc has every category of the named
// locale. newlocale builds a standalone locale object, so asking does not
// change this process's own locale.
static int catappLocaleUsable(const char *name) {
	locale_t l = newlocale(LC_ALL_MASK, name, NULL);
	if (l == NULL) {
		return 0;
	}
	freelocale(l);
	return 1;
}
*/
import "C"

import (
	"log"
	"os"
	"unsafe"
)

// A double-clicked .app gets no LANG from launchd, and every process we spawn
// inherits that: cathost, catway, and through cathost every pane. A pane with
// no locale runs in the C locale, so any tool in it that transcodes through the
// locale gets ASCII or MacRoman. pbpaste was one (N-070): it sent MacRoman
// bytes, and the paste bridge rejected them. The shell itself is another: zsh
// counts each byte of a UTF-8 character as a character of its own, so line
// editing over an "é" moves and deletes by halves. Measured on macOS:
//
//	                      no locale        LANG=en_US.UTF-8
//	locale charmap        US-ASCII         UTF-8
//	ls of café“x”         caf?????x???     café“x”
//	zsh: x=é; ${#x}       2                1
//
// Terminal.app, iTerm2 and Ghostty all close this gap the same way. When LANG
// is unset, they set it from the system's Language & Region as a UTF-8 locale,
// before the shell starts. hydrateLocale does the same for the whole process
// tree, so a pane and a terminal agree.

// fallbackLANG is used when the system locale has no UTF-8 form in libc. An
// English-language Mac set to a region libc has no locale for (en_DE, en_150)
// is the usual case. Ghostty falls back to the same value.
const fallbackLANG = "en_US.UTF-8"

// hydrateLocale sets LANG from the system locale when it is unset or empty.
// An inherited LANG is kept as it is, even a non-UTF-8 one, because someone
// chose it. So are LC_ALL and LC_CTYPE, which outrank LANG and are never
// touched here.
//
// It is not gated on a GUI launch the way hydratePATH is, because nothing here
// is slow or surprising. A launch from a terminal almost always brings LANG
// with it, and one that does not wants the fix as much as a Finder launch does.
func hydrateLocale() {
	if os.Getenv("LANG") != "" {
		return
	}
	lang, region := systemLocale()
	name := pickLANG(lang, region, localeUsable)
	if err := os.Setenv("LANG", name); err != nil {
		log.Printf("could not set LANG=%s: %v", name, err)
		boot.note("locale", "LANG is unset and could not be set: "+err.Error())
		return
	}
	detail := "LANG was unset; set " + name + " from Language & Region"
	if name == fallbackLANG && lang+"_"+region+".UTF-8" != fallbackLANG {
		detail = "LANG was unset; Language & Region gave " + describeLocale(lang, region) +
			", which has no UTF-8 locale, so set " + name
	}
	boot.note("locale", detail)
}

// pickLANG returns <lang>_<region>.UTF-8 when usable says libc has it, and
// fallbackLANG otherwise. It is split from the cgo calls so tests can supply
// locales this machine is not set to.
func pickLANG(lang, region string, usable func(string) bool) string {
	if lang != "" && region != "" {
		if name := lang + "_" + region + ".UTF-8"; usable(name) {
			return name
		}
	}
	return fallbackLANG
}

// describeLocale renders the system locale for the boot log, naming a missing
// half instead of leaving a dangling underscore.
func describeLocale(lang, region string) string {
	switch {
	case lang == "" && region == "":
		return "no locale"
	case region == "":
		return lang + " with no region"
	case lang == "":
		return "region " + region + " with no language"
	}
	return lang + "_" + region
}

// systemLocale reads the language and region codes from CoreFoundation's
// current locale.
func systemLocale() (lang, region string) {
	const n = 64
	var l, r [n]C.char
	C.catappSystemLocale(&l[0], &r[0], n)
	return C.GoString(&l[0]), C.GoString(&r[0])
}

// localeUsable reports whether libc can load the named locale in full. A LANG
// that libc cannot load is ignored silently by every program that reads it, so
// setting one would leave the panes exactly where they started.
func localeUsable(name string) bool {
	cs := C.CString(name)
	defer C.free(unsafe.Pointer(cs))
	return C.catappLocaleUsable(cs) != 0
}
