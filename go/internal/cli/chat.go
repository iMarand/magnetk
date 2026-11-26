package cli

import (
	"bufio"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/iMarand/magnetk/go/pkg/chat"
	"github.com/iMarand/magnetk/go/pkg/identity"
	"github.com/iMarand/magnetk/go/pkg/magnetk"
	"github.com/iMarand/magnetk/go/pkg/node"
)

// ChatMain implements "magnetk chat": text and voice notes with any node
// (phones running the Magnetk app, other "magnetk chat" sessions, the diag
// host), sent over the Magnetk network itself.
//
//	magnetk chat -relay host:4002 a52-3cc9 "hello"          send, wait for the ack
//	magnetk chat -relay host:4002 -audio note.wav a52-3cc9  send a voice note
//	magnetk chat -relay host:4002 a52-3cc9                  interactive
//	magnetk chat "magnetk://a52-3cc9@host:4002/chat?text=hi"
func ChatMain(args []string) error {
	nf := newNodeFlags("chat")
	audio := nf.fs.String("audio", "", "send this file as a voice note / attachment")
	file := nf.fs.String("file", "", "send any file (big files go through the encrypted file transfer)")
	mimeType := nf.fs.String("mime", "", "attachment type (default from the file extension)")
	interactive := nf.fs.Bool("i", false, "interactive: keep the chat open (default when no message is given)")
	wait := nf.fs.Duration("wait", 0, "after sending, keep printing replies for this long")
	dir := nf.fs.String("dir", "magnetk-chat", "where received attachments are saved")
	yes := nf.fs.Bool("y", false, "send a message that came from a magnetk:// link without asking")
	if err := nf.parse(args); err != nil {
		return err
	}
	if nf.fs.NArg() < 1 {
		return errors.New("usage: magnetk chat [flags] <node|magnetk://address> [message]")
	}
	target := nf.fs.Arg(0)
	text := strings.Join(nf.fs.Args()[1:], " ")
	fromLink := false
	expectKey := ""
	if magnetk.IsAddress(target) {
		a, err := magnetk.ParseAddress(target)
		if err != nil {
			return err
		}
		if a.Node == "" {
			return fmt.Errorf("%s names a relay, not a node to chat with", target)
		}
		target = a.Node
		if *nf.relays == "" && len(a.Relays) > 0 {
			*nf.relays = strings.Join(a.Relays, ",")
		}
		if *nf.token == "" && a.Token != "" {
			*nf.token = a.Token
		}
		if text == "" && a.Text != "" {
			text, fromLink = a.Text, true
		}
		expectKey = a.Key
	}
	if fromLink && !*yes {
		// A web page can open magnetk:// links: never send its text silently.
		fmt.Printf("The link wants to send to %s:\n  %q\nSend it? [y/N] ", target, text)
		if !confirm(os.Stdin) {
			text = ""
			*interactive = true
		}
	}

	if *nf.id == "" {
		*nf.id = defaultChatID(nf)
	}
	n, err := nf.start(nil)
	if err != nil {
		return err
	}
	defer n.Close()
	s := newChatSession(n, *dir)
	go s.run()
	me := &magnetk.Address{Node: n.ID(), Relays: []string{n.Info().Relay}}
	if k := n.Key(); k != nil {
		me.Key = k.Fingerprint()
	}
	fmt.Printf("chatting as %s via %s over %s\n  your address: %s\n", n.ID(), n.Info().Relay, n.Info().Protocol, me)
	if k := n.Key(); k != nil {
		fmt.Printf("  your key:     %s\n", identity.Format(k.Fingerprint()))
	}
	s.expect(target, expectKey)

	ctx := context.Background()
	sent := false
	if *file != "" && *audio == "" {
		*audio = *file
	}
	if *audio != "" {
		if err := s.sendFile(ctx, target, *audio, *mimeType, text); err != nil {
			return err
		}
		sent = true
	} else if text != "" {
		if err := s.sendText(ctx, target, text); err != nil {
			return err
		}
		sent = true
	}
	if *interactive || !sent {
		return s.interactive(ctx, target, os.Stdin)
	}
	s.waitAcks(10 * time.Second)
	if *wait > 0 {
		fmt.Printf("waiting %s for replies...\n", *wait)
		time.Sleep(*wait)
	}
	return nil
}

// defaultChatID is "<computer name>-<key prefix>": readable on the other
// side and stable per device (relays bind it to this device's key).
func defaultChatID(nf *nodeFlags) string {
	h, _ := os.Hostname()
	h = strings.ToLower(strings.Map(func(r rune) rune {
		if r == '-' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			return r
		}
		return '-'
	}, h))
	if h == "" {
		h = "chat"
	}
	if len(h) > 24 {
		h = h[:24]
	}
	suffix := ""
	if cfg, err := nf.nodeConfig(nil); err == nil && cfg.Key != nil {
		suffix = strings.ToLower(cfg.Key.Fingerprint()[:4])
	} else {
		b := make([]byte, 2)
		rand.Read(b)
		suffix = fmt.Sprintf("%x", b)
	}
	return strings.Trim(h, "-") + "-" + suffix
}

func confirm(r io.Reader) bool {
	line, _ := bufio.NewReader(r).ReadString('\n')
	line = strings.ToLower(strings.TrimSpace(line))
	return line == "y" || line == "yes"
}

// chatSession prints incoming chat and tracks acks of what it sent.
type chatSession struct {
	n   *node.Node
	box *chat.Box
	out sync.Mutex

	mu      sync.Mutex
	pending map[string]chan float64 // id -> rtt
}

func newChatSession(n *node.Node, dir string) *chatSession {
	s := &chatSession{n: n, pending: map[string]chan float64{}}
	s.box = chat.NewBox(n.ID(), dir, func() *node.Node { return n }, s.onMsg)
	s.box.SetIdentity(n.Key(), identity.OpenPins(filepath.Join(magnetkHome(), "known-peers.json")))
	s.box.SetPolicy(chat.Policy{RequireApproval: true})
	if n.Key() != nil {
		s.box.Attach(n)
	}
	return s
}

// expect pins the key a magnetk://...?key= address promised for node.
func (s *chatSession) expect(node, fp string) {
	if fp == "" {
		return
	}
	if prev := s.box.Pins().Pin(node, fp); prev != "" {
		s.printf("note: the address says %s has key %s; you saw %s before. Trusting the address.\n",
			node, identity.Short(identity.NormalizeFingerprint(fp)), identity.Short(prev))
	}
}

func trustNote(m chat.Msg) string {
	enc := "  [e2e"
	if !m.Encrypted {
		enc = "  [NOT encrypted"
	}
	note := ""
	switch identity.Trust(m.Trust) {
	case identity.TrustVerified, identity.TrustNew:
		note = enc + ", " + chat.TrustLabel(m) + "]"
	case identity.TrustUnsigned:
		note = enc + ", unsigned]"
	default:
		note = "\n  !!! " + chat.TrustLabel(m)
	}
	if m.Error != "" {
		note += "\n  !!! " + m.Error
	}
	if m.Request {
		note += "\n  -> message request from " + m.From + ": /allow " + m.From + " to accept (they get your ack), /block " + m.From + " to drop them, /safety " + m.From + " to compare the safety number"
	}
	return note
}

func (s *chatSession) run() {
	for m := range s.n.Messages() {
		if m.Kind == node.KindDirect && chat.IsChat(m.Data) {
			s.box.Handle(m.From, m.Data)
		}
	}
}

func (s *chatSession) printf(format string, args ...any) {
	s.out.Lock()
	fmt.Printf(format, args...)
	s.out.Unlock()
}

func (s *chatSession) onMsg(m chat.Msg) {
	ts := time.Now().Format("15:04:05")
	switch m.Kind {
	case chat.KindAck:
		s.mu.Lock()
		ch := s.pending[m.Text]
		delete(s.pending, m.Text)
		s.mu.Unlock()
		if ch != nil {
			ch <- m.RTTMs
		}
		if t := identity.Trust(m.Trust); t == identity.TrustInvalid || t == identity.TrustChanged {
			s.printf("%s   !!! ack from %s ignored: %s\n", ts, m.From, chat.TrustLabel(m))
			return
		}
		s.printf("%s   ✓ %s got it (round trip %.1f ms)\n", ts, m.From, m.RTTMs)
	case chat.KindFile:
		if m.Pending {
			s.printf("%s %s is sending %s (%s), receiving...%s\n", ts, m.From, m.Name, humanBytes(float64(m.Size)), trustNote(m))
			return
		}
		line := fmt.Sprintf("%s %s: [%s]", ts, m.From, chat.Summary(m))
		if m.Text != "" {
			line += " " + m.Text
		}
		line += trustNote(m)
		if m.Path != "" {
			line += "\n         saved to " + m.Path
		}
		s.printf("%s\n", line)
	case chat.KindAudio:
		line := fmt.Sprintf("%s %s: [%s]", ts, m.From, chat.Summary(m))
		if m.Text != "" {
			line += " " + m.Text
		}
		line += trustNote(m)
		if m.Path != "" {
			line += "\n         saved to " + m.Path
		}
		s.printf("%s\n", line)
	default:
		s.printf("%s %s: %s%s\n", ts, m.From, m.Text, trustNote(m))
	}
}

func (s *chatSession) track(id string) {
	s.mu.Lock()
	s.pending[id] = make(chan float64, 1)
	s.mu.Unlock()
}

func (s *chatSession) sendText(ctx context.Context, to, text string) error {
	m, err := s.box.Send(ctx, to, text, "", 0, nil)
	if err != nil {
		return fmt.Errorf("send to %s: %w", to, err)
	}
	s.track(m.ID)
	return nil
}

func (s *chatSession) sendFile(ctx context.Context, to, path, mimeType, caption string) error {
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	if mimeType == "" {
		mimeType = mimeFor(path)
	}
	var m chat.Msg
	if st.Size() > chat.InlineLimit {
		// streamed from disk: encrypted, shared, fetched by the receiver
		s.printf("sending %s (%s) through the encrypted file transfer...\n", filepath.Base(path), humanBytes(float64(st.Size())))
		m, err = s.box.SendFile(ctx, to, path, caption, mimeType, 0)
	} else {
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		m, err = s.box.Send(ctx, to, caption, mimeType, chat.WAVDurationMs(data), data)
	}
	if err != nil {
		return fmt.Errorf("send to %s: %w", to, err)
	}
	s.track(m.ID)
	s.printf("sent %s (%s)\n", filepath.Base(path), chat.Summary(m))
	return nil
}

func mimeFor(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".wav":
		return "audio/wav"
	case ".m4a", ".aac":
		return "audio/mp4"
	case ".opus", ".ogg":
		return "audio/ogg"
	case ".mp3":
		return "audio/mpeg"
	}
	if t := mime.TypeByExtension(ext); t != "" {
		return t
	}
	return "application/octet-stream"
}

// waitAcks waits until everything sent was acknowledged (or timeout).
func (s *chatSession) waitAcks(timeout time.Duration) {
	deadline := time.After(timeout)
	for {
		s.mu.Lock()
		var ch chan float64
		for _, c := range s.pending {
			ch = c
			break
		}
		left := len(s.pending)
		s.mu.Unlock()
		if left == 0 {
			return
		}
		select {
		case <-ch:
		case <-time.After(50 * time.Millisecond): // acked and removed meanwhile
		case <-deadline:
			s.printf("no ack after %s (the other side may be offline or an old app)\n", timeout)
			return
		}
	}
}

func (s *chatSession) interactive(ctx context.Context, to string, in io.Reader) error {
	s.printf("chat with %s: type a message and press Enter. /audio <file.wav> [caption], /to <node>, /quit\n", to)
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "":
		case line == "/quit" || line == "/exit":
			s.waitAcks(5 * time.Second)
			return nil
		case line == "/help":
			s.printf("/audio <file> [caption]   send a voice note\n/file <path> [caption]    send any file (end-to-end encrypted)\n/to <node|address>        change recipient (now %s)\n"+
				"/allow [node]             accept a message request\n/block [node]             drop everything from node\n"+
				"/safety [node]            show the safety number to compare with node\n/verified [node]          mark node as verified (after comparing)\n"+
				"/accept [node]            trust node's new key after KEY CHANGED\n/quit\n", to)
		case strings.HasPrefix(line, "/allow") || strings.HasPrefix(line, "/block") || strings.HasPrefix(line, "/safety") ||
			strings.HasPrefix(line, "/verified") || strings.HasPrefix(line, "/accept"):
			cmd, arg, _ := strings.Cut(line, " ")
			peer := strings.TrimSpace(arg)
			if peer == "" {
				peer = to
			}
			s.contactCommand(cmd, peer)
		case strings.HasPrefix(line, "/to "):
			to = strings.TrimSpace(strings.TrimPrefix(line, "/to "))
			if a, err := magnetk.ParseAddress(to); err == nil && a.Node != "" {
				to = a.Node
				s.expect(to, a.Key)
			}
			s.printf("now chatting with %s\n", to)
		case strings.HasPrefix(line, "/audio ") || strings.HasPrefix(line, "/file "):
			_, rest, _ := strings.Cut(line, " ")
			rest = strings.TrimSpace(rest)
			path, caption := splitPathCaption(rest)
			if err := s.sendFile(ctx, to, path, "", caption); err != nil {
				s.printf("! %v\n", err)
			}
		default:
			if err := s.sendText(ctx, to, line); err != nil {
				s.printf("! %v\n", err)
			}
		}
	}
	s.waitAcks(5 * time.Second)
	return sc.Err()
}

func (s *chatSession) contactCommand(cmd, peer string) {
	switch cmd {
	case "/allow":
		s.box.Allow(peer)
		s.printf("%s can message you now\n", peer)
	case "/block":
		s.box.Block(peer)
		s.printf("%s is blocked\n", peer)
	case "/safety":
		n := s.box.SafetyNumber(peer)
		if n == "" {
			s.printf("no key known for %s yet: exchange a message first\n", peer)
			return
		}
		s.printf("safety number with %s:\n  %s\ncompare it with what %s sees (read it aloud or side by side); if it matches: /verified %s\n", peer, n, peer, peer)
	case "/verified":
		if s.box.SafetyNumber(peer) == "" {
			s.printf("no key known for %s yet\n", peer)
			return
		}
		s.box.MarkVerified(peer)
		s.printf("%s marked verified\n", peer)
	case "/accept":
		if err := s.box.AcceptKey(peer); err != nil {
			s.printf("! %v\n", err)
			return
		}
		s.printf("accepted %s's new key (compare the safety number again: /safety %s)\n", peer, peer)
	}
}

// splitPathCaption splits `"C:\my file.wav" caption` or `file.wav caption`.
func splitPathCaption(s string) (string, string) {
	if strings.HasPrefix(s, `"`) {
		if i := strings.Index(s[1:], `"`); i >= 0 {
			return s[1 : i+1], strings.TrimSpace(s[i+2:])
		}
	}
	if i := strings.IndexByte(s, ' '); i >= 0 {
		if _, err := os.Stat(s); err == nil { // a path with spaces, no caption
			return s, ""
		}
		return s[:i], strings.TrimSpace(s[i+1:])
	}
	return s, ""
}
