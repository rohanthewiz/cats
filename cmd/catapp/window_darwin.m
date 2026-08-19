//go:build darwin

// Native multi-window shell for catapp.
//
// The app used to be one webview_go window, and webview_go is one window per
// process by construction: its cocoa backend makes each instance NSApp's
// delegate, owns applicationShouldTerminateAfterLastWindowClosed:, and Run() is
// [NSApp run] — a second webview.New in the same process replaces the first's
// delegate. That is fine for a single window and impossible for several.
//
// So the windows are built here instead: one NSWindow + WKWebView each, all
// sharing the default website data store so the hsess cookie a thin client
// holds per host is one cookie jar across windows (as it already is across
// navigations). The server does the rest — each connection is a view
// on one workspace (?ws=<id>), so N windows over one session is a URL away.
//
//   ┌ NSApp ─────────────────────────────────────────────────────┐
//   │  CatsAppDelegate  (quit → catappCleanup; last window quits) │
//   │  ┌ CatsWindowController ─┐  ┌ CatsWindowController ─┐       │
//   │  │ WKWebView  ?ws=w1     │  │ WKWebView  ?ws=w2     │  …    │
//   │  └───────────────────────┘  └───────────────────────┘       │
//   │        shared WKWebsiteDataStore (one cookie jar)           │
//   └────────────────────────────────────────────────────────────┘
//
// Two bridges ride every window's configuration:
//
//   * catsClip — the native pasteboard (WKWebView cripples
//     navigator.clipboard). A reply-capable handler, so the page's
//     window.catsClipRead() is still a promise.
//   * catsApp — the connect form's three fire-and-forget callbacks, in remote
//     mode.
//
// window.open from the page (the sidebar's "open in new window") is intercepted
// in WKUIDelegate and becomes a native window, which is how the app gets the
// feature with no app-specific JS at all.

#import <Cocoa/Cocoa.h>
#import <WebKit/WebKit.h>
#include <stdlib.h>
#include <string.h>
#include "_cgo_export.h"

// Roomy default that still fits a laptop; every window after the first is
// cascaded off it by AppKit.
static const CGFloat kDefaultW = 1280;
static const CGFloat kDefaultH = 820;

@interface CatsWindowController : NSWindowController <NSWindowDelegate,
                                                      WKUIDelegate,
                                                      WKNavigationDelegate,
                                                      WKScriptMessageHandler,
                                                      WKScriptMessageHandlerWithReply>
@property(nonatomic, strong) WKWebView *web;
@end

// gWindows holds a strong reference to every open controller: an
// NSWindowController that nobody retains is deallocated the moment the last
// autorelease pool drains, taking its window with it.
static NSMutableArray<CatsWindowController *> *gWindows = nil;
static NSString *gAppName = @"Cats";
// gWindowTitle is what a freshly opened window is called. The thin client sets
// it to "Cats Mux — <host>" so a restored window says which catway it is on
// before its page has finished loading.
static NSString *gWindowTitle = @"Cats Mux";

// --- shared configuration ------------------------------------------------------

// catsUserScript defines the page-side half of both bridges. Injected at
// document start into every window, so a page reload (or a navigation to
// another catway) never loses them.
static NSString *const kBridgeJS =
    @"window.catsClipWrite = (t) => window.webkit.messageHandlers.catsClip.postMessage({op:'write',text:String(t)});\n"
    @"window.catsClipRead  = ()  => window.webkit.messageHandlers.catsClip.postMessage({op:'read'});\n"
    @"window.catsConnect = (u,l) => window.webkit.messageHandlers.catsApp.postMessage({op:'connect',url:String(u),label:String(l||'')});\n"
    @"window.catsForget  = (u)   => window.webkit.messageHandlers.catsApp.postMessage({op:'forget',url:String(u)});\n"
    @"window.catsCancel  = ()    => window.webkit.messageHandlers.catsApp.postMessage({op:'cancel'});\n";

static WKWebViewConfiguration *catsConfig(CatsWindowController *owner) {
    WKWebViewConfiguration *cfg = [[WKWebViewConfiguration alloc] init];
    // The DEFAULT data store, deliberately: it is persistent and shared by every
    // WKWebView in the process, which is what makes one login cookie serve every
    // window of a thin client. (WKProcessPool used to be the other half of that
    // sharing; since macOS 12 it is a deprecated no-op — every web view in a
    // process already shares one, so there is nothing left to set.)
    cfg.websiteDataStore = [WKWebsiteDataStore defaultDataStore];

    WKUserContentController *ucc = cfg.userContentController;
    [ucc addScriptMessageHandlerWithReply:owner
                             contentWorld:[WKContentWorld pageWorld]
                                     name:@"catsClip"];
    [ucc addScriptMessageHandler:owner name:@"catsApp"];
    [ucc addUserScript:[[WKUserScript alloc] initWithSource:kBridgeJS
                                              injectionTime:WKUserScriptInjectionTimeAtDocumentStart
                                           forMainFrameOnly:NO]];
    return cfg;
}

@implementation CatsWindowController

- (instancetype)initWithFrame:(NSRect)frame {
    NSUInteger style = NSWindowStyleMaskTitled | NSWindowStyleMaskClosable |
                       NSWindowStyleMaskMiniaturizable | NSWindowStyleMaskResizable;
    NSWindow *win = [[NSWindow alloc] initWithContentRect:frame
                                                styleMask:style
                                                  backing:NSBackingStoreBuffered
                                                    defer:NO];
    self = [super initWithWindow:win];
    if (!self) {
        return nil;
    }
    win.delegate = self;
    win.releasedWhenClosed = NO; // gWindows owns the lifetime, not AppKit
    win.title = gWindowTitle;
    // NOT setFrameAutosaveName: it keys by a fixed name, and N windows need N
    // frames whose names we choose — the restore list in app.json is that.
    self.web = [[WKWebView alloc] initWithFrame:win.contentView.bounds
                                  configuration:catsConfig(self)];
    self.web.autoresizingMask = NSViewWidthSizable | NSViewHeightSizable;
    self.web.UIDelegate = self;
    self.web.navigationDelegate = self;
    win.contentView = self.web;
    return self;
}

// --- window.open → a native window ---------------------------------------------
//
// The page's "open in new window" is a plain window.open(location.pathname +
// "?ws=" + id). Returning nil here would make it a blocked popup; opening a real
// window instead is how the sidebar action works in the app with zero
// app-specific JS. Returning nil AFTER loading the URL ourselves is the
// documented way to say "handled, don't make a second web view".
- (WKWebView *)webView:(WKWebView *)webView
    createWebViewWithConfiguration:(WKWebViewConfiguration *)configuration
               forNavigationAction:(WKNavigationAction *)navigationAction
                    windowFeatures:(WKWindowFeatures *)windowFeatures {
    NSURL *url = navigationAction.request.URL;
    if (url) {
        catappOpenWindowURL((char *)[[url absoluteString] UTF8String]);
    }
    return nil;
}

- (void)webView:(WKWebView *)webView
    runJavaScriptAlertPanelWithMessage:(NSString *)message
                      initiatedByFrame:(WKFrameInfo *)frame
                     completionHandler:(void (^)(void))completionHandler {
    NSAlert *a = [[NSAlert alloc] init];
    a.messageText = message;
    [a runModal];
    completionHandler();
}

// --- bridges --------------------------------------------------------------------

// catsClip: the native pasteboard, with a reply so the page keeps its promise
// shape (window.catsClipRead() is awaited by the paste path).
- (void)userContentController:(WKUserContentController *)ucc
      didReceiveScriptMessage:(WKScriptMessage *)message
                        replyHandler:(void (^)(id, NSString *))replyHandler {
    NSDictionary *m = [message.body isKindOfClass:[NSDictionary class]] ? message.body : nil;
    NSString *op = m[@"op"];
    if ([op isEqualToString:@"write"]) {
        NSString *text = m[@"text"] ?: @"";
        catappClipWrite((char *)[text UTF8String]);
        replyHandler(nil, nil);
        return;
    }
    if ([op isEqualToString:@"read"]) {
        char *out = catappClipRead();
        NSString *s = out ? [NSString stringWithUTF8String:out] : @"";
        free(out); // catappClipRead hands over a C.CString
        replyHandler(s, nil);
        return;
    }
    replyHandler(nil, @"unknown clipboard op");
}

// catsApp: the connect form's callbacks. Fire-and-forget — the form navigates
// as a result of what Go does, not of a return value.
- (void)userContentController:(WKUserContentController *)ucc
      didReceiveScriptMessage:(WKScriptMessage *)message {
    NSDictionary *m = [message.body isKindOfClass:[NSDictionary class]] ? message.body : nil;
    NSString *op = m[@"op"] ?: @"";
    NSString *url = m[@"url"] ?: @"";
    NSString *label = m[@"label"] ?: @"";
    catappConnectForm((char *)[op UTF8String], (char *)[url UTF8String],
                      (char *)[label UTF8String]);
}

// --- lifetime -------------------------------------------------------------------

- (void)windowWillClose:(NSNotification *)note {
    // Tear the handlers down explicitly: WKUserContentController retains its
    // message handlers, and a controller that outlived its window would keep the
    // whole web view alive with it.
    WKUserContentController *ucc = self.web.configuration.userContentController;
    [ucc removeScriptMessageHandlerForName:@"catsClip" contentWorld:[WKContentWorld pageWorld]];
    [ucc removeScriptMessageHandlerForName:@"catsApp"];
    self.web.UIDelegate = nil;
    self.web.navigationDelegate = nil;
    [gWindows removeObject:self];
    // Closing a window closes nothing in the session — the workspace it showed
    // keeps running on the server. All that changes here is the restore list.
    catappWindowsChanged();
}

- (void)windowDidMove:(NSNotification *)note { catappWindowsChanged(); }
- (void)windowDidResize:(NSNotification *)note { catappWindowsChanged(); }

@end

// --- app delegate ----------------------------------------------------------------

@interface CatsAppDelegate : NSObject <NSApplicationDelegate>
@end

@implementation CatsAppDelegate
// The last window closing reaps the backend, exactly as webview_go's Run()
// returning did. In remote mode there is nothing to reap and this is just quit.
- (BOOL)applicationShouldTerminateAfterLastWindowClosed:(NSApplication *)sender {
    return YES;
}
- (void)applicationWillTerminate:(NSNotification *)note {
    catappCleanup(); // idempotent; also reached by Cmd-Q and by a signal
}
// Clicking the Dock icon with every window closed should give you a window
// back rather than a dead icon.
- (BOOL)applicationShouldHandleReopen:(NSApplication *)sender hasVisibleWindows:(BOOL)flag {
    if (!flag) {
        catappNewWindow();
    }
    return YES;
}
- (void)newWindow:(id)sender { catappNewWindow(); }
@end

static CatsAppDelegate *gDelegate = nil;

// --- C entry points (called from window_darwin.go) --------------------------------

// catsAppStart creates the NSApplication, installs our delegate, and makes the
// app a regular foreground app. Must run on the main thread, before any window.
void catsAppStart(const char *cAppName) {
    @autoreleasepool {
        gAppName = [NSString stringWithUTF8String:cAppName];
        gWindows = [NSMutableArray array];
        [NSApplication sharedApplication];
        [NSApp setActivationPolicy:NSApplicationActivationPolicyRegular];
        if (!gDelegate) {
            gDelegate = [[CatsAppDelegate alloc] init];
        }
        [NSApp setDelegate:gDelegate];
    }
}

// catsWindowMenuTarget is what the Window menu's New Window item talks to. The
// menu is built in menu_darwin.m, which has no view of this file's classes.
id catsWindowMenuTarget(void) { return gDelegate; }

static NSRect catsFrameOrDefault(double x, double y, double w, double h) {
    if (w <= 0 || h <= 0) {
        return NSMakeRect(0, 0, kDefaultW, kDefaultH);
    }
    return NSMakeRect(x, y, w, h);
}

// catsOpenWindow opens a window on a URL at a saved frame (all zeros = "pick
// one"). A window with no saved frame is centred and then cascaded off whatever
// is already open, so a restored set does not stack into one pile.
void catsOpenWindow(const char *cURL, double x, double y, double w, double h) {
    @autoreleasepool {
        BOOL placed = (w > 0 && h > 0);
        CatsWindowController *wc =
            [[CatsWindowController alloc] initWithFrame:catsFrameOrDefault(x, y, w, h)];
        if (!placed) {
            [wc.window center];
            if (gWindows.count > 0) {
                NSWindow *last = gWindows.lastObject.window;
                NSPoint origin = NSMakePoint(NSMinX(last.frame), NSMaxY(last.frame));
                [wc.window setFrameTopLeftPoint:[wc.window cascadeTopLeftFromPoint:origin]];
            }
        }
        [gWindows addObject:wc];
        NSURL *url = [NSURL URLWithString:[NSString stringWithUTF8String:cURL]];
        if (url) {
            [wc.web loadRequest:[NSURLRequest requestWithURL:url]];
        }
        [wc showWindow:nil];
        [wc.window makeKeyAndOrderFront:nil];
        catappWindowsChanged();
    }
}

// catsOpenHTMLWindow opens a window showing a literal page — the connect form
// and nothing else. base is the URL the page is treated as coming from, so its
// relative links and its origin behave.
void catsOpenHTMLWindow(const char *cHTML, const char *cTitle) {
    @autoreleasepool {
        CatsWindowController *wc =
            [[CatsWindowController alloc] initWithFrame:NSMakeRect(0, 0, 720, 560)];
        [wc.window center];
        wc.window.title = [NSString stringWithUTF8String:cTitle];
        [gWindows addObject:wc];
        [wc.web loadHTMLString:[NSString stringWithUTF8String:cHTML] baseURL:nil];
        [wc showWindow:nil];
        [wc.window makeKeyAndOrderFront:nil];
    }
}

// catsShowHTMLInKeyWindow replaces the key window's content with a literal page
// (the connect form, reached from the Connect menu while a session is open).
// Opens one if there is no window at all.
void catsShowHTMLInKeyWindow(const char *cHTML, const char *cTitle) {
    @autoreleasepool {
        for (CatsWindowController *wc in gWindows) {
            if (wc.window.isKeyWindow) {
                wc.window.title = [NSString stringWithUTF8String:cTitle];
                [wc.web loadHTMLString:[NSString stringWithUTF8String:cHTML] baseURL:nil];
                return;
            }
        }
        catsOpenHTMLWindow(cHTML, cTitle);
    }
}

// catsNavigateAll points every window at a URL and re-titles them. Connecting a
// thin client to a different catway is a different SESSION, so every window has
// to move — leaving one behind would show two servers' workspaces side by side
// with no way to tell which was which.
void catsNavigateAll(const char *cURL, const char *cTitle) {
    @autoreleasepool {
        NSURL *url = [NSURL URLWithString:[NSString stringWithUTF8String:cURL]];
        gWindowTitle = [NSString stringWithUTF8String:cTitle];
        if (gWindows.count == 0) {
            catsOpenWindow(cURL, 0, 0, 0, 0);
            return;
        }
        for (CatsWindowController *wc in gWindows) {
            wc.window.title = gWindowTitle;
            if (url) {
                [wc.web loadRequest:[NSURLRequest requestWithURL:url]];
            }
        }
    }
}

// catsZoomKeyWindow steps the terminal font size in the FRONT window's page.
// The menu owns ⌘+/⌘-/⌘0 because Cocoa resolves them as key equivalents before
// the WKWebView sees a keydown; with several windows, "which page" is the key
// window's, not a process-global one.
void catsZoomKeyWindow(int delta) {
    @autoreleasepool {
        for (CatsWindowController *wc in gWindows) {
            if (wc.window.isKeyWindow) {
                NSString *js = [NSString
                    stringWithFormat:@"window.catsAdjustFont && window.catsAdjustFont(%d)", delta];
                [wc.web evaluateJavaScript:js completionHandler:nil];
                return;
            }
        }
    }
}

// catsWindowsJSON snapshots the open windows for the restore list: the
// workspace each is showing (read off its live URL, which the page keeps in
// step with its view) and its frame. The caller owns the returned string.
char *catsWindowsJSON(void) {
    @autoreleasepool {
        NSMutableArray *out = [NSMutableArray array];
        for (CatsWindowController *wc in gWindows) {
            NSURL *url = wc.web.URL;
            NSString *ws = @"";
            if (url) {
                NSURLComponents *c = [NSURLComponents componentsWithURL:url
                                                resolvingAgainstBaseURL:NO];
                for (NSURLQueryItem *q in c.queryItems) {
                    if ([q.name isEqualToString:@"ws"] && q.value) {
                        ws = q.value;
                    }
                }
            }
            NSRect f = wc.window.frame;
            [out addObject:@{@"workspace" : ws,
                             @"x" : @(f.origin.x),
                             @"y" : @(f.origin.y),
                             @"w" : @(f.size.width),
                             @"h" : @(f.size.height)}];
        }
        NSData *data = [NSJSONSerialization dataWithJSONObject:out options:0 error:nil];
        if (!data) {
            return strdup("[]");
        }
        NSString *s = [[NSString alloc] initWithData:data encoding:NSUTF8StringEncoding];
        return strdup([s UTF8String]);
    }
}

// catsSetWindowTitle sets the title new windows open with (and retitles the
// ones already open), so switching a thin client to another catway relabels
// every window rather than only the one that navigated first.
void catsSetWindowTitle(const char *cTitle) {
    @autoreleasepool {
        gWindowTitle = [NSString stringWithUTF8String:cTitle];
        for (CatsWindowController *wc in gWindows) {
            wc.window.title = gWindowTitle;
        }
    }
}

int catsWindowCount(void) { return gWindows ? (int)gWindows.count : 0; }

// catsDispatchMain asks the main thread to drain Go's main-queue closures. The
// window snapshot reads NSWindow frames, which is main-thread-only, and it is
// armed from a debounce timer on some goroutine — so the hop is not optional.
void catsDispatchMain(void) {
    dispatch_async(dispatch_get_main_queue(), ^{
      catappMainTick();
    });
}

// catsRunApp activates the app and enters the run loop. Returns when the app
// terminates (last window closed, Cmd-Q, or a signal handler's exit).
void catsRunApp(void) {
    @autoreleasepool {
        [NSApp activateIgnoringOtherApps:YES];
        [NSApp run];
    }
}
