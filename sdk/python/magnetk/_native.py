"""ctypes binding of libmagnetk (the Magnetk C API, see sdk/c/include/magnetk.h)."""

from __future__ import annotations

import ctypes
import json
import os
import sys
from ctypes import (
    POINTER, Structure, byref, c_char_p, c_int, c_int32, c_int64, c_uint64, c_void_p,
)
from pathlib import Path
from typing import Any, Optional

from .errors import raise_for

if sys.platform == "win32":
    _LIB_NAMES = ["libmagnetk.dll", "magnetk.dll"]
elif sys.platform == "darwin":
    _LIB_NAMES = ["libmagnetk.dylib"]
else:
    _LIB_NAMES = ["libmagnetk.so"]


class MkMessage(Structure):
    _fields_ = [
        ("kind", c_int32),
        ("id", c_uint64),
        ("sender", c_void_p),
        ("subject", c_void_p),
        ("data", c_void_p),
        ("len", c_int32),
        ("datagram", c_int32),
        ("request", c_uint64),
    ]


def _candidate_dirs() -> list[Path]:
    here = Path(__file__).resolve().parent
    dirs = []
    env = os.environ.get("MAGNETK_LIB")
    if env:
        p = Path(env)
        dirs.append(p if p.is_dir() else p.parent)
    dirs.append(here / "lib")
    # running from a source checkout: <repo>/build/lib
    for parent in here.parents:
        cand = parent / "build" / "lib"
        if cand.is_dir():
            dirs.append(cand)
            break
    return dirs


def _load() -> ctypes.CDLL:
    env = os.environ.get("MAGNETK_LIB")
    tried = []
    if env and Path(env).is_file():
        candidates = [Path(env)]
    else:
        candidates = [d / name for d in _candidate_dirs() for name in _LIB_NAMES]
    for path in candidates:
        tried.append(str(path))
        if not path.is_file():
            continue
        if sys.platform == "win32" and hasattr(os, "add_dll_directory"):
            os.add_dll_directory(str(path.parent))  # lets msquic.dll resolve
        return ctypes.CDLL(str(path))
    for name in _LIB_NAMES:  # system loader path
        try:
            return ctypes.CDLL(name)
        except OSError:
            tried.append(name)
    raise OSError(
        "libmagnetk not found. Install a wheel that bundles it, set MAGNETK_LIB to its path, "
        "or build it with scripts/build (it lands in build/lib). Tried: " + ", ".join(tried)
    )


lib = _load()


def _sig(name: str, restype, *argtypes) -> None:
    fn = getattr(lib, name)
    fn.restype = restype
    fn.argtypes = list(argtypes)


ERR = POINTER(c_void_p)
_sig("mk_version", c_char_p)
_sig("mk_quic_available", c_int)
_sig("mk_free", None, c_void_p)
_sig("mk_node_open", c_int, c_char_p, POINTER(c_uint64), ERR)
_sig("mk_node_close", c_int, c_uint64)
_sig("mk_node_id", c_void_p, c_uint64)
_sig("mk_node_info", c_void_p, c_uint64)
_sig("mk_node_wait_connected", c_int, c_uint64, c_int, ERR)
_sig("mk_send", c_int, c_uint64, c_char_p, c_char_p, c_int, c_int, c_int, ERR)
_sig("mk_publish", c_int, c_uint64, c_char_p, c_char_p, c_int, c_int, ERR)
_sig("mk_subscribe", c_int, c_uint64, c_char_p, c_char_p, ERR)
_sig("mk_unsubscribe", c_int, c_uint64, c_char_p, ERR)
_sig("mk_serve", c_int, c_uint64, c_char_p, ERR)
_sig("mk_request", c_int, c_uint64, c_char_p, c_char_p, c_char_p, c_int, c_int, POINTER(c_void_p), POINTER(c_int), ERR)
_sig("mk_recv", c_int, c_uint64, c_int, POINTER(POINTER(MkMessage)), ERR)
_sig("mk_message_free", None, POINTER(MkMessage))
_sig("mk_respond", c_int, c_uint64, c_uint64, c_char_p, c_int, c_int, ERR)
_sig("mk_watch_presence", c_int, c_uint64, c_int, ERR)
_sig("mk_ping", c_int, c_uint64, c_int, POINTER(c_int64), ERR)
_sig("mk_list_nodes", c_void_p, c_uint64, c_char_p, ERR)
_sig("mk_relay_stats", c_void_p, c_uint64, ERR)
_sig("mk_pipe_open", c_int, c_uint64, c_char_p, c_char_p, c_char_p, c_char_p, c_int, POINTER(c_uint64), ERR)
_sig("mk_pipe_accept", c_int, c_uint64, c_int, POINTER(c_uint64), ERR)
_sig("mk_pipe_info", c_void_p, c_uint64)
_sig("mk_pipe_read", c_int, c_uint64, c_void_p, c_int, c_int, ERR)
_sig("mk_pipe_write", c_int, c_uint64, c_char_p, c_int, c_int, ERR)
_sig("mk_pipe_close_write", c_int, c_uint64)
_sig("mk_pipe_close", c_int, c_uint64)
_sig("mk_share", c_int, c_uint64, c_char_p, c_int64, POINTER(c_void_p), ERR)
_sig("mk_unshare", c_int, c_uint64, c_char_p, ERR)
_sig("mk_find_providers", c_void_p, c_uint64, c_char_p, ERR)
_sig("mk_download_start", c_int, c_uint64, c_char_p, c_char_p, c_char_p, POINTER(c_uint64), ERR)
_sig("mk_download_poll", c_int, c_uint64, c_int, POINTER(c_void_p), ERR)
_sig("mk_download_cancel", c_int, c_uint64)
_sig("mk_download_free", None, c_uint64)
_sig("mk_relay_start", c_int, c_char_p, POINTER(c_uint64), ERR)
_sig("mk_relay_port", c_int, c_uint64)
_sig("mk_relay_stats_json", c_void_p, c_uint64)
_sig("mk_relay_close", c_int, c_uint64)


# ---------- helpers ----------

def take_string(ptr: Optional[int]) -> Optional[str]:
    """Copy and free a library-allocated C string."""
    if not ptr:
        return None
    try:
        return ctypes.string_at(ptr).decode("utf-8", "replace")
    finally:
        lib.mk_free(ptr)


def take_json(ptr: Optional[int]) -> Any:
    s = take_string(ptr)
    return json.loads(s) if s else None


def take_bytes(ptr: Optional[int], n: int) -> bytes:
    if not ptr:
        return b""
    try:
        return ctypes.string_at(ptr, n)
    finally:
        lib.mk_free(ptr)


class ErrBox:
    """Holds the `char** err` out-parameter of one call."""

    __slots__ = ("p",)

    def __init__(self) -> None:
        self.p = c_void_p()

    @property
    def ref(self):
        return byref(self.p)

    def message(self) -> str:
        return take_string(self.p.value) or ""

    def check(self, rc: int) -> int:
        """Raise the matching MagnetkError for a negative status."""
        if rc < 0:
            raise_for(rc, self.message())
        if self.p.value:
            lib.mk_free(self.p.value)
        return rc


def b(s: Optional[str]) -> Optional[bytes]:
    return None if s is None else s.encode("utf-8")


def data_arg(data: Any) -> bytes:
    if data is None:
        return b""
    if isinstance(data, str):
        return data.encode("utf-8")
    if isinstance(data, (bytes, bytearray, memoryview)):
        return bytes(data)
    raise TypeError("data must be bytes or str, got %s" % type(data).__name__)


def version() -> str:
    return lib.mk_version().decode()


def quic_available() -> bool:
    return bool(lib.mk_quic_available())
