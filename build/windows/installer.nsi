; Clipferry installer. Per-user install: no administrator rights needed.
Unicode true
!include "MUI2.nsh"

Name "Clipferry"
OutFile "${OUT}"
InstallDir "$LOCALAPPDATA\Programs\Clipferry"
InstallDirRegKey HKCU "Software\Clipferry" "InstallDir"
RequestExecutionLevel user
SetCompressor /SOLID lzma
VIProductVersion "${NUMERIC}.0"
VIAddVersionKey "ProductName" "Clipferry"
VIAddVersionKey "FileDescription" "Clipferry installer"
VIAddVersionKey "FileVersion" "${VERSION}"
VIAddVersionKey "LegalCopyright" "MIT License"

!define MUI_ICON "${ICON}"
!define MUI_UNICON "${ICON}"
!define MUI_FINISHPAGE_RUN "$INSTDIR\Clipferry.exe"
!define MUI_FINISHPAGE_RUN_TEXT "Start Clipferry"
!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES
!insertmacro MUI_LANGUAGE "English"

!define UNINST_KEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\Clipferry"

Section "Install"
  ; Stop a running copy so the file can be replaced on upgrade.
  nsExec::Exec 'taskkill /IM Clipferry.exe /F'
  SetOutPath "$INSTDIR"
  File "/oname=Clipferry.exe" "${SRC}"
  WriteUninstaller "$INSTDIR\Uninstall.exe"
  CreateShortcut "$SMPROGRAMS\Clipferry.lnk" "$INSTDIR\Clipferry.exe"
  WriteRegStr HKCU "Software\Clipferry" "InstallDir" "$INSTDIR"
  WriteRegStr HKCU "${UNINST_KEY}" "DisplayName" "Clipferry"
  WriteRegStr HKCU "${UNINST_KEY}" "DisplayVersion" "${VERSION}"
  WriteRegStr HKCU "${UNINST_KEY}" "Publisher" "Clipferry"
  WriteRegStr HKCU "${UNINST_KEY}" "DisplayIcon" "$INSTDIR\Clipferry.exe"
  WriteRegStr HKCU "${UNINST_KEY}" "UninstallString" '"$INSTDIR\Uninstall.exe"'
  WriteRegDWORD HKCU "${UNINST_KEY}" "NoModify" 1
  WriteRegDWORD HKCU "${UNINST_KEY}" "NoRepair" 1
  ; Automatic updates run this installer silently: start the new version.
  IfSilent 0 +2
    Exec '"$INSTDIR\Clipferry.exe"'
SectionEnd

Section "Uninstall"
  nsExec::Exec 'taskkill /IM Clipferry.exe /F'
  DeleteRegValue HKCU "Software\Microsoft\Windows\CurrentVersion\Run" "Clipferry"
  Delete "$INSTDIR\Clipferry.exe"
  Delete "$INSTDIR\Uninstall.exe"
  RMDir "$INSTDIR"
  Delete "$SMPROGRAMS\Clipferry.lnk"
  DeleteRegKey HKCU "${UNINST_KEY}"
  DeleteRegKey HKCU "Software\Clipferry"
  ; History and settings in %APPDATA%\Clipferry are kept on purpose.
SectionEnd
