//go:build darwin

package main

/*
#cgo darwin LDFLAGS: -framework Cocoa
#include <stdlib.h>

// Defined in menu_darwin.m — builds and installs the native macOS menu bar.
void installAppMenu(const char *appName);
*/
import "C"

import "unsafe"

// herdrappCleanup is called from the Objective-C Quit menu action (menu_darwin.m)
// before the process terminates, so a Cmd-Q reaps the supervised daemons instead
// of orphaning them. It runs on the main thread and is idempotent (runCleanup is
// guarded by sync.Once), so it composes safely with the window-close and signal
// teardown paths.
//
//export herdrappCleanup
func herdrappCleanup() {
	runCleanup()
}

// installMenu installs the native menu bar on the shared NSApplication. webview
// creates a bundled app with no menu of its own, so without this Cmd-Q cannot
// quit and the standard Cmd-C/V/X/A editing shortcuts — routed in Cocoa through
// Edit-menu items to the first responder (the WKWebView) — do not work. Must be
// called on the main thread, after webview.New (which creates NSApplication) and
// before Run().
func installMenu() {
	name := C.CString(appMenuName())
	defer C.free(unsafe.Pointer(name))
	C.installAppMenu(name)
}

// appMenuName is the name shown in the app menu and its About/Hide/Quit items.
func appMenuName() string { return "Herdr" }
