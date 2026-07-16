//go:build desktop && darwin

package main

/*
#cgo CFLAGS: -x objective-c -Wno-deprecated-declarations
#cgo LDFLAGS: -framework Cocoa -framework WebKit
#import <Cocoa/Cocoa.h>
#import <WebKit/WebKit.h>
#include <signal.h>

static pid_t gChildPid = 0;

// UTDelegate wires the WebView capabilities a real browser has but the bare
// webview lacks: file pickers, camera permission, popups and JS dialogs; plus
// stopping the POS server when the window closes.
@interface UTDelegate : NSObject <WKUIDelegate, NSWindowDelegate>
@end

@implementation UTDelegate

- (void)windowWillClose:(NSNotification *)n {
    if (gChildPid > 0) kill(gChildPid, SIGTERM); // stop the POS server
    [NSApp terminate:nil];
}

// <input type="file"> — open a native file picker (import CSV, logo upload, …).
- (void)webView:(WKWebView *)webView
        runOpenPanelWithParameters:(WKOpenPanelParameters *)parameters
        initiatedByFrame:(WKFrameInfo *)frame
        completionHandler:(void (^)(NSArray<NSURL *> *URLs))completionHandler {
    NSOpenPanel *panel = [NSOpenPanel openPanel];
    panel.canChooseFiles = YES;
    panel.canChooseDirectories = NO;
    panel.allowsMultipleSelection = parameters.allowsMultipleSelection;
    [panel beginWithCompletionHandler:^(NSModalResponse r){
        completionHandler(r == NSModalResponseOK ? panel.URLs : nil);
    }];
}

// getUserMedia camera/mic permission — grant it (the AI plugin scans with the
// camera). Only sent on macOS 12+.
- (void)webView:(WKWebView *)webView
        requestMediaCapturePermissionForOrigin:(WKSecurityOrigin *)origin
        initiatedByFrame:(WKFrameInfo *)frame
        type:(WKMediaCaptureType)type
        decisionHandler:(void (^)(WKPermissionDecision))decisionHandler {
    decisionHandler(WKPermissionDecisionGrant);
}

// window.open / target=_blank — load in the same view instead of doing nothing.
- (WKWebView *)webView:(WKWebView *)webView
        createWebViewWithConfiguration:(WKWebViewConfiguration *)configuration
        forNavigationAction:(WKNavigationAction *)navigationAction
        windowFeatures:(WKWindowFeatures *)windowFeatures {
    if (navigationAction.targetFrame == nil) {
        [webView loadRequest:navigationAction.request];
    }
    return nil;
}

// JS alert()/confirm() — native panels (so e.g. the update confirm works).
- (void)webView:(WKWebView *)webView
        runJavaScriptAlertPanelWithMessage:(NSString *)message
        initiatedByFrame:(WKFrameInfo *)frame
        completionHandler:(void (^)(void))completionHandler {
    NSAlert *a = [[NSAlert alloc] init];
    a.messageText = message ?: @"";
    [a runModal];
    completionHandler();
}
- (void)webView:(WKWebView *)webView
        runJavaScriptConfirmPanelWithMessage:(NSString *)message
        initiatedByFrame:(WKFrameInfo *)frame
        completionHandler:(void (^)(BOOL))completionHandler {
    NSAlert *a = [[NSAlert alloc] init];
    a.messageText = message ?: @"";
    [a addButtonWithTitle:@"OK"];
    [a addButtonWithTitle:@"Cancel"];
    completionHandler([a runModal] == NSAlertFirstButtonReturn);
}
@end

// buildMenu creates the App + Edit menus. The Edit menu is what makes
// Cmd-C/Cmd-V (copy/paste) and the standard clipboard work in WKWebView.
static void buildMenu(void) {
    NSMenu *bar = [[NSMenu alloc] init];
    [NSApp setMainMenu:bar];

    NSMenuItem *appItem = [[NSMenuItem alloc] init];
    [bar addItem:appItem];
    NSMenu *appMenu = [[NSMenu alloc] init];
    [appMenu addItemWithTitle:@"Quit Universal Till" action:@selector(terminate:) keyEquivalent:@"q"];
    [appItem setSubmenu:appMenu];

    NSMenuItem *editItem = [[NSMenuItem alloc] init];
    [bar addItem:editItem];
    NSMenu *edit = [[NSMenu alloc] initWithTitle:@"Edit"];
    [edit addItemWithTitle:@"Undo" action:@selector(undo:) keyEquivalent:@"z"];
    NSMenuItem *redo = [edit addItemWithTitle:@"Redo" action:@selector(redo:) keyEquivalent:@"z"];
    [redo setKeyEquivalentModifierMask:(NSEventModifierFlagCommand | NSEventModifierFlagShift)];
    [edit addItem:[NSMenuItem separatorItem]];
    [edit addItemWithTitle:@"Cut" action:@selector(cut:) keyEquivalent:@"x"];
    [edit addItemWithTitle:@"Copy" action:@selector(copy:) keyEquivalent:@"c"];
    [edit addItemWithTitle:@"Paste" action:@selector(paste:) keyEquivalent:@"v"];
    [edit addItemWithTitle:@"Select All" action:@selector(selectAll:) keyEquivalent:@"a"];
    [editItem setSubmenu:edit];
}

void RunWebView(const char *curl, const char *ctitle, int childPid) {
    gChildPid = (pid_t)childPid;
    @autoreleasepool {
        [NSApplication sharedApplication];
        [NSApp setActivationPolicy:NSApplicationActivationPolicyRegular];
        buildMenu();

        NSRect frame = NSMakeRect(0, 0, 1280, 860);
        NSWindow *window = [[NSWindow alloc]
            initWithContentRect:frame
            styleMask:(NSWindowStyleMaskTitled | NSWindowStyleMaskClosable |
                       NSWindowStyleMaskResizable | NSWindowStyleMaskMiniaturizable)
            backing:NSBackingStoreBuffered
            defer:NO];

        WKWebViewConfiguration *cfg = [[WKWebViewConfiguration alloc] init];
        WKWebView *webview = [[WKWebView alloc] initWithFrame:frame configuration:cfg];
        [webview setAutoresizingMask:(NSViewWidthSizable | NSViewHeightSizable)];

        UTDelegate *del = [[UTDelegate alloc] init]; // never released: lives for the app
        webview.UIDelegate = del;
        window.delegate = del;

        [window setTitle:[NSString stringWithUTF8String:ctitle]];
        [window setContentView:webview];
        [window center];
        [window makeKeyAndOrderFront:nil];

        NSURL *url = [NSURL URLWithString:[NSString stringWithUTF8String:curl]];
        [webview loadRequest:[NSURLRequest requestWithURL:url]];

        [NSApp activateIgnoringOtherApps:YES];
        [NSApp run];
    }
}
*/
import "C"

import "unsafe"

// showWindow opens the native WKWebView window and blocks until it closes.
// childPid is the POS server process, stopped when the window closes.
func showWindow(url, title string, childPid int) {
	cURL := C.CString(url)
	cTitle := C.CString(title)
	defer C.free(unsafe.Pointer(cURL))
	defer C.free(unsafe.Pointer(cTitle))
	C.RunWebView(cURL, cTitle, C.int(childPid))
}
