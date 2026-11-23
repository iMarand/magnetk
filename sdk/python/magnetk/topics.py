"""Topic matching, identical to the relay: "*" = one token, ">" = the rest."""

from __future__ import annotations


def valid_topic(topic: str) -> bool:
    """A publish topic: dot-separated, non-empty tokens, no wildcards."""
    if not topic or len(topic) > 256:
        return False
    return all(tok and tok not in ("*", ">") for tok in topic.split("."))


def valid_pattern(pattern: str) -> bool:
    """A subscription pattern: "*" anywhere, ">" only as the last token."""
    if not pattern or len(pattern) > 256:
        return False
    toks = pattern.split(".")
    return all(toks) and all(t != ">" or i == len(toks) - 1 for i, t in enumerate(toks))


def match_topic(pattern: str, topic: str) -> bool:
    if pattern == topic:
        return True
    p, t = pattern.split("."), topic.split(".")
    for i, tok in enumerate(p):
        if tok == ">":
            return len(t) > i
        if i >= len(t) or (tok != "*" and tok != t[i]):
            return False
    return len(p) == len(t)
