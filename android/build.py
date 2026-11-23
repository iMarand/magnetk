#!/usr/bin/env python3
"""Build the Magnetk field-test APK without Gradle or Android Studio.

Needs: Go, a JDK (javac, keytool), and the Android SDK with build-tools,
a platform (API >= 26) and the NDK. Paths are found from ANDROID_HOME /
ANDROID_SDK_ROOT / ANDROID_NDK_HOME / JAVA_HOME or PATH.

    python android/build.py                       # -> android/build/magnetk-test.apk
    python android/build.py --relay 192.168.1.10:4002 --token secret
    python android/build.py --install             # also adb install -r

Steps: go build (c-shared, per ABI) -> javac -> d8 -> aapt2 compile/link ->
add dex + native libs -> zipalign -> apksigner.
"""
import argparse
import glob
import os
import shutil
import struct
import subprocess
import sys
import zipfile
import zlib

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(HERE)
GO_DIR = os.path.join(ROOT, "go")
BUILD = os.path.join(HERE, "build")
WIN = os.name == "nt"

ABIS = {
    "arm64-v8a": ("arm64", "", "aarch64-linux-android"),
    "armeabi-v7a": ("arm", "7", "armv7a-linux-androideabi"),
    "x86_64": ("amd64", "", "x86_64-linux-android"),
}


def die(msg):
    sys.exit("build.py: " + msg)


def run(cmd, **kw):
    print("+", " ".join(str(c) for c in cmd))
    subprocess.run([str(c) for c in cmd], check=True, **kw)


def version_key(name):
    out = []
    for part in name.replace("-", ".").split("."):
        out.append(int(part) if part.isdigit() else 0)
    return out


def find_sdk():
    for env in ("ANDROID_HOME", "ANDROID_SDK_ROOT"):
        p = os.environ.get(env)
        if p and os.path.isdir(p):
            return p
    for p in ("D:/AndroidSDK", os.path.expanduser("~/Android/Sdk"), os.path.expanduser("~/Library/Android/sdk"),
              os.path.join(os.environ.get("LOCALAPPDATA", ""), "Android", "Sdk")):
        if os.path.isdir(p):
            return p
    die("Android SDK not found: set ANDROID_HOME")


def latest(dirpath, minimum=None):
    if not os.path.isdir(dirpath):
        return None
    names = [n for n in os.listdir(dirpath)
             if os.path.isdir(os.path.join(dirpath, n)) and n.replace("android-", "")[:1].isdigit()]
    if minimum is not None:
        names = [n for n in names if version_key(n.replace("android-", ""))[0] >= minimum]
    if not names:
        return None
    return os.path.join(dirpath, sorted(names, key=lambda n: version_key(n.replace("android-", "")))[-1])


def find_ndk(sdk):
    p = os.environ.get("ANDROID_NDK_HOME") or os.environ.get("ANDROID_NDK_ROOT")
    if p and os.path.isdir(p):
        return p
    p = latest(os.path.join(sdk, "ndk"))
    if p:
        return p
    die("Android NDK not found: install it with sdkmanager \"ndk;28.2.13676358\" or set ANDROID_NDK_HOME")


def tool(dirpath, name):
    for ext in ([".exe", ".bat", ".cmd", ""] if WIN else [""]):
        p = os.path.join(dirpath, name + ext)
        if os.path.isfile(p):
            return p
    die(f"{name} not found in {dirpath}")


def jdk_tool(name):
    jh = os.environ.get("JAVA_HOME")
    if jh:
        p = os.path.join(jh, "bin", name + (".exe" if WIN else ""))
        if os.path.isfile(p):
            return p
    p = shutil.which(name)
    if not p:
        die(f"{name} not found: install a JDK (17+) or set JAVA_HOME")
    return p


# ---------------------------------------------------------------- icon

def png(width, height, pixel):
    raw = bytearray()
    for y in range(height):
        raw.append(0)
        for x in range(width):
            raw.extend(pixel(x, y))
    def chunk(tag, data):
        c = struct.pack(">I", len(data)) + tag + data
        return c + struct.pack(">I", zlib.crc32(tag + data) & 0xFFFFFFFF)
    return (b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", struct.pack(">IIBBBBB", width, height, 8, 6, 0, 0, 0))
            + chunk(b"IDAT", zlib.compress(bytes(raw), 9)) + chunk(b"IEND", b""))


def icon(size):
    """A horseshoe magnet on a rounded blue square."""
    s = size
    cx, cy = s / 2, s * 0.50
    r_out, r_in = s * 0.30, s * 0.16
    top = s * 0.20
    radius = s * 0.22

    def pixel(x, y):
        px, py = x + 0.5, y + 0.5
        # rounded square mask
        dx = max(radius - px, 0, px - (s - radius))
        dy = max(radius - py, 0, py - (s - radius))
        if dx * dx + dy * dy > radius * radius:
            return (0, 0, 0, 0)
        bg = (30, 58, 138, 255)
        d = ((px - cx) ** 2 + (py - cy) ** 2) ** 0.5
        in_arc = py >= cy and r_in <= d <= r_out
        in_legs = top <= py < cy and (cx - r_out <= px <= cx - r_in or cx + r_in <= px <= cx + r_out)
        if in_legs and py < top + s * 0.12:
            return (239, 68, 68, 255)  # red tips
        if in_arc or in_legs:
            return (241, 245, 249, 255)
        return bg
    return png(s, s, pixel)


# ---------------------------------------------------------------- build

def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--relay", default="", help="default relay address shown in the app")
    ap.add_argument("--token", default="", help="default relay token shown in the app")
    ap.add_argument("--abis", default="arm64-v8a", help="comma separated: arm64-v8a,armeabi-v7a,x86_64")
    ap.add_argument("--api", type=int, default=26, help="minimum Android API for the native code")
    ap.add_argument("--out", default=os.path.join(BUILD, "magnetk-test.apk"))
    ap.add_argument("--install", action="store_true", help="adb install -r the APK")
    ap.add_argument("--skip-go", action="store_true", help="reuse previously built native libraries")
    args = ap.parse_args()

    sdk = find_sdk()
    ndk = find_ndk(sdk)
    bt = latest(os.path.join(sdk, "build-tools"))
    platform = latest(os.path.join(sdk, "platforms"), minimum=26)
    if not bt or not platform:
        die("need build-tools and a platform >= android-26 (sdkmanager \"build-tools;35.0.0\" \"platforms;android-35\")")
    android_jar = os.path.join(platform, "android.jar")
    print(f"SDK {sdk}\nNDK {ndk}\nbuild-tools {bt}\nplatform {platform}")
    aapt2, d8, zipalign, apksigner = tool(bt, "aapt2"), tool(bt, "d8"), tool(bt, "zipalign"), tool(bt, "apksigner")
    javac, keytool = jdk_tool("javac"), jdk_tool("keytool")

    os.makedirs(BUILD, exist_ok=True)

    # 1. native library per ABI
    host = {"win32": "windows-x86_64", "darwin": "darwin-x86_64"}.get(sys.platform, "linux-x86_64")
    clang = os.path.join(ndk, "toolchains", "llvm", "prebuilt", host, "bin", "clang" + (".exe" if WIN else ""))
    libdir = os.path.join(BUILD, "lib")
    for abi in [a.strip() for a in args.abis.split(",") if a.strip()]:
        if abi not in ABIS:
            die(f"unknown ABI {abi}")
        goarch, goarm, triple = ABIS[abi]
        out = os.path.join(libdir, abi, "libmagnetkdiag.so")
        if args.skip_go and os.path.isfile(out):
            continue
        os.makedirs(os.path.dirname(out), exist_ok=True)
        env = dict(os.environ, GOOS="android", GOARCH=goarch, CGO_ENABLED="1",
                   CC=f"{clang} --target={triple}{args.api}")
        if goarm:
            env["GOARM"] = goarm
        run(["go", "build", "-buildmode=c-shared", "-tags", "nomsquic", "-trimpath", "-ldflags=-s -w", "-o", out, "./mobile"],
            cwd=GO_DIR, env=env)
        h = out[:-3] + ".h"
        if os.path.exists(h):
            os.remove(h)

    # 2. resources (icon) + manifest
    res = os.path.join(BUILD, "res")
    for density, size in (("mipmap-mdpi", 48), ("mipmap-hdpi", 72), ("mipmap-xhdpi", 96), ("mipmap-xxhdpi", 144), ("mipmap-xxxhdpi", 192)):
        d = os.path.join(res, density)
        os.makedirs(d, exist_ok=True)
        with open(os.path.join(d, "ic_launcher.png"), "wb") as f:
            f.write(icon(size))
    flat = os.path.join(BUILD, "res.zip")
    run([aapt2, "compile", "--dir", res, "-o", flat])
    base = os.path.join(BUILD, "base.apk")
    run([aapt2, "link", "-o", base, "-I", android_jar, "--manifest", os.path.join(HERE, "AndroidManifest.xml"),
         "--min-sdk-version", "26", "--target-sdk-version", "34", flat])

    # 3. Java -> dex
    gen = os.path.join(BUILD, "gen", "com", "magnetk", "diag")
    os.makedirs(gen, exist_ok=True)
    with open(os.path.join(gen, "BuildConfig.java"), "w", encoding="utf-8") as f:
        f.write("package com.magnetk.diag;\n\n/** Generated by android/build.py. */\nfinal class BuildConfig {\n"
                f"    static final String DEFAULT_RELAY = {java_str(args.relay)};\n"
                f"    static final String DEFAULT_TOKEN = {java_str(args.token)};\n"
                "    private BuildConfig() {}\n}\n")
    classes = os.path.join(BUILD, "classes")
    shutil.rmtree(classes, ignore_errors=True)
    os.makedirs(classes)
    sources = glob.glob(os.path.join(HERE, "src", "**", "*.java"), recursive=True) + glob.glob(os.path.join(BUILD, "gen", "**", "*.java"), recursive=True)
    run([javac, "--release", "11", "-encoding", "UTF-8", "-Xlint:-options", "-classpath", android_jar, "-d", classes] + sources)
    dexdir = os.path.join(BUILD, "dex")
    shutil.rmtree(dexdir, ignore_errors=True)
    os.makedirs(dexdir)
    class_files = glob.glob(os.path.join(classes, "**", "*.class"), recursive=True)
    run([d8, "--release", "--min-api", "26", "--lib", android_jar, "--output", dexdir] + class_files)

    # 4. assemble, align, sign
    unsigned = os.path.join(BUILD, "unsigned.apk")
    shutil.copy(base, unsigned)
    with zipfile.ZipFile(unsigned, "a", zipfile.ZIP_DEFLATED) as z:
        z.write(os.path.join(dexdir, "classes.dex"), "classes.dex")
        for so in glob.glob(os.path.join(libdir, "*", "*.so")):
            abi = os.path.basename(os.path.dirname(so))
            if abi in args.abis.split(","):
                z.write(so, f"lib/{abi}/{os.path.basename(so)}")
    aligned = os.path.join(BUILD, "aligned.apk")
    run([zipalign, "-f", "-p", "4", unsigned, aligned])

    ks = os.path.join(HERE, "debug.keystore")
    if not os.path.exists(ks):
        run([keytool, "-genkeypair", "-keystore", ks, "-storepass", "android", "-keypass", "android", "-alias", "magnetk",
             "-keyalg", "RSA", "-keysize", "2048", "-validity", "10000", "-dname", "CN=Magnetk Test,O=Magnetk"])
    os.makedirs(os.path.dirname(os.path.abspath(args.out)), exist_ok=True)
    run([apksigner, "sign", "--ks", ks, "--ks-pass", "pass:android", "--key-pass", "pass:android", "--ks-key-alias", "magnetk",
         "--out", args.out, aligned])
    run([apksigner, "verify", args.out])
    print(f"\nAPK: {args.out} ({os.path.getsize(args.out) / 1e6:.1f} MB)")

    if args.install:
        adb = tool(os.path.join(sdk, "platform-tools"), "adb")
        run([adb, "install", "-r", args.out])


def java_str(s):
    return '"' + s.replace("\\", "\\\\").replace('"', '\\"') + '"'


if __name__ == "__main__":
    main()
