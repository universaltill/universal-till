; Universal Till POS — Windows installer (NSIS, Modern UI 2).
; Built in CI after goreleaser: the Windows zip is extracted to a staging dir
; and this script packages it into a guided Setup.exe with a wizard.
;
;   makensis -DVERSION=<x.y.z> -DSRCDIR=<staging> installer.nsi
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
!define UNINSTKEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\UniversalTill"

Name "${APPNAME}"
OutFile "unitill-pos-setup-${VERSION}.exe"
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
!define MUI_FINISHPAGE_RUN "$INSTDIR\${EXENAME}"
!define MUI_FINISHPAGE_RUN_TEXT "Start ${APPNAME} now (opens the setup page in your browser)"
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

Section "Universal Till" SecMain
  SectionIn RO
  SetOutPath "$INSTDIR"
  ; Everything the goreleaser Windows archive contains: the exe, web/ assets,
  ; README, LICENSE, pos.env.example, and the .bat launcher.
  File /r "${SRCDIR}\*.*"

  ; Shortcuts. The shortcut's working directory is $INSTDIR (last SetOutPath),
  ; so the till finds web/ and creates its data/ folder here. It opens the
  ; browser itself, so the shortcut just launches the exe.
  CreateDirectory "$SMPROGRAMS\${APPNAME}"
  CreateShortcut "$SMPROGRAMS\${APPNAME}\${APPNAME}.lnk" "$INSTDIR\${EXENAME}" "" "$INSTDIR\${EXENAME}" 0
  CreateShortcut "$DESKTOP\${APPNAME}.lnk" "$INSTDIR\${EXENAME}" "" "$INSTDIR\${EXENAME}" 0
  CreateShortcut "$SMPROGRAMS\${APPNAME}\Uninstall ${APPNAME}.lnk" "$INSTDIR\uninstall.exe"

  ; Add/Remove Programs entry (per-user hive).
  WriteRegStr HKCU "Software\UniversalTill" "InstallDir" "$INSTDIR"
  WriteRegStr HKCU "${UNINSTKEY}" "DisplayName" "${APPNAME}"
  WriteRegStr HKCU "${UNINSTKEY}" "DisplayVersion" "${VERSION}"
  WriteRegStr HKCU "${UNINSTKEY}" "Publisher" "${PUBLISHER}"
  WriteRegStr HKCU "${UNINSTKEY}" "DisplayIcon" "$INSTDIR\${EXENAME}"
  WriteRegStr HKCU "${UNINSTKEY}" "UninstallString" "$INSTDIR\uninstall.exe"
  WriteRegStr HKCU "${UNINSTKEY}" "URLInfoAbout" "https://www.universaltill.com"
  WriteRegDWORD HKCU "${UNINSTKEY}" "NoModify" 1
  WriteRegDWORD HKCU "${UNINSTKEY}" "NoRepair" 1

  WriteUninstaller "$INSTDIR\uninstall.exe"
SectionEnd

Section "Uninstall"
  ; Preserve the shop's database: remove app files but keep the data/ folder
  ; (the operator can delete it by hand if they really mean to).
  Delete "$INSTDIR\${EXENAME}"
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
