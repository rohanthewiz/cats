//go:build darwin

package main

/*
#cgo darwin LDFLAGS: -framework Cocoa
#include <stdlib.h>

// Defined in menu_darwin.m — builds and installs the native macOS menu bar.
// hostCount < 0 omits the Connect menu entirely (local mode has nothing to
// connect to); 0 keeps the menu with only its "Connect to Another…" item.
void installAppMenu(const char *appName, const char **hostNames, int hostCount, int currentHost);
*/
import "C"

import "unsafe"

// catappCleanup is called from the Objective-C Quit menu action (menu_darwin.m)
// before the process terminates, so a Cmd-Q reaps the supervised daemons instead
// of orphaning them. It runs on the main thread and is idempotent (runCleanup is
// guarded by sync.Once), so it composes safely with the window-close and signal
// teardown paths.
//
//export catappCleanup
func catappCleanup() {
	runCleanup()
}

// catappZoom is called from the View menu's zoom actions (menu_darwin.m).
// delta is +1/-1 to step the terminal font size, 0 to reset it. The menu owns
// these shortcuts because Cocoa resolves ⌘+/⌘-/⌘0 as key equivalents before
// the WKWebView's page sees a keydown, so the page's own handler never fires
// in the app.
//
//export catappZoom
func catappZoom(delta C.int) {
	zoomFont(int(delta))
}

// catappConnectPreset is called from the Connect menu (menu_darwin.m). index is
// a position in the saved catway list, or -1 for "Connect to Another…".
//
//export catappConnectPreset
func catappConnectPreset(index C.int) {
	selectPreset(int(index))
}

// installMenu installs the native menu bar on the shared NSApplication. webview
// creates a bundled app with no menu of its own, so without this Cmd-Q cannot
// quit and the standard Cmd-C/V/X/A editing shortcuts — routed in Cocoa through
// Edit-menu items to the first responder (the WKWebView) — do not work. Must be
// called on the main thread, after webview.New (which creates NSApplication) and
// before Run().
//
// No Connect menu: this is the menu every mode gets, installed before anyone
// knows whether there are catways to list. The thin client re-installs with its
// list once it does (installConnectMenu), which is cheap — the whole menu is
// rebuilt and handed to NSApp, which is also how the checkmark moves.
func installMenu() { installConnectMenu(nil, -1) }

// installConnectMenu is installMenu with the thin client's catway list: one item
// per preset, a checkmark on the one in the window, and "Connect to Another…"
// underneath. current is an index into names, -1 for none.
//
// Passing nil names with current -1 means "local mode": no Connect menu at all,
// rather than an empty one. A menu offering nothing is a worse answer than no
// menu, and the self-contained app has nothing to offer — its catway is the one
// it started itself.
func installConnectMenu(names []string, current int) {
	name := C.CString(appMenuName())
	defer C.free(unsafe.Pointer(name))

	count := C.int(-1)
	var arr **C.char
	if names != nil {
		count = C.int(len(names))
		// A C array of the strings, freed on the way out: installAppMenu copies
		// each one into an NSString before it returns.
		cs := make([]*C.char, len(names))
		for i, n := range names {
			cs[i] = C.CString(n)
		}
		defer func() {
			for _, p := range cs {
				C.free(unsafe.Pointer(p))
			}
		}()
		if len(cs) > 0 {
			arr = (**C.char)(unsafe.Pointer(&cs[0]))
		}
	}
	C.installAppMenu(name, arr, count, C.int(current))
}

// appMenuName is the name shown in the app menu and its About/Hide/Quit items.
func appMenuName() string { return "Cats" }
