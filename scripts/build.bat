@echo off
REM Magnetk P2P Build Script for Windows

echo ========================================
echo  Magnetk P2P File Transfer System
echo  Build Script
echo ========================================
echo.

REM Navigate to project root (one level up from scripts)
cd /d "%~dp0.."

REM Set MinGW Path (Required for MsQuic/CGo)
set "Path=C:\msys64\mingw64\bin;%Path%"

echo Go version:
go version
echo.

REM Create output directory
if not exist "bin" mkdir bin

# Tidy dependencies
echo Tidying Go modules...
cd go
go mod tidy
cd ..

REM Build binaries
echo Building relay node...
cd go
go build -o ../bin/relay.exe ./cmd/relay
if %errorlevel% neq 0 (
    echo Build failed for relay
    cd ..
    exit /b 1
)

echo Building seed node...
go build -o ../bin/seed.exe ./cmd/seed
if %errorlevel% neq 0 (
    echo Build failed for seed
    cd ..
    exit /b 1
)
if %errorlevel% neq 0 (
    echo Build failed for seed
    cd ..
    exit /b 1
)

REM Build peer client
echo Building peer client...
go build -o ../bin/peer.exe ./cmd/peer
if %errorlevel% neq 0 (
    echo Build failed for peer
    cd ..
    exit /b 1
)

REM Copy shared libraries to bin
echo Copying dependencies...
if exist "..\c\build\msquic.dll" copy /y "..\c\build\msquic.dll" "..\bin\" >nul
if exist "..\c\build\libmagnetk_transport.dll" copy /y "..\c\build\libmagnetk_transport.dll" "..\bin\" >nul

echo.
echo ========================================
echo  Build Complete!
echo ========================================
echo.
echo Binaries and libraries are in the 'bin' directory:
echo   - bin\relay.exe  (Relay Node)
echo   - bin\seed.exe   (Seed Node)
echo   - bin\peer.exe   (Peer Client)
echo   - bin\msquic.dll (QUIC Engine)
echo   - bin\libmagnetk_transport.dll (C-Transport)
echo.
echo Usage:
echo   1. Start relay:  bin\relay.exe --port 4001
echo   2. Start seed:   bin\seed.exe --relay "relay_addr" --file "filepath"
echo   3. Download:     bin\peer.exe --magnetk "magnetk:?..." --output "outfile"
echo.
