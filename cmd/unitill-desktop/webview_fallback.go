//go:build desktop && !darwin

package main

import webview "github.com/webview/webview_go"

// showWindow opens the webview_go window (Linux/Windows) and blocks until it
// closes. Run() returns on close, so main's deferred Kill stops the server;
// childPid is unused here.
func showWindow(url, title string, childPid int) {
	_ = childPid
	w := webview.New(false)
	defer w.Destroy()
	w.SetTitle(title)
	w.SetSize(1280, 860, webview.HintNone)
	w.Navigate(url)
	w.Run()
}
