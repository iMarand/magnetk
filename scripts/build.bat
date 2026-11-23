@echo off
REM ==========================================================================
REM  Magnetk v2 build (Windows)
REM
REM    scripts\build.bat            CLI + libmagnetk (C ABI for Python/C++/C)
REM    scripts\build.bat msquic     also (re)build MsQuic (Release, Schannel)
REM    scripts\build.bat android    also build the Android field-test APK
REM    scripts\build.bat all        everything
REM
REM  Needs: Go 1.22+, MSYS2 gcc (C:\msys64\ucrt64 or mingw64) for cgo.
REM  MsQuic needs PowerShell 7, CMake and Visual Studio Build Tools.
REM  Output: build\bin (magnetk.exe + msquic.dll), build\lib (libmagnetk.dll
REM  + msquic.dll), copied into sdk\python\magnetk\lib for the Python SDK.
REM ==========================================================================
setlocal
cd /d "%~dp0.."
set ROOT=%CD%

if exist "C:\msys64\ucrt64\bin\gcc.exe" (
    set "PATH=C:\msys64\ucrt64\bin;%PATH%"
) else if exist "C:\msys64\mingw64\bin\gcc.exe" (
    set "PATH=C:\msys64\mingw64\bin;%PATH%"
)
where gcc >nul 2>nul || (echo [!] gcc not found: install MSYS2 and "pacman -S mingw-w64-ucrt-x86_64-gcc" & exit /b 1)
where go  >nul 2>nul || (echo [!] Go not found & exit /b 1)
set CGO_ENABLED=1

set MSQUIC_OUT=%ROOT%\msquic\artifacts\bin\windows\x64_Release_schannel
if /i "%1"=="msquic" goto msquic
if /i "%1"=="all" goto msquic
if not exist "%MSQUIC_OUT%\msquic.lib" goto msquic
goto magnetk

:msquic
echo === MsQuic (Release, Schannel) ===
pwsh -NoProfile -File msquic\scripts\build.ps1 -Config Release -Tls schannel -Arch x64 -DisableTest -DisableTools || (echo [!] MsQuic build failed & exit /b 1)

:magnetk
echo === magnetk CLI ===
if not exist build\bin mkdir build\bin
if not exist build\lib mkdir build\lib
pushd go
go build -trimpath -ldflags="-s -w" -o "%ROOT%\build\bin\magnetk.exe" .\cmd\magnetk || (popd & echo [!] CLI build failed & exit /b 1)
echo === libmagnetk (C ABI) ===
go build -trimpath -ldflags="-s -w" -buildmode=c-shared -o "%ROOT%\build\lib\libmagnetk.dll" .\capi || (popd & echo [!] libmagnetk build failed & exit /b 1)
popd
del /q build\lib\libmagnetk.h 2>nul
copy /y "%MSQUIC_OUT%\msquic.dll" build\bin\ >nul
copy /y "%MSQUIC_OUT%\msquic.dll" build\lib\ >nul
if not exist sdk\python\magnetk\lib mkdir sdk\python\magnetk\lib
copy /y build\lib\*.dll sdk\python\magnetk\lib\ >nul

if /i "%1"=="android" goto android
if /i "%1"=="all" goto android
goto done

:android
echo === Android field-test APK ===
python android\build.py %2 %3 %4 %5 %6 %7 || (echo [!] APK build failed & exit /b 1)

:done
echo.
echo Done.
echo   build\bin\magnetk.exe     CLI: relay, share, get, send, pub, sub, req, bench, diag ...
echo   build\lib\libmagnetk.dll  C ABI used by the Python and C++ SDKs
echo Quick start:
echo   build\bin\magnetk.exe relay
echo   build\bin\magnetk.exe bench -local all
endlocal
