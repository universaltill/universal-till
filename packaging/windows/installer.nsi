; Universal Till POS — Windows installer (NSIS, Modern UI 2).
; Built in CI after goreleaser: the Windows zip is extracted to a staging dir
; and this script packages it into a guided Setup.exe with a wizard.
;
;   makensis -DVERSION=<x.y.z> -DSRCDIR=<staging> [-DUNINST_SIGN_CMD=<signer>] installer.nsi
;
; Per-user install (no admin, and the app can write its ./data next to the
; binary — Program Files would be read-only and break the DB).

Unicode true
!include "MUI2.nsh"

!ifndef VERSION
  !define VERSION "0.0.0"
!endif
!ifndef SRCDIR
  !define SRCDIR "staging"
!endif

!define APPNAME "Universal Till"
!define PUBLISHER "Task Runner Technology LTD"
!define EXENAME "unitill-pos.exe"
; The desktop shell (Edge WebView2 window, like the mac app): the shortcuts
; launch this; it starts unitill-pos.exe itself with no console window.
!define SHELLEXE "unitill-desktop.exe"
!define UNINSTKEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\UniversalTill"

Name "${APPNAME}"
OutFile "unitill-pos-setup-${VERSION}.exe"

; Sign the uninstaller that WriteUninstaller embeds (ut-docs#2610). release.yml
; passes -DUNINST_SIGN_CMD=<path to packaging/windows/sign-exe.sh>; makensis
; runs it on the generated uninstall.exe before packing it and aborts the
; build if it exits non-zero (NSIS >= 3.08). Unset = local unsigned build.
!ifdef UNINST_SIGN_CMD
  !uninstfinalize '"${UNINST_SIGN_CMD}" "%1"' = 0
!endif
; Per-user install location (writable — the till stores its database here).
InstallDir "$LOCALAPPDATA\Programs\Universal Till"
InstallDirRegKey HKCU "Software\UniversalTill" "InstallDir"
RequestExecutionLevel user
SetCompressor /SOLID lzma

VIProductVersion "${VERSION}.0"
VIAddVersionKey "ProductName" "${APPNAME}"
VIAddVersionKey "CompanyName" "${PUBLISHER}"
VIAddVersionKey "FileDescription" "${APPNAME} installer"
VIAddVersionKey "FileVersion" "${VERSION}"
VIAddVersionKey "LegalCopyright" "© ${PUBLISHER}"

!define MUI_ABORTWARNING
!define MUI_FINISHPAGE_RUN "$INSTDIR\${SHELLEXE}"
!define MUI_FINISHPAGE_RUN_TEXT "Start ${APPNAME} now"
!define MUI_FINISHPAGE_LINK "universaltill.com"
!define MUI_FINISHPAGE_LINK_LOCATION "https://www.universaltill.com"

!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_LICENSE "${SRCDIR}\LICENSE"
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH

!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES

!insertmacro MUI_LANGUAGE "English"

; Stop a running Universal Till before its files are replaced or removed
; (ut-docs#2760). A running unitill-pos.exe — including one orphaned by a
; crashed shell — locks its own image, and File then fails with "Error
; opening file for writing". Only copies whose image lives under $INSTDIR are
; stopped (a headless unitill-pos elsewhere is left alone); the shell goes
; first so its kill-on-close job takes its server down with it. The path
; reaches PowerShell through an environment variable, never spliced into the
; script text, so an apostrophe in the user's profile path can't break it.
; Processes are found through WMI (Win32_Process.ExecutablePath), not
; Get-Process: this installer is 32-bit, so nsExec starts the 32-bit
; PowerShell, whose Get-Process returns a null Path for every 64-bit
; process — the filter would silently match nothing.
; Best-effort: if PowerShell can't run or the script fails, the install log
; says so and carries on; File reports any file that is still locked.
!macro StopRunningTill
  DetailPrint "Closing ${APPNAME} if it is running…"
  System::Call 'Kernel32::SetEnvironmentVariable(t "UT_STOP_DIR", t "$INSTDIR")'
  nsExec::ExecToLog `powershell.exe -NoProfile -NonInteractive -ExecutionPolicy Bypass -Command "$$d = $$env:UT_STOP_DIR.TrimEnd('\') + '\'; try { foreach ($$n in @('unitill-desktop.exe', 'unitill-pos.exe')) { Get-CimInstance Win32_Process -Filter ('Name=''' + $$n + '''') -ErrorAction Stop | Where-Object { $$_.ExecutablePath -and $$_.ExecutablePath.StartsWith($$d, [StringComparison]::OrdinalIgnoreCase) } | ForEach-Object { Write-Output ('Stopping ' + $$_.Name + ' (pid ' + $$_.ProcessId + ')'); Stop-Process -Id $$_.ProcessId -Force -ErrorAction SilentlyContinue; Wait-Process -Id $$_.ProcessId -Timeout 15 -ErrorAction SilentlyContinue } }; exit 0 } catch { Write-Output $$_; exit 1 }"`
  Pop $0
  StrCmp $0 "0" +2 0
    DetailPrint "Could not check for a running ${APPNAME} ($0); continuing."
!macroend

Section "Universal Till" SecMain
  SectionIn RO
  SetOutPath "$INSTDIR"
  !insertmacro StopRunningTill
  ; Everything the goreleaser Windows archive contains: the exe, web/ assets,
  ; README, LICENSE, pos.env.example, and the .bat launcher.
  File /r "${SRCDIR}\*.*"

  ; Shortcuts. The shortcut's working directory is $INSTDIR (last SetOutPath),
  ; so the shell finds web/ and the till creates its data/ folder here. The
  ; shortcuts launch the DESKTOP SHELL (its own window, no terminal, no
  ; browser); unitill-pos.exe stays available for headless/server use.
  CreateDirectory "$SMPROGRAMS\${APPNAME}"
  CreateShortcut "$SMPROGRAMS\${APPNAME}\${APPNAME}.lnk" "$INSTDIR\${SHELLEXE}" "" "$INSTDIR\${SHELLEXE}" 0
  CreateShortcut "$DESKTOP\${APPNAME}.lnk" "$INSTDIR\${SHELLEXE}" "" "$INSTDIR\${SHELLEXE}" 0
  CreateShortcut "$SMPROGRAMS\${APPNAME}\Uninstall ${APPNAME}.lnk" "$INSTDIR\uninstall.exe"

  ; Add/Remove Programs entry (per-user hive).
  WriteRegStr HKCU "Software\UniversalTill" "InstallDir" "$INSTDIR"
  WriteRegStr HKCU "${UNINSTKEY}" "DisplayName" "${APPNAME}"
  WriteRegStr HKCU "${UNINSTKEY}" "DisplayVersion" "${VERSION}"
  WriteRegStr HKCU "${UNINSTKEY}" "Publisher" "${PUBLISHER}"
  WriteRegStr HKCU "${UNINSTKEY}" "DisplayIcon" "$INSTDIR\${SHELLEXE}"
  WriteRegStr HKCU "${UNINSTKEY}" "UninstallString" "$INSTDIR\uninstall.exe"
  WriteRegStr HKCU "${UNINSTKEY}" "URLInfoAbout" "https://www.universaltill.com"
  WriteRegDWORD HKCU "${UNINSTKEY}" "NoModify" 1
  WriteRegDWORD HKCU "${UNINSTKEY}" "NoRepair" 1

  WriteUninstaller "$INSTDIR\uninstall.exe"
SectionEnd

Section "Uninstall"
  !insertmacro StopRunningTill
  ; Preserve the shop's database: remove app files but keep the data/ folder
  ; (the operator can delete it by hand if they really mean to).
  Delete "$INSTDIR\${EXENAME}"
  Delete "$INSTDIR\${SHELLEXE}"
  Delete "$INSTDIR\uninstall.exe"
  Delete "$INSTDIR\README.md"
  Delete "$INSTDIR\LICENSE"
  Delete "$INSTDIR\pos.env.example"
  Delete "$INSTDIR\run-unitill.bat"
  RMDir /r "$INSTDIR\web"

  Delete "$DESKTOP\${APPNAME}.lnk"
  RMDir /r "$SMPROGRAMS\${APPNAME}"

  DeleteRegKey HKCU "${UNINSTKEY}"
  DeleteRegKey HKCU "Software\UniversalTill"
  ; $INSTDIR itself is left if data/ still lives there (RMDir only removes it
  ; when empty), so an uninstall never silently destroys sales history.
  RMDir "$INSTDIR"
SectionEnd
