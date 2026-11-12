; 书库 Windows 版的安装程序（NSIS）。书库在 NAS 上现场做（server/desktop.go 调 makensis），
; 也能在 Windows 上做（.github/workflows/desktop-check.yml）。
;   makensis -INPUTCHARSET UTF8 -DRUNTIME=运行环境目录 -DAPPSRC=程序目录 -DOUTFILE=安装程序.exe -DVERSION=1.0.0.33 installer.nsi
; 装到 %LOCALAPPDATA%\Programs\Shuku（不用管理员），桌面、开始菜单放“书库”，装完马上打开。
; 设置、语音模型在 %LOCALAPPDATA%\Shuku，卸载时留着（重新装不用再下载模型）。

Unicode true
!ifndef VERSION
  !define VERSION "dev"
!endif
Name "书库"
Caption "安装书库 ${VERSION}"
OutFile "${OUTFILE}"
InstallDir "$LOCALAPPDATA\Programs\Shuku"
RequestExecutionLevel user
SetCompressor /SOLID lzma
SetCompressorDictSize 16
Icon "${APPSRC}\shuku.ico"
UninstallIcon "${APPSRC}\shuku.ico"
BrandingText "书库 ${VERSION}"
ShowInstDetails nevershow
ShowUninstDetails nevershow
AutoCloseWindow true

Page instfiles
UninstPage instfiles

!define UNINST "Software\Microsoft\Windows\CurrentVersion\Uninstall\Shuku"
!define RUN "Software\Microsoft\Windows\CurrentVersion\Run"

; 正在运行的书库先退出（本机设置页的 /quit），不然文件被占着换不了
!macro QuitApp
  nsExec::Exec 'powershell -NoProfile -NonInteractive -WindowStyle Hidden -Command "try { Invoke-WebRequest -UseBasicParsing -TimeoutSec 3 -Method Post http://127.0.0.1:27230/quit | Out-Null; Start-Sleep -Seconds 2 } catch {}"'
  Pop $0
!macroend

Section "书库"
  !insertmacro QuitApp
  SetOutPath "$INSTDIR"
  ; 运行环境整个换掉（旧的库留着可能和新的混在一起）
  RMDir /r "$INSTDIR\python"
  SetOutPath "$INSTDIR\python"
  File /r "${RUNTIME}\python\*.*"
  SetOutPath "$INSTDIR\app"
  File /r "${APPSRC}\*.*"
  SetOutPath "$INSTDIR"
  CreateShortCut "$DESKTOP\书库.lnk" "$INSTDIR\python\pythonw.exe" '"$INSTDIR\app\shuku_desktop.py"' "$INSTDIR\app\shuku.ico" 0 SW_SHOWNORMAL "" "书库：看书、听书加速"
  CreateShortCut "$SMPROGRAMS\书库.lnk" "$INSTDIR\python\pythonw.exe" '"$INSTDIR\app\shuku_desktop.py"' "$INSTDIR\app\shuku.ico" 0 SW_SHOWNORMAL "" "书库：看书、听书加速"
  WriteUninstaller "$INSTDIR\uninstall.exe"
  WriteRegStr HKCU "${UNINST}" "DisplayName" "书库"
  WriteRegStr HKCU "${UNINST}" "DisplayVersion" "${VERSION}"
  WriteRegStr HKCU "${UNINST}" "Publisher" "书库"
  WriteRegStr HKCU "${UNINST}" "DisplayIcon" "$INSTDIR\app\shuku.ico"
  WriteRegStr HKCU "${UNINST}" "InstallLocation" "$INSTDIR"
  WriteRegStr HKCU "${UNINST}" "UninstallString" '"$INSTDIR\uninstall.exe"'
  WriteRegStr HKCU "${UNINST}" "QuietUninstallString" '"$INSTDIR\uninstall.exe" /S'
  WriteRegDWORD HKCU "${UNINST}" "NoModify" 1
  WriteRegDWORD HKCU "${UNINST}" "NoRepair" 1
SectionEnd

Function .onInstSuccess
  ; 装完（包括自动升级的静默安装）马上打开
  Exec '"$INSTDIR\python\pythonw.exe" "$INSTDIR\app\shuku_desktop.py"'
FunctionEnd

Section "Uninstall"
  !insertmacro QuitApp
  Delete "$DESKTOP\书库.lnk"
  Delete "$SMPROGRAMS\书库.lnk"
  DeleteRegValue HKCU "${RUN}" "ShukuDesktop"
  DeleteRegKey HKCU "${UNINST}"
  RMDir /r "$INSTDIR\python"
  RMDir /r "$INSTDIR\app"
  Delete "$INSTDIR\uninstall.exe"
  RMDir "$INSTDIR"
SectionEnd
