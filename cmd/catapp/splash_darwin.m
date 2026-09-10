//go:build darwin

// The startup window: a small always-there-first window that shows the boot
// log (bootlog.go) while the launcher brings everything up.
//
// It is deliberately NOT a CatsWindowController. Those windows are the app —
// they are held in gWindows, they are what the restore list is made of, and
// closing the last of them quits. The splash is a temporary surface with no
// workspace behind it; putting it in gWindows would save it into app.json as a
// window to reopen next launch and would make its frame part of the layout.
// So it is its own tiny class, held in its own global, invisible to everything
// else in the shell.
//
//	catsOpenSplash(html)  ──► window + WKWebView, loads the page
//	catsSplashPush(js)    ──► evaluateJavaScript, or buffered until the page
//	                          has loaded (the log starts before the page does)
//	catsCloseSplash()     ──► closes and forgets it
//
// Buffering is not optional: the first steps are recorded before AppKit has
// even drawn, and evaluateJavaScript against a web view whose document has not
// arrived does nothing. Only the LATEST push is kept, because every push is the
// whole log — an older one has nothing in it the newer one lacks.

#import <Cocoa/Cocoa.h>
#import <WebKit/WebKit.h>
#include <stdlib.h>
#include "_cgo_export.h"

// Big enough for a dozen steps and a few lines of daemon output without
// scrolling, small enough to read as a progress window rather than as the app.
static const CGFloat kSplashW = 580;
static const CGFloat kSplashH = 440;

@interface CatsSplashController : NSObject <NSWindowDelegate, WKNavigationDelegate>
@property(nonatomic, strong) NSWindow *window;
@property(nonatomic, strong) WKWebView *web;
@property(nonatomic, assign) BOOL ready;    // the page has loaded; JS will run
@property(nonatomic, assign) BOOL closing;  // guards re-entry from windowWillClose
@property(nonatomic, strong) NSString *pending; // latest push, if it arrived early
@end

// gSplash is the one startup window, or nil once it has gone. Owned for as long
// as it exists (see the ownership note in window_darwin.m — this file is
// compiled without ARC too).
static CatsSplashController *gSplash = nil;

@implementation CatsSplashController

- (instancetype)initWithHTML:(NSString *)html title:(NSString *)title {
    self = [super init];
    if (!self) {
        return nil;
    }
    NSRect frame = NSMakeRect(0, 0, kSplashW, kSplashH);
    NSUInteger style = NSWindowStyleMaskTitled | NSWindowStyleMaskClosable |
                       NSWindowStyleMaskMiniaturizable | NSWindowStyleMaskResizable;
    // autorelease + a strong property is the balanced MRR pair: the property's
    // retain is what keeps these alive, and the enclosing pool takes the
    // allocation's own +1 when the C entry point returns.
    NSWindow *win = [[[NSWindow alloc] initWithContentRect:frame
                                                 styleMask:style
                                                   backing:NSBackingStoreBuffered
                                                     defer:NO] autorelease];
    win.title = title;
    win.delegate = self;
    win.releasedWhenClosed = NO; // this controller owns the lifetime
    [win center];

    WKWebViewConfiguration *cfg = [[[WKWebViewConfiguration alloc] init] autorelease];
    WKWebView *web = [[[WKWebView alloc] initWithFrame:frame configuration:cfg] autorelease];
    web.autoresizingMask = NSViewWidthSizable | NSViewHeightSizable;
    web.navigationDelegate = self;
    win.contentView = web;

    self.window = win;
    self.web = web;
    [web loadHTMLString:html baseURL:nil];
    return self;
}

// push runs one statement in the page, or holds it until the page exists.
- (void)push:(NSString *)js {
    if (!self.ready) {
        self.pending = js; // every push carries the whole log; keep only the last
        return;
    }
    [self.web evaluateJavaScript:js completionHandler:nil];
}

- (void)webView:(WKWebView *)webView didFinishNavigation:(WKNavigation *)navigation {
    self.ready = YES;
    if (self.pending) {
        [self.web evaluateJavaScript:self.pending completionHandler:nil];
        self.pending = nil;
    }
}

// The user closing the window is a decision ("stop showing me this"), not a
// failure: Go is told so it stops pushing, and startup carries on. Note that if
// this is the only window open — a startup that is still working, or one that
// failed — AppKit will then terminate the app, which is the intended way out of
// a launch that is going nowhere. Termination runs catappCleanup, so the
// daemons are reaped on the way.
- (void)windowWillClose:(NSNotification *)note {
    self.web.navigationDelegate = nil;
    self.window.delegate = nil;
    if (!self.closing) { // a programmatic close has already told Go
        catappSplashClosed();
    }
}

- (void)dealloc {
    [_window release];
    [_web release];
    [_pending release];
    [super dealloc];
}

@end

// --- C entry points (called from splash_darwin.go) ------------------------------
//
// All of these are main-thread only, like every other AppKit call in the app;
// the Go side hops through onMainThread for the ones that come off a goroutine.

// catsOpenSplash shows the startup window. Calling it twice is a no-op — one
// launch, one window.
void catsOpenSplash(const char *cHTML, const char *cTitle) {
    @autoreleasepool {
        if (gSplash) {
            return;
        }
        NSString *html = cHTML ? [NSString stringWithUTF8String:cHTML] : @"";
        NSString *title = cTitle ? [NSString stringWithUTF8String:cTitle] : @"Starting";
        gSplash = [[CatsSplashController alloc] initWithHTML:html title:title];
        [gSplash.window makeKeyAndOrderFront:nil];
    }
}

// catsSplashPush hands one JavaScript statement to the page (a
// window.catsBootPush(...) call built in Go).
void catsSplashPush(const char *cJS) {
    @autoreleasepool {
        if (!gSplash || !cJS) {
            return;
        }
        [gSplash push:[NSString stringWithUTF8String:cJS]];
    }
}

// catsSplashRaise brings the startup window back in front. The first workspace
// window opens on top of it, and while startup is still running the log is the
// thing worth looking at — but only ordered front, never made key, so it cannot
// steal the keystrokes someone is already typing into a pane.
void catsSplashRaise(void) {
    @autoreleasepool {
        if (gSplash) {
            [gSplash.window orderFront:nil];
        }
    }
}

// catsCloseSplash closes the window and forgets it. Safe to call when there is
// no splash, and safe to call twice.
void catsCloseSplash(void) {
    @autoreleasepool {
        if (!gSplash) {
            return;
        }
        CatsSplashController *sp = gSplash;
        gSplash = nil; // before the close, so a re-entrant call finds nothing
        sp.closing = YES;
        [sp.window close];
        [sp release];
    }
}
