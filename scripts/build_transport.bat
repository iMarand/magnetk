@echo off
REM Magnetk C Transport Build Script

echo ========================================
echo  Building Magnetk C Transport Layer
echo ========================================

REM Add MinGW to PATH (same as build.bat)
set "Path=C:\msys64\mingw64\bin;%Path%"

cd /d "%~dp0..\c"
if not exist "build" mkdir build
cd build

echo Configuring CMake...
cmake -G "MinGW Makefiles" ..
if %errorlevel% neq 0 (
    echo CMake configuration failed.
    exit /b 1
)

echo Building...
cmake --build .
if %errorlevel% neq 0 (
    echo Build failed.
    exit /b 1
)

echo.
echo ========================================
echo  C Transport Build Complete!
echo ========================================
echo.
