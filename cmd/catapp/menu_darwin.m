//go:build darwin

// Native macOS menu bar for catapp. A plain webview app is bundled without a
// menu, which means Cmd-Q cannot quit and the standard Cmd-Z/X/C/V/A editing
// shortcuts (Cocoa wires these through Edit-menu items to the first responder)
// do not function. installAppMenu builds a minimal App + Edit menu and installs
// it on the shared application.
//
// The thin client adds a Connect menu listing its saved catways; see
// installConnectMenu on the Go side for why the whole bar is rebuilt to move a
// checkmark.
//
// Quit is special: it must reap the supervised catway/cathost daemons before
// the process dies. Rather than target NSApp's terminate: directly, the Quit
// item targets a small helper whose action first calls the Go-exported
// catappCleanup (idempotent) and only then terminates.

#import <Cocoa/Cocoa.h>
#include "_cgo_export.h"

// CatsMenuTarget owns the Quit action. A single instance is kept alive in a
// static (NSMenuItem.target is a weak/unretained reference, so the target must
// outlive the menu on its own).
@interface CatsMenuTarget : NSObject
- (void)quit:(id)sender;
- (void)zoomIn:(id)sender;
- (void)zoomOut:(id)sender;
- (void)zoomActual:(id)sender;
- (void)connect:(id)sender;
@end

@implementation CatsMenuTarget
- (void)quit:(id)sender {
    catappCleanup();        // reap daemons (no-op in remote mode; runs once)
    [NSApp terminate:sender]; // safe now — nothing left to orphan
}
// Zoom routes through Go (catappZoom → webview Eval → the page's
// window.catsAdjustFont): WKWebView never sees ⌘+/⌘-/⌘0 as keydowns because
// Cocoa resolves them as menu key equivalents first.
- (void)zoomIn:(id)sender     { catappZoom(1); }
- (void)zoomOut:(id)sender    { catappZoom(-1); }
- (void)zoomActual:(id)sender { catappZoom(0); }
// Connect items carry their index in the saved-catway list as the item tag;
// "Connect to Another…" carries -1. The list itself lives in Go, so the tag is
// the whole message.
- (void)connect:(id)sender { catappConnectPreset((int)[(NSMenuItem *)sender tag]); }
@end

static CatsMenuTarget *gMenuTarget = nil;

// The app delegate, which owns New Window. Defined in window_darwin.m; nil in
// the single-window paths (the error sheet), which is what windowMenu == 0
// already says.
extern id catsWindowMenuTarget(void);

void installAppMenu(const char *cAppName, const char **hostNames, int hostCount,
                    int currentHost, int windowMenu) {
    @autoreleasepool {
        if (!gMenuTarget) {
            gMenuTarget = [[CatsMenuTarget alloc] init];
        }
        NSString *appName = [NSString stringWithUTF8String:cAppName];
        NSMenu *mainMenu = [[NSMenu alloc] init];

        // --- Application menu -------------------------------------------------
        // The first submenu is treated as the app menu; its own title is ignored
        // (the running app's name is shown instead).
        NSMenuItem *appItem = [[NSMenuItem alloc] init];
        [mainMenu addItem:appItem];
        NSMenu *appMenu = [[NSMenu alloc] init];
        [appItem setSubmenu:appMenu];

        [appMenu addItemWithTitle:[@"About " stringByAppendingString:appName]
                           action:@selector(orderFrontStandardAboutPanel:)
                    keyEquivalent:@""];
        [appMenu addItem:[NSMenuItem separatorItem]];
        [appMenu addItemWithTitle:[@"Hide " stringByAppendingString:appName]
                           action:@selector(hide:)
                    keyEquivalent:@"h"];
        NSMenuItem *hideOthers =
            [appMenu addItemWithTitle:@"Hide Others"
                               action:@selector(hideOtherApplications:)
                        keyEquivalent:@"h"];
        [hideOthers setKeyEquivalentModifierMask:(NSEventModifierFlagOption |
                                                  NSEventModifierFlagCommand)];
        [appMenu addItemWithTitle:@"Show All"
                           action:@selector(unhideAllApplications:)
                    keyEquivalent:@""];
        [appMenu addItem:[NSMenuItem separatorItem]];
        NSMenuItem *quitItem =
            [appMenu addItemWithTitle:[@"Quit " stringByAppendingString:appName]
                               action:@selector(quit:)
                        keyEquivalent:@"q"];
        [quitItem setTarget:gMenuTarget]; // route Quit through our cleanup

        // --- Edit menu --------------------------------------------------------
        // nil targets fall through the responder chain to the WKWebView, which
        // implements cut:/copy:/paste:/selectAll:/undo:/redo:.
        NSMenuItem *editItem = [[NSMenuItem alloc] init];
        [mainMenu addItem:editItem];
        NSMenu *editMenu = [[NSMenu alloc] initWithTitle:@"Edit"];
        [editItem setSubmenu:editMenu];

        [editMenu addItemWithTitle:@"Undo" action:@selector(undo:) keyEquivalent:@"z"];
        NSMenuItem *redo = [editMenu addItemWithTitle:@"Redo"
                                               action:@selector(redo:)
                                        keyEquivalent:@"z"];
        [redo setKeyEquivalentModifierMask:(NSEventModifierFlagShift |
                                            NSEventModifierFlagCommand)];
        [editMenu addItem:[NSMenuItem separatorItem]];
        [editMenu addItemWithTitle:@"Cut" action:@selector(cut:) keyEquivalent:@"x"];
        [editMenu addItemWithTitle:@"Copy" action:@selector(copy:) keyEquivalent:@"c"];
        [editMenu addItemWithTitle:@"Paste" action:@selector(paste:) keyEquivalent:@"v"];
        [editMenu addItemWithTitle:@"Select All"
                            action:@selector(selectAll:)
                     keyEquivalent:@"a"];

        // --- View menu --------------------------------------------------------
        // Font sizing for the terminal. These must be menu items: Cocoa consumes
        // ⌘+/⌘-/⌘0 as key equivalents before the WKWebView's page ever gets a
        // keydown, so the in-page shortcut handling cannot work in the app.
        NSMenuItem *viewItem = [[NSMenuItem alloc] init];
        [mainMenu addItem:viewItem];
        NSMenu *viewMenu = [[NSMenu alloc] initWithTitle:@"View"];
        [viewItem setSubmenu:viewMenu];

        NSMenuItem *zoomIn =
            [viewMenu addItemWithTitle:@"Bigger Text"
                                action:@selector(zoomIn:)
                         keyEquivalent:@"+"];
        [zoomIn setTarget:gMenuTarget];
        // Hidden twin so the unshifted ⌘= works too (the visible item's "+"
        // only matches the shifted key on most layouts).
        NSMenuItem *zoomInAlt =
            [viewMenu addItemWithTitle:@"Bigger Text"
                                action:@selector(zoomIn:)
                         keyEquivalent:@"="];
        [zoomInAlt setTarget:gMenuTarget];
        [zoomInAlt setHidden:YES];
        [zoomInAlt setAllowsKeyEquivalentWhenHidden:YES];
        NSMenuItem *zoomOut =
            [viewMenu addItemWithTitle:@"Smaller Text"
                                action:@selector(zoomOut:)
                         keyEquivalent:@"-"];
        [zoomOut setTarget:gMenuTarget];
        NSMenuItem *zoomActual =
            [viewMenu addItemWithTitle:@"Default Text Size"
                                action:@selector(zoomActual:)
                         keyEquivalent:@"0"];
        [zoomActual setTarget:gMenuTarget];

        // --- Window menu ------------------------------------------------------
        // Only for the native multi-window shell (window_darwin.m). The single
        // webview_go paths — the startup error sheet — have exactly one window
        // and nothing to offer here; an inert "New Window" would be worse than
        // no menu.
        //
        // New Window opens on the PRIMARY view: the server resolves an omitted
        // workspace to whichever window the user touched last, which is what
        // "another window on what I am doing" means. Opening a window on a
        // specific project is the sidebar's job (its row's "open in new
        // window"), where the project is on screen to point at.
        if (windowMenu) {
            NSMenuItem *windowItem = [[NSMenuItem alloc] init];
            [mainMenu addItem:windowItem];
            NSMenu *windowMenuEl = [[NSMenu alloc] initWithTitle:@"Window"];
            [windowItem setSubmenu:windowMenuEl];

            NSMenuItem *newWin = [windowMenuEl addItemWithTitle:@"New Window"
                                                         action:@selector(newWindow:)
                                                  keyEquivalent:@"n"];
            [newWin setTarget:catsWindowMenuTarget()];
            [windowMenuEl addItem:[NSMenuItem separatorItem]];
            // nil targets: AppKit routes these through the responder chain to
            // the key window, which is the whole point of a Window menu.
            [windowMenuEl addItemWithTitle:@"Minimize"
                                    action:@selector(performMiniaturize:)
                             keyEquivalent:@"m"];
            [windowMenuEl addItemWithTitle:@"Zoom" action:@selector(performZoom:) keyEquivalent:@""];
            [windowMenuEl addItemWithTitle:@"Close Window"
                                    action:@selector(performClose:)
                             keyEquivalent:@"w"];
            [windowMenuEl addItem:[NSMenuItem separatorItem]];
            [windowMenuEl addItemWithTitle:@"Bring All to Front"
                                    action:@selector(arrangeInFront:)
                             keyEquivalent:@""];
            // Handing it to NSApp is what makes AppKit keep the list of open
            // windows at the foot of the menu current by itself.
            [NSApp setWindowsMenu:windowMenuEl];
        }

        // --- Connect menu -----------------------------------------------------
        // The thin client's saved catways. hostCount < 0 means this mode has
        // none to offer (the self-contained app runs its own catway), and the
        // menu is left out entirely rather than shown empty.
        //
        // The whole menu bar is rebuilt whenever the list or the current
        // connection changes — see installConnectMenu. That is also how the
        // checkmark moves: NSMenu has no cheaper way to keep a marker in step,
        // and rebuilding a menu of a handful of items costs nothing next to a
        // marker that says you are somewhere you are not.
        if (hostCount >= 0) {
            NSMenuItem *connectItem = [[NSMenuItem alloc] init];
            [mainMenu addItem:connectItem];
            NSMenu *connectMenu = [[NSMenu alloc] initWithTitle:@"Connect"];
            [connectItem setSubmenu:connectMenu];

            for (int i = 0; i < hostCount; i++) {
                NSString *title = [NSString stringWithUTF8String:hostNames[i]];
                // ⌘1..⌘9 for the first nine, which is as far as a number row
                // goes; the rest are click-only rather than sharing a shortcut.
                NSString *key = i < 9 ? [NSString stringWithFormat:@"%d", i + 1] : @"";
                NSMenuItem *it = [connectMenu addItemWithTitle:title
                                                       action:@selector(connect:)
                                                keyEquivalent:key];
                [it setTarget:gMenuTarget];
                [it setTag:i];
                [it setState:(i == currentHost) ? NSControlStateValueOn
                                                : NSControlStateValueOff];
            }
            if (hostCount > 0) {
                [connectMenu addItem:[NSMenuItem separatorItem]];
            }
            NSMenuItem *other = [connectMenu addItemWithTitle:@"Connect to Another…"
                                                      action:@selector(connect:)
                                               keyEquivalent:@"k"];
            [other setTarget:gMenuTarget];
            [other setTag:-1];
        }

        [NSApp setMainMenu:mainMenu];
    }
}
