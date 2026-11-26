#!/usr/bin/env bash
# ==========================================================================
#  Magnetk v2 build (Linux / macOS)
#
#    scripts/build.sh            CLI + libmagnetk (C ABI for Python/C++/C)
#    scripts/build.sh msquic     also (re)build MsQuic (Release, OpenSSL)
#    scripts/build.sh android    also build the Android field-test APK
#    scripts/build.sh tcp        CLI without QUIC (no MsQuic needed, pure Go)
#    scripts/build.sh all        everything
#
#  Needs: Go 1.22+, a C compiler for cgo. MsQuic needs CMake, PowerShell
#  (pwsh) and perl/make for OpenSSL; or install libmsquic from
#  packages.microsoft.com and build with:  MSQUIC_LIB=/usr/lib/x86_64-linux-gnu
#  Output: build/bin, build/lib (also copied into sdk/python/magnetk/lib).
# ==========================================================================
set -euo pipefail
cd "$(dirname "$0")/.."
ROOT=$PWD
MODE=${1:-}

case "$(uname -s)" in
  Darwin) OS=macos; LIBEXT=dylib ;;
  *)      OS=linux; LIBEXT=so ;;
esac
MSQUIC_OUT="$ROOT/msquic/artifacts/bin/$OS/x64_Release_openssl"
mkdir -p build/bin build/lib

if [ "$MODE" = "tcp" ]; then
  echo "=== magnetk CLI (TCP/TLS only, no QUIC) ==="
  (cd go && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -tags nomsquic -o "$ROOT/build/bin/magnetk" ./cmd/magnetk)
  echo "Done: build/bin/magnetk (static, copy it to any $OS machine)"
  exit 0
fi

if [ "$MODE" = "msquic" ] || [ "$MODE" = "all" ] || { [ ! -f "$MSQUIC_OUT/libmsquic.$LIBEXT" ] && [ -z "${MSQUIC_LIB:-}" ]; }; then
  echo "=== MsQuic (Release, OpenSSL) ==="
  if [ ! -f msquic/scripts/build.ps1 ]; then
    echo "=== fetching MsQuic source (microsoft/msquic @ ${MSQUIC_REV:=6c284c8ab68efd5a152646746dec4697e17a842c}) ==="
    git clone https://github.com/microsoft/msquic.git msquic
    git -C msquic checkout -q "$MSQUIC_REV"
    git -C msquic submodule update --init --recursive
  fi
  pwsh -NoProfile -File msquic/scripts/build.ps1 -Config Release -Tls openssl -DisableTest -DisableTools
fi

export CGO_ENABLED=1
if [ -n "${MSQUIC_LIB:-}" ]; then
  export CGO_LDFLAGS="-L$MSQUIC_LIB ${CGO_LDFLAGS:-}"
  MSQUIC_OUT=$MSQUIC_LIB
fi

echo "=== magnetk CLI ==="
(cd go && go build -trimpath -ldflags="-s -w" -o "$ROOT/build/bin/magnetk" ./cmd/magnetk)
echo "=== libmagnetk (C ABI) ==="
(cd go && go build -trimpath -ldflags="-s -w" -buildmode=c-shared -o "$ROOT/build/lib/libmagnetk.$LIBEXT" ./capi)
rm -f build/lib/libmagnetk.h
for f in "$MSQUIC_OUT"/libmsquic.$LIBEXT*; do
  [ -e "$f" ] && cp -a "$f" build/bin/ && cp -a "$f" build/lib/
done
mkdir -p sdk/python/magnetk/lib
cp -a build/lib/* sdk/python/magnetk/lib/

if [ "$MODE" = "android" ] || [ "$MODE" = "all" ]; then
  echo "=== Android field-test APK ==="
  shift || true
  python3 android/build.py "$@"
fi

echo
echo "Done."
echo "  build/bin/magnetk           CLI: relay, share, get, send, pub, sub, req, bench, diag ..."
echo "  build/lib/libmagnetk.$LIBEXT  C ABI used by the Python and C++ SDKs"
echo "Quick start:"
echo "  ./build/bin/magnetk relay"
echo "  ./build/bin/magnetk bench -local all"
