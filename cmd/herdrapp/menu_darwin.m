//go:build darwin

// Native macOS menu bar for herdrapp. A plain webview app is bundled without a
// menu, which means Cmd-Q cannot quit and the standard Cmd-Z/X/C/V/A editing
// shortcuts (Cocoa wires these through Edit-menu items to the first responder)
// do not function. installAppMenu builds a minimal App + Edit menu and installs
// it on the shared application.
//
// Quit is special: it must reap the supervised gateway/termhost daemons before
// the process dies. Rather than target NSApp's terminate: directly, the Quit
// item targets a small helper whose action first calls the Go-exported
// herdrappCleanup (idempotent) and only then terminates.

#import <Cocoa/Cocoa.h>
#include "_cgo_export.h"

// HerdrMenuTarget owns the Quit action. A single instance is kept alive in a
// static (NSMenuItem.target is a weak/unretained reference, so the target must
// outlive the menu on its own).
@interface HerdrMenuTarget : NSObject
- (void)quit:(id)sender;
@end

@implementation HerdrMenuTarget
- (void)quit:(id)sender {
    herdrappCleanup();        // reap daemons (no-op in remote mode; runs once)
    [NSApp terminate:sender]; // safe now — nothing left to orphan
}
@end

static HerdrMenuTarget *gMenuTarget = nil;

void installAppMenu(const char *cAppName) {
    @autoreleasepool {
        if (!gMenuTarget) {
            gMenuTarget = [[HerdrMenuTarget alloc] init];
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

        [NSApp setMainMenu:mainMenu];
    }
}
