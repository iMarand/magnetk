"""magnetk links.

    magnetk:?xt=urn:sha256:<hash>&dn=<name>&xl=<size>&cs=<chunk size>
            &mr=<manifest root>&xs=<provider node>&relay=<host:port>[&relay=...]
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import List
from urllib.parse import parse_qs, quote

DEFAULT_RELAY_PORT = 4002


def normalize_relay(addr: str) -> str:
    """Accept host, host:port, quic://host:port or a Magnetk v1 multiaddr."""
    s = addr.strip()
    for prefix in ("quic://", "tcp://", "magnetk://"):
        if s.startswith(prefix):
            s = s[len(prefix):]
    if not s:
        return ""
    if s.startswith("/"):  # /ip4/HOST/tcp/PORT/p2p/ID (v1)
        parts = s.split("/")
        return "%s:%d" % (parts[2], DEFAULT_RELAY_PORT) if len(parts) > 2 else ""
    if s.startswith("["):
        return s if "]:" in s else "%s:%d" % (s, DEFAULT_RELAY_PORT)
    if s.count(":") == 1:
        host, port = s.split(":")
        if port == "4003":  # v1 legacy-only port
            return "%s:%d" % (host, DEFAULT_RELAY_PORT)
        return s
    return "%s:%d" % (s, DEFAULT_RELAY_PORT)


@dataclass
class Link:
    file_hash: str
    file_name: str = ""
    file_size: int = 0
    chunk_size: int = 0
    manifest_root: str = ""
    seed_id: str = ""
    relays: List[str] = field(default_factory=list)
    dht: bool = False

    @staticmethod
    def parse(uri: str) -> "Link":
        uri = uri.strip()
        if not uri.startswith("magnetk:?"):
            raise ValueError("not a magnetk link (must start with magnetk:?)")
        q = parse_qs(uri[len("magnetk:?"):], keep_blank_values=True)

        def first(key: str, default: str = "") -> str:
            values = q.get(key)
            return values[0] if values else default

        xt = first("xt")
        if not xt.startswith("urn:sha256:"):
            raise ValueError("magnetk link needs xt=urn:sha256:<hash>")
        h = xt[len("urn:sha256:"):].lower()
        if len(h) != 64 or any(c not in "0123456789abcdef" for c in h):
            raise ValueError("file hash must be 64 hex characters")
        relays: List[str] = []
        for r in q.get("relay", []):
            for one in r.split(","):
                n = normalize_relay(one)
                if n and n not in relays:
                    relays.append(n)
        return Link(
            file_hash=h, file_name=first("dn"), file_size=int(first("xl", "0") or 0),
            chunk_size=int(first("cs", "0") or 0), manifest_root=first("mr").lower(),
            seed_id=first("xs"), relays=relays, dht=first("dht").lower() in ("1", "true"))

    @property
    def relay_addr(self) -> str:
        return self.relays[0] if self.relays else ""

    def __str__(self) -> str:
        parts = ["xt=urn:sha256:" + self.file_hash]
        if self.file_name:
            parts.append("dn=" + quote(self.file_name, safe=""))
        if self.file_size:
            parts.append("xl=%d" % self.file_size)
        if self.chunk_size:
            parts.append("cs=%d" % self.chunk_size)
        if self.manifest_root:
            parts.append("mr=" + self.manifest_root)
        if self.seed_id:
            parts.append("xs=" + quote(self.seed_id, safe=""))
        for r in self.relays:
            parts.append("relay=" + quote(r, safe=""))
        if self.dht:
            parts.append("dht=true")
        return "magnetk:?" + "&".join(parts)


MagnetkLink = Link  # v1 name
