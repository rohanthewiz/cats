//go:build darwin

// Native macOS menu bar for catapp. A plain webview app is bundled without a
// menu, which means Cmd-Q cannot quit and the standard Cmd-Z/X/C/V/A editing
// shortcuts (Cocoa wires these through Edit-menu items to the first responder)
// do not function. installAppMenu builds a minimal App + Edit menu and installs
// it on the shared application.
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
@end

static CatsMenuTarget *gMenuTarget = nil;

void installAppMenu(const char *cAppName) {
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

        [NSApp setMainMenu:mainMenu];
    }
}
