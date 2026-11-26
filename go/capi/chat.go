package main

/*
#include "mk_types.h"
*/
import "C"

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"github.com/iMarand/magnetk/go/pkg/chat"
	"github.com/iMarand/magnetk/go/pkg/identity"
	"github.com/iMarand/magnetk/go/pkg/magnetk"
	"github.com/iMarand/magnetk/go/pkg/node"
)

// kindChat is MK_KIND_CHAT: a secure chat message or ack decoded by the
// engine; data is the chat.Msg as JSON.
const kindChat = 5

func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, p[2:])
	}
	return p
}

// loadIdentity returns the key for the identity_file option.
func loadIdentity(path string) (*identity.Key, error) {
	if path == "" || path == "off" {
		return nil, nil
	}
	k, _, err := identity.LoadOrCreate(expandHome(path))
	return k, err
}

// setupChat creates the node's chat box (after Start, when the id is known).
func setupChat(nh *nodeHandle, j nodeJSON) {
	n := nh.n
	dir := j.ChatDir
	if dir == "" {
		dir = filepath.Join(os.TempDir(), "magnetk-chat")
	}
	pinsFile := j.KnownPeersFile
	if pinsFile == "" && j.IdentityFile != "" {
		pinsFile = filepath.Join(filepath.Dir(expandHome(j.IdentityFile)), "known-peers.json")
	}
	box := chat.NewBox(n.ID(), expandHome(dir), func() *node.Node { return n }, func(m chat.Msg) {
		nh.mu.Lock()
		nh.chatq = append(nh.chatq, m)
		nh.mu.Unlock()
	})
	if k := n.Key(); k != nil {
		box.SetIdentity(k, identity.OpenPins(expandHome(pinsFile)))
		box.Attach(n)
	}
	box.SetPolicy(chat.Policy{RequireApproval: j.ChatRequireApproval, RequireEncryption: j.ChatRequireEncryption})
	nh.chat = box
}

// popChat returns a queued chat event as an mk_message (nil if none).
func popChat(nh *nodeHandle) *C.mk_message {
	nh.mu.Lock()
	if len(nh.chatq) == 0 {
		nh.mu.Unlock()
		return nil
	}
	m := nh.chatq[0]
	nh.chatq = nh.chatq[1:]
	nh.mu.Unlock()
	b, _ := json.Marshal(m)
	cm := (*C.mk_message)(C.calloc(1, C.size_t(unsafe.Sizeof(C.mk_message{}))))
	cm.kind = kindChat
	cm.from = C.CString(m.From)
	cm.subject = C.CString(m.Kind)
	cm.data = C.CBytes(b)
	cm.len = C.int32_t(len(b))
	return cm
}

func jsonOut(out **C.char, v any) {
	if out == nil {
		return
	}
	b, _ := json.Marshal(v)
	*out = C.CString(string(b))
}

//export mk_identity
func mk_identity(h C.uint64_t, out **C.char, errOut **C.char) C.int {
	nh, ok := getNode(h, errOut)
	if !ok {
		return mkErrInvalid
	}
	k := nh.n.Key()
	if k == nil {
		return invalid(errOut, "this node has no identity key (set identity_file)")
	}
	fp := k.Fingerprint()
	addr := &magnetk.Address{Node: nh.n.ID(), Key: fp}
	if r := nh.n.Info().Relay; r != "" {
		addr.Relays = []string{r}
	}
	jsonOut(out, map[string]string{"fingerprint": fp, "formatted": identity.Format(fp), "address": addr.String()})
	return mkOK
}

//export mk_chat_send
func mk_chat_send(h C.uint64_t, to, text *C.char, att unsafe.Pointer, attLen C.int, mime *C.char, durMs C.int, out **C.char, errOut **C.char) C.int {
	nh, ok := getNode(h, errOut)
	if !ok {
		return mkErrInvalid
	}
	if nh.chat == nil {
		return invalid(errOut, "chat is off for this node (open it with \"chat\": true)")
	}
	var data []byte
	if att != nil && attLen > 0 {
		data = C.GoBytes(att, attLen)
	}
	dur := int(durMs)
	if dur <= 0 && len(data) > 0 {
		dur = chat.WAVDurationMs(data)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	m, err := nh.chat.Send(ctx, C.GoString(to), C.GoString(text), C.GoString(mime), dur, data)
	if err != nil {
		return setErr(errOut, err)
	}
	jsonOut(out, m)
	return mkOK
}

//export mk_chat_contact
func mk_chat_contact(h C.uint64_t, action, peer *C.char, out **C.char, errOut **C.char) C.int {
	nh, ok := getNode(h, errOut)
	if !ok {
		return mkErrInvalid
	}
	if nh.chat == nil {
		return invalid(errOut, "chat is off for this node")
	}
	p := C.GoString(peer)
	box := nh.chat
	switch C.GoString(action) {
	case "allow":
		box.Allow(p)
	case "block":
		box.Block(p)
	case "verified":
		box.MarkVerified(p)
	case "accept":
		if err := box.AcceptKey(p); err != nil {
			return setErr(errOut, err)
		}
	case "safety":
		n := box.SafetyNumber(p)
		if n == "" {
			return setErr(errOut, errors.New("no key known for "+p+" yet"))
		}
		jsonOut(out, map[string]string{"peer": p, "safety_number": n})
	case "contact":
		jsonOut(out, box.Contact(p))
	case "expect":
		// peer is "node=FINGERPRINT": the key a magnetk://...?key= address promised
		node, fp, _ := strings.Cut(p, "=")
		box.Pins().Pin(node, fp)
	default:
		return invalid(errOut, "action must be allow, block, verified, accept, safety, contact or expect")
	}
	return mkOK
}
