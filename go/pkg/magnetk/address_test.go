package magnetk

import (
	"reflect"
	"testing"
)

func TestParseAddress(t *testing.T) {
	cases := []struct {
		in     string
		node   string
		relays []string
		action string
		text   string
	}{
		{"magnetk://a52-3cc9@192.168.1.66:4002", "a52-3cc9", []string{"192.168.1.66:4002"}, "", ""},
		{"magnetk://a52@vps.example.com/chat?text=hello%20there", "a52", []string{"vps.example.com:4002"}, "chat", "hello there"},
		{"magnetk://a52@/chat?text=hi", "a52", nil, "chat", "hi"},
		{"magnetk://relay.example.com:5000", "", []string{"relay.example.com:5000"}, "", ""},
		{"magnetk://n1@a:1?relay=b:2&relay=c", "n1", []string{"a:1", "b:2", "c:4002"}, "", ""},
		{"MAGNETK://n1@[::1]:4002/chat", "n1", []string{"[::1]:4002"}, "chat", ""},
	}
	for _, c := range cases {
		a, err := ParseAddress(c.in)
		if err != nil {
			t.Fatalf("%s: %v", c.in, err)
		}
		if a.Node != c.node || !reflect.DeepEqual(a.Relays, c.relays) || a.Action != c.action || a.Text != c.text {
			t.Fatalf("%s: got %+v", c.in, a)
		}
		// String round-trips
		b, err := ParseAddress(a.String())
		if err != nil || b.Node != a.Node || !reflect.DeepEqual(b.Relays, a.Relays) || b.Action != a.Action || b.Text != a.Text {
			t.Fatalf("%s: round trip %q -> %+v %v", c.in, a.String(), b, err)
		}
	}
	for _, bad := range []string{"", "http://x", "magnetk:?xt=urn:sha256:00", "magnetk://", "magnetk://relay/chat", "magnetk://n@h:99999"} {
		if _, err := ParseAddress(bad); err == nil {
			t.Fatalf("%q: expected an error", bad)
		}
	}
	if got := ChatAddress("a52", "hi you", "1.2.3.4:4002"); got != "magnetk://a52@1.2.3.4:4002/chat?text=hi+you" {
		t.Fatalf("ChatAddress = %s", got)
	}
	if got := NodeAddress("a52"); got != "magnetk://a52@" {
		t.Fatalf("NodeAddress without relay = %s", got)
	}
	if !IsLink("magnetk:?xt=1") || IsLink("magnetk://a@b") || !IsAddress("magnetk://a@b") {
		t.Fatal("IsLink/IsAddress")
	}
}
