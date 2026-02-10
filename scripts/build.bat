@echo off
REM Magnetk P2P Build Script for Windows

echo ========================================
echo  Magnetk P2P File Transfer System
echo  Build Script
echo ========================================
echo.

REM Check for Go
where go >nul 2>nul
if %errorlevel% neq 0 (
    echo Error: Go is not installed or not in PATH
    exit /b 1
)

echo Go version:
go version
echo.

REM Create output directory
if not exist "bin" mkdir bin

REM Build relay node
echo Building relay node...
cd go
go build -o ../bin/relay.exe ./cmd/relay
if %errorlevel% neq 0 (
    echo Build failed for relay
    cd ..
    exit /b 1
)

REM Build seed node
echo Building seed node...
go build -o ../bin/seed.exe ./cmd/seed
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

cd ..

echo.
echo ========================================
echo  Build Complete!
echo ========================================
echo.
echo Binaries are in the 'bin' directory:
echo   - bin\relay.exe  (Relay Node)
echo   - bin\seed.exe   (Seed Node)
echo   - bin\peer.exe   (Peer Client)
echo.
echo Usage:
echo   1. Start relay:  bin\relay.exe --port 4001
echo   2. Start seed:   bin\seed.exe --relay "relay_addr" --file "filepath"
echo   3. Download:     bin\peer.exe --magnetk "magnetk:?..." --output "outfile"
echo.
