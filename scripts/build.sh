#!/bin/bash
# Magnetk P2P Build Script for Linux/macOS

echo "========================================"
echo " Magnetk P2P File Transfer System"
echo " Build Script"
echo "========================================"
echo

# Check for Go
if ! command -v go &> /dev/null; then
    echo "Error: Go is not installed or not in PATH"
    exit 1
fi

echo "Go version:"
go version
echo

# Create output directory
mkdir -p bin

# Build relay node
echo "Building relay node..."
cd go
if ! go build -o ../bin/relay ./cmd/relay; then
    echo "Build failed for relay"
    cd ..
    exit 1
fi

# Build seed node
echo "Building seed node..."
if ! go build -o ../bin/seed ./cmd/seed; then
    echo "Build failed for seed"
    cd ..
    exit 1
fi

# Build peer client
echo "Building peer client..."
if ! go build -o ../bin/peer ./cmd/peer; then
    echo "Build failed for peer"
    cd ..
    exit 1
fi

cd ..

echo
echo "========================================"
echo " Build Complete!"
echo "========================================"
echo
echo "Binaries are in the 'bin' directory:"
echo "  - bin/relay  (Relay Node)"
echo "  - bin/seed   (Seed Node)"
echo "  - bin/peer   (Peer Client)"
echo
echo "Usage:"
echo "  1. Start relay:  ./bin/relay --port 4001"
echo "  2. Start seed:   ./bin/seed --relay 'relay_addr' --file 'filepath'"
echo "  3. Download:     ./bin/peer --magnetk 'magnetk:?...' --output 'outfile'"
echo
