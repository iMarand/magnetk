"""Magnetk exceptions. Every error raised by the SDK derives from MagnetkError."""

from __future__ import annotations

import builtins


class MagnetkError(Exception):
    """Base class. `code` is the MK_ERR_* value from the C API."""

    code = -1

    def __init__(self, message: str = "", code: int | None = None):
        super().__init__(message or self.__class__.__name__)
        if code is not None:
            self.code = code


class TimeoutError(MagnetkError, builtins.TimeoutError):  # noqa: A001
    """The operation did not complete in time (also a built-in TimeoutError)."""
    code = -2


class ClosedError(MagnetkError):
    """The node or pipe is closed."""
    code = -3


class NoRouteError(MagnetkError):
    """The destination node is not connected to the network."""
    code = -4


class UnauthorizedError(MagnetkError):
    """The relay rejected the token."""
    code = -5


class NotConnectedError(MagnetkError, builtins.ConnectionError):
    """No relay connection, or it dropped while waiting (also a ConnectionError)."""
    code = -6


class RemoteError(MagnetkError):
    """The remote request handler returned an error."""
    code = -7


class RejectedError(MagnetkError):
    """The remote node refused the pipe."""
    code = -8


class NoProvidersError(MagnetkError):
    """Nobody shares the requested file."""
    code = -9


class InvalidError(MagnetkError):
    """Invalid argument or handle."""
    code = -10


class DroppedError(MagnetkError):
    """The receiver was too slow and the message was dropped."""
    code = -11


_BY_CODE = {cls.code: cls for cls in (
    TimeoutError, ClosedError, NoRouteError, UnauthorizedError, NotConnectedError,
    RemoteError, RejectedError, NoProvidersError, InvalidError, DroppedError,
)}


def raise_for(code: int, message: str) -> None:
    raise _BY_CODE.get(code, MagnetkError)(message, code)
