; Windows setup for mtsync: installs for the current user (no administrator),
; in %LOCALAPPDATA%\Programs\mtsync, adds it to the user's Path and starts the
; service at every login (mtsync install). One setup carries both builds and
; picks the one for this PC. Built by the release workflow:
;   makensis -DVERSION=0.41.1 -DDIST="$PWD/dist" deploy/installers/mtsync-setup.nsi
; DIST (absolute: makensis works from this folder) holds
; mtsync-windows-amd64.exe and mtsync-windows-arm64.exe; the setup is written
; there as mtsync-windows-setup.exe.

Unicode true
!include MUI2.nsh
!include LogicLib.nsh
!include x64.nsh
!include WinMessages.nsh

!ifndef VERSION
  !error "Pass -DVERSION=X.Y.Z"
!endif
!ifndef DIST
  !error "Pass -DDIST=/absolute/path/to/dist"
!endif
!ifndef OUT
  !define OUT "${DIST}/mtsync-windows-setup.exe"
!endif

!define UNINSTALL_KEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\mtsync"

Name "Metatrash Sync (mtsync) ${VERSION}"
OutFile "${OUT}"
RequestExecutionLevel user
InstallDir "$LOCALAPPDATA\Programs\mtsync"
SetCompressor /SOLID lzma
ManifestDPIAware true

; Numeric part only: 0.42.0-rc.1 -> 0.42.0.0
!searchparse /noerrors "${VERSION}" "" VERSION_NUMBER "-"
VIProductVersion "${VERSION_NUMBER}.0"
VIAddVersionKey "ProductName" "Metatrash Sync"
VIAddVersionKey "FileDescription" "mtsync setup"
VIAddVersionKey "FileVersion" "${VERSION}"
VIAddVersionKey "ProductVersion" "${VERSION}"
VIAddVersionKey "LegalCopyright" "metatrash.com"

!define MUI_ABORTWARNING
!define MUI_FINISHPAGE_TEXT "mtsync is installed and its service runs at every login.$\r$\n$\r$\nNext, connect this computer to your Metatrash account and choose the spaces it may use. Then open Slate Viewer and use Open Space, or run mtsync add SPACE FOLDER in a terminal."
!define MUI_FINISHPAGE_RUN "$INSTDIR\mtsync.exe"
!define MUI_FINISHPAGE_RUN_PARAMETERS "login"
!define MUI_FINISHPAGE_RUN_TEXT "Connect this computer now (opens the browser)"
!define MUI_FINISHPAGE_LINK "How mtsync works"
!define MUI_FINISHPAGE_LINK_LOCATION "https://github.com/davezap/metatrash/tree/main/cmd/mtsync#readme"

!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES
!insertmacro MUI_LANGUAGE "English"

; Adds or removes the install folder in the user's Path. PowerShell, because
; NSIS strings stop at 1024 characters and a long Path would be cut; the
; Path stays REG_EXPAND_SZ, so entries like %USERPROFILE%\... keep working. The
; folder comes from LOCALAPPDATA (the setup has no folder page), so a quote
; in the user name can't break the command.
!macro UserPath ACTION
  nsExec::ExecToLog `powershell.exe -NoProfile -NonInteractive -ExecutionPolicy Bypass -Command "$$d = Join-Path $$env:LOCALAPPDATA 'Programs\mtsync'; $$k = Get-Item 'HKCU:\Environment'; $$p = $$k.GetValue('Path', '', 'DoNotExpandEnvironmentNames'); $$parts = @($$p -split ';' | Where-Object { $$_ -and $$_.TrimEnd('\') -ne $$d }); if ('${ACTION}' -eq 'add') { $$parts += $$d }; Set-ItemProperty 'HKCU:\Environment' -Name Path -Type ExpandString -Value ($$parts -join ';')"`
  Pop $0
  ; Tell Explorer, so new terminals see the change.
  SendMessage ${HWND_BROADCAST} ${WM_SETTINGCHANGE} 0 "STR:Environment" /TIMEOUT=5000
!macroend

Section "mtsync"
  SetOutPath "$INSTDIR"
  ; An update: the running service holds the program, so stop it first.
  ${If} ${FileExists} "$INSTDIR\mtsync.exe"
    DetailPrint "Stopping the running service"
    nsExec::ExecToLog '"$INSTDIR\mtsync.exe" stop'
    Pop $0
  ${EndIf}

  ${If} ${IsNativeARM64}
    File "/oname=mtsync.exe" "${DIST}/mtsync-windows-arm64.exe"
  ${Else}
    File "/oname=mtsync.exe" "${DIST}/mtsync-windows-amd64.exe"
  ${EndIf}
  WriteUninstaller "$INSTDIR\uninstall.exe"

  DetailPrint "Adding $INSTDIR to your Path"
  !insertmacro UserPath add

  DetailPrint "Starting the service at every login (mtsync install)"
  nsExec::ExecToLog '"$INSTDIR\mtsync.exe" install'
  Pop $0
  ${If} $0 != 0
    MessageBox MB_ICONEXCLAMATION|MB_OK "mtsync is installed, but its service did not start. Run mtsync log in a terminal to see why." /SD IDOK
  ${EndIf}

  ; Settings > Apps lists it, with Uninstall.
  WriteRegStr HKCU "${UNINSTALL_KEY}" "DisplayName" "Metatrash Sync (mtsync)"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "DisplayVersion" "${VERSION}"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "Publisher" "metatrash.com"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "DisplayIcon" "$INSTDIR\mtsync.exe"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "InstallLocation" "$INSTDIR"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "UninstallString" '"$INSTDIR\uninstall.exe"'
  WriteRegStr HKCU "${UNINSTALL_KEY}" "QuietUninstallString" '"$INSTDIR\uninstall.exe" /S'
  WriteRegStr HKCU "${UNINSTALL_KEY}" "URLInfoAbout" "https://metatrash.com"
  WriteRegDWORD HKCU "${UNINSTALL_KEY}" "NoModify" 1
  WriteRegDWORD HKCU "${UNINSTALL_KEY}" "NoRepair" 1
SectionEnd

; Uninstalling stops the service and removes the program. The sign-in,
; registered folders and the synced files stay, so a reinstall carries on.
Section "Uninstall"
  nsExec::ExecToLog '"$INSTDIR\mtsync.exe" uninstall'
  Pop $0
  Delete "$INSTDIR\mtsync.exe"
  Delete "$INSTDIR\uninstall.exe"
  RMDir "$INSTDIR"
  !insertmacro UserPath remove
  DeleteRegKey HKCU "${UNINSTALL_KEY}"
SectionEnd
