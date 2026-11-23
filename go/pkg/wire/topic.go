package wire

import "strings"

// ValidTopic reports whether t is a usable publish topic: non-empty,
// dot-separated tokens, no wildcards, no empty tokens, max 256 bytes.
func ValidTopic(t string) bool {
	if t == "" || len(t) > 256 {
		return false
	}
	for _, tok := range strings.Split(t, ".") {
		if tok == "" || tok == "*" || tok == ">" {
			return false
		}
	}
	return true
}

// ValidPattern reports whether p is a usable subscription pattern.
// "*" matches exactly one token, ">" (last token only) matches one or more.
func ValidPattern(p string) bool {
	if p == "" || len(p) > 256 {
		return false
	}
	toks := strings.Split(p, ".")
	for i, tok := range toks {
		if tok == "" {
			return false
		}
		if tok == ">" && i != len(toks)-1 {
			return false
		}
	}
	return true
}

// IsWildcard reports whether a pattern contains wildcard tokens.
func IsWildcard(p string) bool {
	return p == "*" || p == ">" ||
		strings.HasPrefix(p, "*.") || strings.HasPrefix(p, ">.") ||
		strings.Contains(p, ".*.") || strings.Contains(p, ".>") ||
		strings.HasSuffix(p, ".*")
}

// MatchTopic reports whether topic matches pattern.
func MatchTopic(pattern, topic string) bool {
	if pattern == topic {
		return true
	}
	for {
		var ptok, ttok string
		pi := strings.IndexByte(pattern, '.')
		if pi < 0 {
			ptok, pattern = pattern, ""
		} else {
			ptok, pattern = pattern[:pi], pattern[pi+1:]
		}
		if ptok == ">" {
			return topic != "" || ttok != ""
		}
		if topic == "" {
			return false
		}
		ti := strings.IndexByte(topic, '.')
		if ti < 0 {
			ttok, topic = topic, ""
		} else {
			ttok, topic = topic[:ti], topic[ti+1:]
		}
		if ptok != "*" && ptok != ttok {
			return false
		}
		if pattern == "" && pi < 0 {
			return topic == "" && ti < 0
		}
	}
}
