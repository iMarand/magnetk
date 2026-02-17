#!/bin/bash
# Magnetk C Transport Build Script for Linux/macOS

echo "========================================"
echo " Building Magnetk C Transport Layer"
echo "========================================"
echo

# Navigate to project root/c
cd "$(dirname "$0")/../c" || exit 1

mkdir -p build
cd build || exit 1

echo "Configuring CMake..."
if ! cmake ..; then
    echo "CMake configuration failed."
    exit 1
fi

echo "Building..."
if ! cmake --build .; then
    echo "Build failed."
    exit 1
fi

echo
echo "========================================"
echo " C Transport Build Complete!"
echo "========================================"
echo
