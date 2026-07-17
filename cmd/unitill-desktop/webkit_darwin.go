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
@interface UTDelegate : NSObject <WKUIDelegate, WKNavigationDelegate, NSWindowDelegate, WKDownloadDelegate>
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

// isExternalURL: anything not served by the local till. The POS webview must
// never navigate off the till — external pages (marketplace claim/login,
// docs, vendor sites) open in the user's default browser instead, so the
// register is never stranded on a page with no way back.
static BOOL isExternalURL(NSURL *url) {
    NSString *scheme = url.scheme.lowercaseString;
    if (![scheme isEqualToString:@"http"] && ![scheme isEqualToString:@"https"]) {
        return NO; // about:blank etc. — not ours to reroute
    }
    NSString *host = url.host.lowercaseString;
    return !([host isEqualToString:@"localhost"] || [host isEqualToString:@"127.0.0.1"] ||
             [host isEqualToString:@"::1"] || [host isEqualToString:@"[::1]"]);
}

// window.open / target=_blank — external targets go to the default browser;
// till-local ones load in the same view instead of doing nothing.
- (WKWebView *)webView:(WKWebView *)webView
        createWebViewWithConfiguration:(WKWebViewConfiguration *)configuration
        forNavigationAction:(WKNavigationAction *)navigationAction
        windowFeatures:(WKWindowFeatures *)windowFeatures {
    if (navigationAction.targetFrame == nil) {
        NSURL *url = navigationAction.request.URL;
        if (isExternalURL(url)) {
            [[NSWorkspace sharedWorkspace] openURL:url];
        } else {
            [webView loadRequest:navigationAction.request];
        }
    }
    return nil;
}

// Plain-link navigations: same rule. Any main-frame navigation that would
// leave the till opens in the default browser and the webview stays put.
- (void)webView:(WKWebView *)webView
        decidePolicyForNavigationAction:(WKNavigationAction *)navigationAction
        decisionHandler:(void (^)(WKNavigationActionPolicy))decisionHandler {
    NSURL *url = navigationAction.request.URL;
    if (navigationAction.targetFrame != nil && navigationAction.targetFrame.isMainFrame &&
        isExternalURL(url)) {
        [[NSWorkspace sharedWorkspace] openURL:url];
        decisionHandler(WKNavigationActionPolicyCancel);
        return;
    }
    if (@available(macOS 11.3, *)) {
        if (navigationAction.shouldPerformDownload) {
            decisionHandler(WKNavigationActionPolicyDownload);
            return;
        }
    }
    decisionHandler(WKNavigationActionPolicyAllow);
}

// Responses the view can't display (attachments, unknown MIME types) become
// DOWNLOADS saved to ~/Downloads — CSV exports, backups, any <a download>.
- (void)webView:(WKWebView *)webView
        decidePolicyForNavigationResponse:(WKNavigationResponse *)navigationResponse
        decisionHandler:(void (^)(WKNavigationResponsePolicy))decisionHandler {
    if (@available(macOS 11.3, *)) {
        BOOL attachment = NO;
        if ([navigationResponse.response isKindOfClass:[NSHTTPURLResponse class]]) {
            NSString *cd = [((NSHTTPURLResponse *)navigationResponse.response).allHeaderFields[@"Content-Disposition"] lowercaseString];
            attachment = cd != nil && [cd containsString:@"attachment"];
        }
        if (attachment || !navigationResponse.canShowMIMEType) {
            decisionHandler(WKNavigationResponsePolicyDownload);
            return;
        }
    }
    decisionHandler(WKNavigationResponsePolicyAllow);
}

- (void)webView:(WKWebView *)webView
        navigationAction:(WKNavigationAction *)navigationAction
        didBecomeDownload:(WKDownload *)download API_AVAILABLE(macos(11.3)) {
    download.delegate = self;
}

- (void)webView:(WKWebView *)webView
        navigationResponse:(WKNavigationResponse *)navigationResponse
        didBecomeDownload:(WKDownload *)download API_AVAILABLE(macos(11.3)) {
    download.delegate = self;
}

// Save into ~/Downloads, deduping "name (2).ext" style like a browser.
- (void)download:(WKDownload *)download
        decideDestinationUsingResponse:(NSURLResponse *)response
        suggestedFilename:(NSString *)suggestedFilename
        completionHandler:(void (^)(NSURL *destination))completionHandler API_AVAILABLE(macos(11.3)) {
    NSURL *dir = [[[NSFileManager defaultManager] URLsForDirectory:NSDownloadsDirectory
                                                         inDomains:NSUserDomainMask] firstObject];
    NSString *name = suggestedFilename.length ? suggestedFilename : @"download";
    NSURL *dest = [dir URLByAppendingPathComponent:name];
    NSString *base = [name stringByDeletingPathExtension];
    NSString *ext = [name pathExtension];
    int i = 2;
    while ([[NSFileManager defaultManager] fileExistsAtPath:dest.path] && i < 1000) {
        NSString *candidate = ext.length
            ? [NSString stringWithFormat:@"%@ (%d).%@", base, i, ext]
            : [NSString stringWithFormat:@"%@ (%d)", base, i];
        dest = [dir URLByAppendingPathComponent:candidate];
        i++;
    }
    completionHandler(dest);
}

- (void)downloadDidFinish:(WKDownload *)download API_AVAILABLE(macos(11.3)) {
    NSLog(@"unitill: download finished");
}

- (void)download:(WKDownload *)download
        didFailWithError:(NSError *)error
        resumeData:(NSData *)resumeData API_AVAILABLE(macos(11.3)) {
    NSLog(@"unitill: download failed: %@", error.localizedDescription);
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
        webview.navigationDelegate = del; // pins the view to the till; external links → browser
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
