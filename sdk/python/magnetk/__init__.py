"""Magnetk Python SDK: fast messaging, pipes and file distribution over QUIC.

    import magnetk

    node = magnetk.Node(relays=["relay.example.com:4002"], id="alice")
    node.send("bob", "hello")
    node.subscribe("chat.*", lambda m: print(m.sender, m.text))
    shared = node.share("big.iso")
    print(shared.link)
"""

from ._native import quic_available, version
from .errors import (
    ClosedError, DroppedError, InvalidError, MagnetkError, NoProvidersError, NoRouteError,
    NotConnectedError, RejectedError, RemoteError, TimeoutError, UnauthorizedError,
)
from .link import Link, MagnetkLink, normalize_relay
from .node import Download, DownloadResult, Message, Node, Pipe, Progress, SharedFile, Subscription
from .relay import Relay
from .topics import match_topic, valid_pattern, valid_topic

__version__ = "2.0.0"

__all__ = [
    "Node", "Relay", "Message", "Pipe", "Subscription", "SharedFile", "Download", "DownloadResult",
    "Progress", "Link", "MagnetkLink", "normalize_relay", "match_topic", "valid_topic", "valid_pattern",
    "version", "quic_available", "MagnetkError", "TimeoutError", "ClosedError", "NoRouteError",
    "UnauthorizedError", "NotConnectedError", "RemoteError", "RejectedError", "NoProvidersError",
    "InvalidError", "DroppedError",
]
