package wire

import (
	"bufio"
	"bytes"
	"reflect"
	"testing"
)

func readBack(t *testing.T, frame []byte) (uint8, []byte) {
	t.Helper()
	typ, body, err := ReadFrame(bufio.NewReader(bytes.NewReader(frame)), DefaultMaxFrame)
	if err != nil {
		t.Fatal(err)
	}
	return typ, body
}

func TestRoundTrips(t *testing.T) {
	h := Hello{Version: 2, NodeID: "alice", Token: "secret", Flags: HelloWatchPresence, Meta: `{"a":1}`, Client: "go/test", Addrs: []string{"10.0.0.1:4002", "[::1]:4002"}}
	typ, body := readBack(t, h.Encode())
	h2, err := DecodeHello(body)
	if typ != THello || err != nil || !reflect.DeepEqual(h, h2) {
		t.Fatalf("hello: %v %+v", err, h2)
	}

	s := Send{ID: 7, From: "", To: "bob", Flags: FlagAck | FlagStore, Payload: []byte("hi\x00there")}
	_, body = readBack(t, s.Encode())
	s2, err := DecodeSend(body)
	if err != nil || s2.ID != 7 || s2.To != "bob" || s2.Flags != s.Flags || string(s2.Payload) != string(s.Payload) {
		t.Fatalf("send: %v %+v", err, s2)
	}

	dl := Deliver{ID: 9, Kind: KindRequest, From: "a", Subject: "svc.echo", ReqID: 42, Flags: 1, Payload: []byte{}}
	_, body = readBack(t, dl.Encode())
	dl2, err := DecodeDeliver(body)
	if err != nil || dl2.ReqID != 42 || dl2.Subject != "svc.echo" || len(dl2.Payload) != 0 {
		t.Fatalf("deliver: %v %+v", err, dl2)
	}

	r := Req{ID: 1, To: "", Topic: "svc.add", TimeoutMs: 500, Payload: []byte("1+1")}
	_, body = readBack(t, r.Encode())
	r2, err := DecodeReq(body)
	if err != nil || r2.Topic != "svc.add" || r2.TimeoutMs != 500 {
		t.Fatalf("req: %v %+v", err, r2)
	}

	fp := FileProviders{ID: 3, Hash: "ab", Name: "f.bin", Size: 1 << 40, ChunkSize: 1 << 20, Root: "cd",
		Providers: []NodeInfo{{NodeID: "n1", Addrs: []string{"1.2.3.4:5"}, Observed: "9.9.9.9:1", Relay: "r1"}}}
	_, body = readBack(t, fp.Encode())
	fp2, err := DecodeFileProviders(body)
	if err != nil || !reflect.DeepEqual(fp, fp2) {
		t.Fatalf("providers: %v %+v", err, fp2)
	}

	m := ManifestRes{Hash: "h", Name: "n", Size: 10, ChunkSize: 4, ChunkHashes: [][32]byte{{1}, {2}, {3}}}
	_, body = readBack(t, m.Encode())
	m2, err := DecodeManifestRes(body)
	if err != nil || !reflect.DeepEqual(m, m2) {
		t.Fatalf("manifest: %v", err)
	}

	cr := ChunkReq{Hash: "h", Indices: []uint32{0, 5, 99}}
	_, body = readBack(t, cr.Encode())
	cr2, err := DecodeChunkReq(body)
	if err != nil || !reflect.DeepEqual(cr, cr2) {
		t.Fatalf("chunkreq: %v", err)
	}

	hdr := ChunkDataHeader(77, 3)
	frame := append(append([]byte{}, hdr...), 'x', 'y', 'z')
	typ, body = readBack(t, frame)
	if typ != TChunkData || len(body) != 7 || body[3] != 77 || string(body[4:]) != "xyz" {
		t.Fatalf("chunk data header broken: %v", body)
	}
}

func TestMalformedAndLimits(t *testing.T) {
	if _, err := DecodeSend([]byte{0, 1}); err == nil {
		t.Fatal("short body must fail")
	}
	big := NewEnc(TSend, 0).Raw(make([]byte, 100)).Frame()
	if _, _, err := ReadFrame(bufio.NewReader(bytes.NewReader(big)), 50); err == nil {
		t.Fatal("frame limit not enforced")
	}
}

func TestTopics(t *testing.T) {
	cases := []struct {
		p, t string
		ok   bool
	}{
		{"a.b.c", "a.b.c", true},
		{"a.*.c", "a.b.c", true},
		{"a.*", "a.b.c", false},
		{"a.>", "a.b.c", true},
		{"a.>", "a", false},
		{">", "x", true},
		{"*", "x", true},
		{"*", "x.y", false},
		{"a.b", "a.b.c", false},
		{"a.b.c", "a.b", false},
		{"*.b.>", "x.b.y.z", true},
	}
	for _, c := range cases {
		if got := MatchTopic(c.p, c.t); got != c.ok {
			t.Errorf("MatchTopic(%q,%q)=%v want %v", c.p, c.t, got, c.ok)
		}
	}
	if !ValidPattern("a.>") || ValidPattern("a.>.b") || ValidTopic("a.*") || !ValidTopic("a.b") {
		t.Fatal("validation")
	}
	if !IsWildcard("a.*") || !IsWildcard(">") || IsWildcard("a.b") {
		t.Fatal("IsWildcard")
	}
}
