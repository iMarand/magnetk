package magnetk

import (
	"strings"
	"testing"
)

const h = "693ac0474a5927de6567d68c26c9d932dd39d7f7124c1b44c22bd5b619338fca"

func TestRoundTrip(t *testing.T) {
	ml := &MagnetLink{FileHash: h, FileName: "my file.bin", FileSize: 123, ChunkSize: 1 << 20,
		ManifestRoot: strings.Repeat("ab", 32), SeedID: "node-1", Relays: []string{"1.2.3.4:4002", "relay.example.com:4002"}}
	got, err := Parse(ml.String())
	if err != nil {
		t.Fatal(err)
	}
	if got.FileName != "my file.bin" || got.FileSize != 123 || got.ChunkSize != 1<<20 || got.ManifestRoot != ml.ManifestRoot ||
		len(got.Relays) != 2 || got.Relays[1] != "relay.example.com:4002" || got.RelayAddr != "1.2.3.4:4002" || got.SeedID != "node-1" {
		t.Fatalf("round trip: %+v", got)
	}
}

func TestLegacyLink(t *testing.T) {
	// produced by the v1 seed binary
	v1 := "magnetk:?dn=data200.bin&relay=%2Fip4%2F127.0.0.1%2Ftcp%2F24001%2Fp2p%2F12D3KooWPwUvPh6drUqSTQXxfa5Y9jGMeoQ1bMBksxTNHprYcWct&xl=209715200&xs=12D3KooWE5eu&xt=urn%3Asha256%3A" + h
	ml, err := Parse(v1)
	if err != nil {
		t.Fatal(err)
	}
	if ml.RelayAddr != "127.0.0.1:4002" || ml.FileSize != 209715200 || ml.FileHash != h {
		t.Fatalf("legacy parse: %+v", ml)
	}
}

func TestNormalizeRelay(t *testing.T) {
	cases := map[string]string{
		"1.2.3.4":                     "1.2.3.4:4002",
		"1.2.3.4:5000":                "1.2.3.4:5000",
		"1.2.3.4:4003":                "1.2.3.4:4002",
		"quic://host:9":               "host:9",
		"/ip4/9.9.9.9/tcp/4001/p2p/X": "9.9.9.9:4002",
		"[::1]:4002":                  "[::1]:4002",
		"":                            "",
	}
	for in, want := range cases {
		if got := NormalizeRelay(in); got != want {
			t.Errorf("NormalizeRelay(%q)=%q want %q", in, got, want)
		}
	}
}

func TestRejectsBadLinks(t *testing.T) {
	for _, bad := range []string{"magnet:?xt=urn:sha256:" + h, "magnetk:?xt=urn:sha1:abc", "magnetk:?xt=urn:sha256:xyz", "magnetk:?xt=urn:sha256:" + h + "&mr=zz"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("expected error for %q", bad)
		}
	}
}
