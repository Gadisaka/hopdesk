; HopDesk Windows installer (NSIS). Built by scripts/build-windows.sh:
;   makensis -DVERSION=1.2.3 -DBUILDDIR=<folder with HopDesk.exe> -DOUTFILE=<setup.exe> [-DWITH_FFMPEG] hopdesk.nsi
; Installs for the current user only (no admin rights needed).
; Automatic updates run it as:  HopDesk-Setup.exe /S /RELAUNCH
; (silent, into the existing install folder, then starts the new HopDesk).

Unicode true
SetCompressor /SOLID lzma
RequestExecutionLevel user

!ifndef VERSION
  !define VERSION "0.0.0"
!endif

Name "HopDesk"
OutFile "${OUTFILE}"
InstallDir "$LOCALAPPDATA\Programs\HopDesk"
; Updates go into the folder HopDesk was installed in, even if it was changed.
InstallDirRegKey HKCU "Software\Microsoft\Windows\CurrentVersion\Uninstall\HopDesk" "InstallLocation"
BrandingText "HopDesk ${VERSION}"

!include "MUI2.nsh"
!include "FileFunc.nsh"
!include "LogicLib.nsh"
!define MUI_ICON "hopdesk.ico"
!define MUI_UNICON "hopdesk.ico"
!define MUI_FINISHPAGE_RUN "$INSTDIR\HopDesk.exe"
!define MUI_FINISHPAGE_RUN_TEXT "Open HopDesk now"
!define MUI_WELCOMEPAGE_TEXT "HopDesk lets you reach this computer from your own devices.$\r$\n$\r$\nAfter installing, HopDesk opens a setup page in your browser and lives in the notification area next to the clock."

!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES
!insertmacro MUI_LANGUAGE "English"

!define UNINSTKEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\HopDesk"

Section "HopDesk"
  ; Close a running copy so its files can be replaced.
  nsExec::Exec 'taskkill /IM HopDesk.exe /F'
  Sleep 500

  SetOutPath "$INSTDIR"
  File "${BUILDDIR}/HopDesk.exe"
  !ifdef WITH_FFMPEG
    File "${BUILDDIR}/ffmpeg.exe"
    File "${BUILDDIR}/ffmpeg-LICENSE.txt"
  !endif
  WriteUninstaller "$INSTDIR\Uninstall.exe"

  CreateDirectory "$SMPROGRAMS\HopDesk"
  CreateShortcut "$SMPROGRAMS\HopDesk\HopDesk.lnk" "$INSTDIR\HopDesk.exe"
  CreateShortcut "$SMPROGRAMS\HopDesk\Uninstall HopDesk.lnk" "$INSTDIR\Uninstall.exe"

  WriteRegStr HKCU "${UNINSTKEY}" "DisplayName" "HopDesk"
  WriteRegStr HKCU "${UNINSTKEY}" "DisplayVersion" "${VERSION}"
  WriteRegStr HKCU "${UNINSTKEY}" "Publisher" "HopDesk"
  WriteRegStr HKCU "${UNINSTKEY}" "DisplayIcon" "$INSTDIR\HopDesk.exe"
  WriteRegStr HKCU "${UNINSTKEY}" "InstallLocation" "$INSTDIR"
  WriteRegStr HKCU "${UNINSTKEY}" "UninstallString" '"$INSTDIR\Uninstall.exe"'
  WriteRegDWORD HKCU "${UNINSTKEY}" "NoModify" 1
  WriteRegDWORD HKCU "${UNINSTKEY}" "NoRepair" 1
SectionEnd

; After a silent update, start the new version (the old one was closed above).
Function .onInstSuccess
  ${GetParameters} $R0
  ClearErrors
  ${GetOptions} $R0 "/RELAUNCH" $R1
  ${IfNot} ${Errors}
    Exec '"$INSTDIR\HopDesk.exe" -app'
  ${EndIf}
FunctionEnd

Section "Uninstall"
  nsExec::Exec 'taskkill /IM HopDesk.exe /F'
  Sleep 500
  DeleteRegValue HKCU "Software\Microsoft\Windows\CurrentVersion\Run" "HopDesk"
  DeleteRegKey HKCU "${UNINSTKEY}"
  Delete "$INSTDIR\HopDesk.exe"
  Delete "$INSTDIR\ffmpeg.exe"
  Delete "$INSTDIR\ffmpeg-LICENSE.txt"
  Delete "$INSTDIR\Uninstall.exe"
  RMDir "$INSTDIR"
  Delete "$SMPROGRAMS\HopDesk\HopDesk.lnk"
  Delete "$SMPROGRAMS\HopDesk\Uninstall HopDesk.lnk"
  RMDir "$SMPROGRAMS\HopDesk"
  ; Settings and this computer's identity stay in %APPDATA%\hopdesk,
  ; so reinstalling keeps the same fingerprint. Delete that folder to reset.
SectionEnd
