"""Run a Magnetk relay inside a Python process (tests, small deployments).

For production, run the standalone `magnetk relay` binary on a VPS.
"""

from __future__ import annotations

import json
from ctypes import byref, c_uint64
from typing import Any, Dict, List, Optional

from . import _native as nat


class Relay:
    """An embedded relay.

    >>> relay = Relay(listen="127.0.0.1:0")
    >>> node = Node(relays=[relay.address])
    """

    def __init__(self, listen: str = "0.0.0.0:4002", *, tokens: Optional[List[str]] = None,
                 peers: Optional[List[str]] = None, cluster_secret: Optional[str] = None,
                 relay_id: Optional[str] = None, transport: str = "auto",
                 legacy_listen: Optional[str] = None, cert_file: Optional[str] = None,
                 key_file: Optional[str] = None, log: bool = False):
        cfg: Dict[str, Any] = {"listen": listen, "transport": transport, "log": log}
        if tokens:
            cfg["tokens"] = tokens
        if peers:
            cfg["peers"] = peers
        if cluster_secret:
            cfg["cluster_secret"] = cluster_secret
        if relay_id:
            cfg["id"] = relay_id
        if legacy_listen:
            cfg["legacy_listen"] = legacy_listen
        if cert_file:
            cfg["cert_file"], cfg["key_file"] = cert_file, key_file
        h = c_uint64()
        err = nat.ErrBox()
        err.check(nat.lib.mk_relay_start(json.dumps(cfg).encode(), byref(h), err.ref))
        self._h = h.value
        self._host = listen.rsplit(":", 1)[0]
        if self._host in ("0.0.0.0", "", "::", "[::]"):
            self._host = "127.0.0.1"

    @property
    def port(self) -> int:
        return nat.lib.mk_relay_port(self._h)

    @property
    def address(self) -> str:
        """host:port to give to nodes on this machine."""
        return "%s:%d" % (self._host, self.port)

    def stats(self) -> Dict[str, Any]:
        return nat.take_json(nat.lib.mk_relay_stats_json(self._h)) or {}

    def close(self) -> None:
        if self._h:
            nat.lib.mk_relay_close(self._h)
            self._h = 0

    def __enter__(self) -> "Relay":
        return self

    def __exit__(self, *exc) -> None:
        self.close()
