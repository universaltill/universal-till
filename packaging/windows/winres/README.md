# Windows exe resources (ut-docs#2786)

`server.json` (`unitill-pos.exe`) and `desktop.json` (`unitill-desktop.exe`)
describe the icon and version info linked into the Windows binaries.
`packaging/windows/winres.sh` turns one of them into
`rsrc_windows_amd64.syso` next to that binary's `main` package; the Go
linker picks the `.syso` up only for `GOOS=windows GOARCH=amd64`.
goreleaser runs it as a `pre` hook of the `windows` and `desktop-windows`
builds. The generated `.syso` files are gitignored.

The icon group's ID is **32512** (`IDI_APPLICATION`) on purpose: the
vendored webview loads the window-class icon with
`LoadImage(GetModuleHandle(NULL), IDI_APPLICATION, …)`, so this one
resource gives the title bar and taskbar their icon, as well as Explorer,
the shortcuts and Apps & features (they use icon index 0, the only group).

`scripts/ci/checkpeicon` fails the build when an exe lacks either
resource.
