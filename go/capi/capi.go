// Package main builds libmagnetk, the C ABI of Magnetk used by the Python and
// C++ SDKs (and any language with a C FFI).
//
//	go build -buildmode=c-shared -o libmagnetk.dll ./capi     (Windows)
//	go build -buildmode=c-shared -o libmagnetk.so  ./capi     (Linux)
//
// The authoritative header is sdk/c/include/magnetk.h. Conventions:
//   - every object is an opaque uint64 handle (0 = invalid);
//   - functions return 0 (MK_OK) or a negative MK_ERR_* code; if err is
//     not NULL it receives a malloc'd message (free with mk_free);
//   - strings/buffers returned by the library are freed with mk_free;
//   - blocking calls take timeout_ms (-1 = wait forever) and may be called
//     from any thread; nothing ever calls back into the host language.
package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct mk_message {
    int32_t  kind;      // 1 direct, 2 publish, 3 request, 4 presence
    uint64_t id;
    char*    from;
    char*    subject;
    void*    data;
    int32_t  len;
    int32_t  datagram;
    uint64_t request;   // non-zero for requests: answer with mk_respond
} mk_message;
*/
import "C"

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"strings"
	"time"
	"unsafe"

	"github.com/iMarand/magnetk/go/pkg/config"
	"github.com/iMarand/magnetk/go/pkg/files"
	"github.com/iMarand/magnetk/go/pkg/netx"
	"github.com/iMarand/magnetk/go/pkg/node"
	"github.com/iMarand/magnetk/go/pkg/relay"
	"github.com/iMarand/magnetk/go/pkg/transport"
)

func main() {}

// Resource limits for the host application (Python, C++, ...) come from the
// environment: MAGNETK_MAX_PROCS (CPU cores), MAGNETK_MEMORY_LIMIT_MB (soft
// heap limit) and MAGNETK_GC_PERCENT.
func init() {
	if f, err := config.Load(""); err == nil {
		f.ApplyRuntime()
	}
}

const libVersion = "2.0.0"

// ===== helpers =====

func setErr(errOut **C.char, err error) C.int {
	if err == nil {
		return mkOK
	}
	if errOut != nil {
		*errOut = C.CString(err.Error())
	}
	return C.int(codeOf(err))
}

func invalid(errOut **C.char, msg string) C.int {
	if errOut != nil {
		*errOut = C.CString(msg)
	}
	return mkErrInvalid
}

func ctxFor(timeoutMs C.int) (context.Context, context.CancelFunc) {
	if timeoutMs < 0 {
		return context.WithCancel(context.Background())
	}
	return context.WithTimeout(context.Background(), time.Duration(timeoutMs)*time.Millisecond)
}

func goBytes(p unsafe.Pointer, n C.int) []byte {
	if p == nil || n <= 0 {
		return nil
	}
	return C.GoBytes(p, n)
}

func cJSON(v any) *C.char {
	b, err := json.Marshal(v)
	if err != nil {
		return C.CString("{}")
	}
	return C.CString(string(b))
}

func getNode(h C.uint64_t, errOut **C.char) (*nodeHandle, bool) {
	nh, ok := nodes.get(uint64(h))
	if !ok {
		invalid(errOut, "invalid node handle")
	}
	return nh, ok
}

// ===== library =====

var versionStr = C.CString(libVersion) // static: never freed

//export mk_version
func mk_version() *C.char { return versionStr }

//export mk_quic_available
func mk_quic_available() C.int {
	if transport.Available() {
		return 1
	}
	return 0
}

//export mk_free
func mk_free(p unsafe.Pointer) { C.free(p) }

// ===== node =====

type nodeJSON struct {
	ID               string   `json:"id"`
	Token            string   `json:"token"`
	Meta             string   `json:"meta"`
	Relays           []string `json:"relays"`
	Transport        string   `json:"transport"`
	Route            string   `json:"route"`
	Listen           bool     `json:"listen"`
	ListenAddr       string   `json:"listen_addr"`
	Advertise        []string `json:"advertise"`
	UPnP             bool     `json:"upnp"`
	VerifyPeer       bool     `json:"verify_peer"`
	CAFile           string   `json:"ca_file"`
	CertFile         string   `json:"cert_file"`
	KeyFile          string   `json:"key_file"`
	WatchPresence    bool     `json:"watch_presence"`
	InboxSize        int      `json:"inbox_size"`
	SubBuffer        int      `json:"sub_buffer"`
	SlowWaitMs       int      `json:"slow_consumer_wait_ms"`
	RequestTimeoutMs int      `json:"request_timeout_ms"`
	HeartbeatMs      int      `json:"heartbeat_ms"`
	ChunkSize        int64    `json:"chunk_size"`
	StreamsPerSource int      `json:"streams_per_source"`
	CacheDir         string   `json:"cache_dir"`
	LinkRelays       []string `json:"link_relays"`
	ConnectTimeoutMs int      `json:"connect_timeout_ms"`
	Congestion       string   `json:"congestion"`
	Profile          string   `json:"profile"`
	Log              bool     `json:"log"`
}

//export mk_node_open
func mk_node_open(configJSON *C.char, out *C.uint64_t, errOut **C.char) C.int {
	if out == nil {
		return invalid(errOut, "out is NULL")
	}
	*out = 0
	var j nodeJSON
	if configJSON != nil {
		if err := json.Unmarshal([]byte(C.GoString(configJSON)), &j); err != nil {
			return invalid(errOut, "config JSON: "+err.Error())
		}
	}
	if len(j.Relays) == 0 {
		if env := os.Getenv("MAGNETK_RELAYS"); env != "" {
			j.Relays = strings.Split(env, ",")
		}
	}
	cfg := node.Config{
		ID: j.ID, Token: j.Token, Meta: j.Meta, Relays: j.Relays,
		Transport: netx.ParseMode(j.Transport), Route: node.Route(j.Route),
		Listen: j.Listen, ListenAddr: j.ListenAddr, AdvertiseAddrs: j.Advertise, UPnP: j.UPnP,
		VerifyPeer: j.VerifyPeer, CAFile: j.CAFile, CertFile: j.CertFile, KeyFile: j.KeyFile,
		WatchPresence: j.WatchPresence, InboxSize: j.InboxSize, SubBuffer: j.SubBuffer,
		SlowConsumerWait: time.Duration(j.SlowWaitMs) * time.Millisecond,
		RequestTimeout:   time.Duration(j.RequestTimeoutMs) * time.Millisecond,
		ChunkSize:        j.ChunkSize, StreamsPerSource: j.StreamsPerSource, CacheDir: j.CacheDir,
		LinkRelays: j.LinkRelays,
		QUIC:       transport.Config{Congestion: transport.ParseCongestion(j.Congestion), Profile: transport.ParseProfile(j.Profile)},
	}
	if j.HeartbeatMs > 0 {
		cfg.HeartbeatInterval = time.Duration(j.HeartbeatMs) * time.Millisecond
	}
	if j.Log {
		cfg.Logger = log.New(os.Stderr, "[magnetk] ", log.LstdFlags)
	}
	n, err := node.New(cfg)
	if err != nil {
		return setErr(errOut, err)
	}
	timeout := 15 * time.Second
	if j.ConnectTimeoutMs > 0 {
		timeout = time.Duration(j.ConnectTimeoutMs) * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := n.Start(ctx); err != nil {
		n.Close()
		return setErr(errOut, err)
	}
	*out = C.uint64_t(nodes.add(&nodeHandle{n: n, requests: map[uint64]*node.Message{}}))
	return mkOK
}

//export mk_node_close
func mk_node_close(h C.uint64_t) C.int {
	nh, ok := nodes.remove(uint64(h))
	if !ok {
		return mkErrInvalid
	}
	nh.n.Close()
	return mkOK
}

//export mk_node_id
func mk_node_id(h C.uint64_t) *C.char {
	nh, ok := nodes.get(uint64(h))
	if !ok {
		return nil
	}
	return C.CString(nh.n.ID())
}

//export mk_node_info
func mk_node_info(h C.uint64_t) *C.char {
	nh, ok := nodes.get(uint64(h))
	if !ok {
		return nil
	}
	in := nh.n.Info()
	st := nh.n.Stats()
	return cJSON(map[string]any{
		"node_id": in.NodeID, "connected": in.Connected, "relay": in.Relay, "relay_id": in.RelayID,
		"protocol": in.Protocol, "observed": in.Observed, "rtt_us": in.RTT.Microseconds(), "direct": in.Direct,
		"sent": st.Sent, "received": st.Received, "dropped": st.Dropped, "reconnects": st.Reconnects,
		"served_bytes": st.ServedBytes, "shared_files": st.SharedFiles,
	})
}

//export mk_node_wait_connected
func mk_node_wait_connected(h C.uint64_t, timeoutMs C.int, errOut **C.char) C.int {
	nh, ok := getNode(h, errOut)
	if !ok {
		return mkErrInvalid
	}
	ctx, cancel := ctxFor(timeoutMs)
	defer cancel()
	return setErr(errOut, nh.n.WaitConnected(ctx))
}

// ===== messaging =====

const (
	flagReceipt    = 1
	flagStore      = 2
	flagUnreliable = 4
)

func sendOpts(flags C.int) []node.SendOption {
	var o []node.SendOption
	if flags&flagReceipt != 0 {
		o = append(o, node.WithReceipt())
	}
	if flags&flagStore != 0 {
		o = append(o, node.WithStore())
	}
	if flags&flagUnreliable != 0 {
		o = append(o, node.Unreliable())
	}
	return o
}

//export mk_send
func mk_send(h C.uint64_t, to *C.char, data unsafe.Pointer, n C.int, flags C.int, timeoutMs C.int, errOut **C.char) C.int {
	nh, ok := getNode(h, errOut)
	if !ok {
		return mkErrInvalid
	}
	ctx, cancel := ctxFor(timeoutMs)
	defer cancel()
	return setErr(errOut, nh.n.Send(ctx, C.GoString(to), goBytes(data, n), sendOpts(flags)...))
}

//export mk_publish
func mk_publish(h C.uint64_t, topic *C.char, data unsafe.Pointer, n C.int, flags C.int, errOut **C.char) C.int {
	nh, ok := getNode(h, errOut)
	if !ok {
		return mkErrInvalid
	}
	ctx, cancel := ctxFor(30000)
	defer cancel()
	return setErr(errOut, nh.n.Publish(ctx, C.GoString(topic), goBytes(data, n), sendOpts(flags)...))
}

//export mk_subscribe
func mk_subscribe(h C.uint64_t, pattern *C.char, queue *C.char, errOut **C.char) C.int {
	nh, ok := getNode(h, errOut)
	if !ok {
		return mkErrInvalid
	}
	ctx, cancel := ctxFor(30000)
	defer cancel()
	q := ""
	if queue != nil {
		q = C.GoString(queue)
	}
	_, err := nh.n.QueueSubscribe(ctx, C.GoString(pattern), q, nil)
	return setErr(errOut, err)
}

//export mk_serve
func mk_serve(h C.uint64_t, topic *C.char, errOut **C.char) C.int {
	nh, ok := getNode(h, errOut)
	if !ok {
		return mkErrInvalid
	}
	ctx, cancel := ctxFor(30000)
	defer cancel()
	t := C.GoString(topic)
	_, err := nh.n.QueueSubscribe(ctx, t, "svc:"+t, nil) // requests arrive through mk_recv
	return setErr(errOut, err)
}

//export mk_unsubscribe
func mk_unsubscribe(h C.uint64_t, pattern *C.char, errOut **C.char) C.int {
	nh, ok := getNode(h, errOut)
	if !ok {
		return mkErrInvalid
	}
	return setErr(errOut, nh.n.UnsubscribePattern(context.Background(), C.GoString(pattern)))
}

//export mk_request
func mk_request(h C.uint64_t, to *C.char, topic *C.char, data unsafe.Pointer, n C.int, timeoutMs C.int, out *unsafe.Pointer, outLen *C.int, errOut **C.char) C.int {
	nh, ok := getNode(h, errOut)
	if !ok {
		return mkErrInvalid
	}
	ctx, cancel := ctxFor(timeoutMs)
	defer cancel()
	var resp []byte
	var err error
	if to != nil && C.GoString(to) != "" {
		resp, err = nh.n.Request(ctx, C.GoString(to), goBytes(data, n))
	} else if topic != nil {
		resp, err = nh.n.RequestTopic(ctx, C.GoString(topic), goBytes(data, n))
	} else {
		return invalid(errOut, "request needs a node id or a topic")
	}
	if err != nil {
		return setErr(errOut, err)
	}
	if out != nil {
		if len(resp) > 0 {
			*out = C.CBytes(resp)
		} else {
			*out = nil
		}
	}
	if outLen != nil {
		*outLen = C.int(len(resp))
	}
	return mkOK
}

//export mk_recv
func mk_recv(h C.uint64_t, timeoutMs C.int, out **C.mk_message, errOut **C.char) C.int {
	nh, ok := getNode(h, errOut)
	if !ok {
		return mkErrInvalid
	}
	if out == nil {
		return invalid(errOut, "out is NULL")
	}
	*out = nil
	ctx, cancel := ctxFor(timeoutMs)
	defer cancel()
	m, err := nh.n.Receive(ctx)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return 0
		}
		return setErr(errOut, err)
	}
	cm := (*C.mk_message)(C.calloc(1, C.size_t(unsafe.Sizeof(C.mk_message{}))))
	cm.kind = C.int32_t(m.Kind)
	cm.id = C.uint64_t(m.ID)
	cm.from = C.CString(m.From)
	cm.subject = C.CString(m.Subject)
	if len(m.Data) > 0 {
		cm.data = C.CBytes(m.Data)
	}
	cm.len = C.int32_t(len(m.Data))
	if m.Datagram {
		cm.datagram = 1
	}
	if m.Kind == node.KindRequest {
		nh.mu.Lock()
		nh.nextReq++
		id := nh.nextReq
		nh.requests[id] = m
		nh.mu.Unlock()
		cm.request = C.uint64_t(id)
	}
	*out = cm
	return 1
}

//export mk_message_free
func mk_message_free(m *C.mk_message) {
	if m == nil {
		return
	}
	C.free(unsafe.Pointer(m.from))
	C.free(unsafe.Pointer(m.subject))
	C.free(m.data)
	C.free(unsafe.Pointer(m))
}

//export mk_respond
func mk_respond(h C.uint64_t, request C.uint64_t, data unsafe.Pointer, n C.int, isError C.int, errOut **C.char) C.int {
	nh, ok := getNode(h, errOut)
	if !ok {
		return mkErrInvalid
	}
	nh.mu.Lock()
	m := nh.requests[uint64(request)]
	delete(nh.requests, uint64(request))
	nh.mu.Unlock()
	if m == nil {
		return invalid(errOut, "unknown or already answered request")
	}
	if isError != 0 {
		return setErr(errOut, m.RespondError(string(goBytes(data, n))))
	}
	return setErr(errOut, m.Respond(goBytes(data, n)))
}

//export mk_watch_presence
func mk_watch_presence(h C.uint64_t, on C.int, errOut **C.char) C.int {
	nh, ok := getNode(h, errOut)
	if !ok {
		return mkErrInvalid
	}
	return setErr(errOut, nh.n.WatchPresence(context.Background(), on != 0))
}

//export mk_ping
func mk_ping(h C.uint64_t, timeoutMs C.int, outMicros *C.int64_t, errOut **C.char) C.int {
	nh, ok := getNode(h, errOut)
	if !ok {
		return mkErrInvalid
	}
	ctx, cancel := ctxFor(timeoutMs)
	defer cancel()
	d, err := nh.n.Ping(ctx)
	if err != nil {
		return setErr(errOut, err)
	}
	if outMicros != nil {
		*outMicros = C.int64_t(d.Microseconds())
	}
	return mkOK
}

//export mk_list_nodes
func mk_list_nodes(h C.uint64_t, prefix *C.char, errOut **C.char) *C.char {
	nh, ok := getNode(h, errOut)
	if !ok {
		return nil
	}
	p := ""
	if prefix != nil {
		p = C.GoString(prefix)
	}
	list, err := nh.n.ListNodes(context.Background(), p)
	if err != nil {
		setErr(errOut, err)
		return nil
	}
	out := make([]map[string]any, 0, len(list))
	for _, ni := range list {
		out = append(out, map[string]any{"node_id": ni.NodeID, "relay": ni.Relay, "observed": ni.Observed, "addrs": ni.Addrs, "meta": ni.Meta})
	}
	return cJSON(out)
}

//export mk_relay_stats
func mk_relay_stats(h C.uint64_t, errOut **C.char) *C.char {
	nh, ok := getNode(h, errOut)
	if !ok {
		return nil
	}
	js, err := nh.n.RelayStats(context.Background())
	if err != nil {
		setErr(errOut, err)
		return nil
	}
	return C.CString(string(js))
}

// ===== pipes =====

//export mk_pipe_open
func mk_pipe_open(h C.uint64_t, to, service, meta, route *C.char, timeoutMs C.int, out *C.uint64_t, errOut **C.char) C.int {
	nh, ok := getNode(h, errOut)
	if !ok {
		return mkErrInvalid
	}
	if out == nil {
		return invalid(errOut, "out is NULL")
	}
	ctx, cancel := ctxFor(timeoutMs)
	defer cancel()
	var opts []node.PipeOption
	if meta != nil {
		opts = append(opts, node.WithMeta(C.GoString(meta)))
	}
	if route != nil && C.GoString(route) != "" {
		opts = append(opts, node.WithRoute(node.Route(C.GoString(route))))
	}
	p, err := nh.n.OpenPipe(ctx, C.GoString(to), C.GoString(service), opts...)
	if err != nil {
		return setErr(errOut, err)
	}
	*out = C.uint64_t(pipes.add(p))
	return mkOK
}

//export mk_pipe_accept
func mk_pipe_accept(h C.uint64_t, timeoutMs C.int, out *C.uint64_t, errOut **C.char) C.int {
	nh, ok := getNode(h, errOut)
	if !ok {
		return mkErrInvalid
	}
	ctx, cancel := ctxFor(timeoutMs)
	defer cancel()
	p, err := nh.n.AcceptPipe(ctx)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return 0
		}
		return setErr(errOut, err)
	}
	*out = C.uint64_t(pipes.add(p))
	return 1
}

//export mk_pipe_info
func mk_pipe_info(p C.uint64_t) *C.char {
	pp, ok := pipes.get(uint64(p))
	if !ok {
		return nil
	}
	return cJSON(map[string]any{"peer": pp.Peer, "service": pp.Service, "meta": pp.Meta, "direct": pp.Direct})
}

// mk_pipe_read returns bytes read (>0), 0 at end of stream, or an error code.
//
//export mk_pipe_read
func mk_pipe_read(p C.uint64_t, buf unsafe.Pointer, n C.int, timeoutMs C.int, errOut **C.char) C.int {
	pp, ok := pipes.get(uint64(p))
	if !ok {
		return invalid(errOut, "invalid pipe handle")
	}
	if n <= 0 || buf == nil {
		return invalid(errOut, "empty buffer")
	}
	if timeoutMs >= 0 {
		pp.SetReadDeadline(time.Now().Add(time.Duration(timeoutMs) * time.Millisecond))
	} else {
		pp.SetReadDeadline(time.Time{})
	}
	dst := unsafe.Slice((*byte)(buf), int(n))
	got, err := pp.Read(dst)
	if got > 0 {
		return C.int(got)
	}
	if err == io.EOF {
		return 0
	}
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return mkErrTimeout
	}
	if err == nil {
		return 0
	}
	return setErr(errOut, err)
}

//export mk_pipe_write
func mk_pipe_write(p C.uint64_t, buf unsafe.Pointer, n C.int, timeoutMs C.int, errOut **C.char) C.int {
	pp, ok := pipes.get(uint64(p))
	if !ok {
		return invalid(errOut, "invalid pipe handle")
	}
	if n <= 0 {
		return 0
	}
	if timeoutMs >= 0 {
		pp.SetWriteDeadline(time.Now().Add(time.Duration(timeoutMs) * time.Millisecond))
	} else {
		pp.SetWriteDeadline(time.Time{})
	}
	written, err := pp.Write(unsafe.Slice((*byte)(buf), int(n)))
	if err != nil {
		if errors.Is(err, os.ErrDeadlineExceeded) {
			if written > 0 {
				return C.int(written)
			}
			return mkErrTimeout
		}
		return setErr(errOut, err)
	}
	return C.int(written)
}

//export mk_pipe_close_write
func mk_pipe_close_write(p C.uint64_t) C.int {
	pp, ok := pipes.get(uint64(p))
	if !ok {
		return mkErrInvalid
	}
	if pp.CloseWrite() != nil {
		return mkErr
	}
	return mkOK
}

//export mk_pipe_close
func mk_pipe_close(p C.uint64_t) C.int {
	pp, ok := pipes.remove(uint64(p))
	if !ok {
		return mkErrInvalid
	}
	pp.Close()
	return mkOK
}

// ===== files =====

func sharedJSON(sf *node.SharedFile) *C.char {
	return cJSON(map[string]any{"hash": sf.Hash, "name": sf.Name, "size": sf.Size, "chunk_size": sf.ChunkSize, "root": sf.Root, "path": sf.Path, "link": sf.Link})
}

//export mk_share
func mk_share(h C.uint64_t, path *C.char, chunkSize C.int64_t, outJSON **C.char, errOut **C.char) C.int {
	nh, ok := getNode(h, errOut)
	if !ok {
		return mkErrInvalid
	}
	sf, err := nh.n.Share(context.Background(), C.GoString(path), &node.ShareOptions{ChunkSize: int64(chunkSize)})
	if err != nil {
		return setErr(errOut, err)
	}
	if outJSON != nil {
		*outJSON = sharedJSON(sf)
	}
	return mkOK
}

//export mk_unshare
func mk_unshare(h C.uint64_t, hash *C.char, errOut **C.char) C.int {
	nh, ok := getNode(h, errOut)
	if !ok {
		return mkErrInvalid
	}
	return setErr(errOut, nh.n.Unshare(context.Background(), C.GoString(hash)))
}

//export mk_find_providers
func mk_find_providers(h C.uint64_t, hash *C.char, errOut **C.char) *C.char {
	nh, ok := getNode(h, errOut)
	if !ok {
		return nil
	}
	p, err := nh.n.FindProviders(context.Background(), C.GoString(hash))
	if err != nil {
		setErr(errOut, err)
		return nil
	}
	ids := make([]string, 0, len(p.Nodes))
	for _, ni := range p.Nodes {
		ids = append(ids, ni.NodeID)
	}
	return cJSON(map[string]any{"hash": p.Hash, "name": p.Name, "size": p.Size, "chunk_size": p.ChunkSize, "root": p.Root, "providers": ids})
}

type downloadJSON struct {
	Route            string `json:"route"`
	Seed             bool   `json:"seed"`
	StreamsPerSource int    `json:"streams_per_source"`
	NoResume         bool   `json:"no_resume"`
	MaxSources       int    `json:"max_sources"`
}

//export mk_download_start
func mk_download_start(h C.uint64_t, link, out, optsJSON *C.char, dl *C.uint64_t, errOut **C.char) C.int {
	nh, ok := getNode(h, errOut)
	if !ok {
		return mkErrInvalid
	}
	if dl == nil {
		return invalid(errOut, "dl is NULL")
	}
	var o downloadJSON
	if optsJSON != nil {
		if s := C.GoString(optsJSON); s != "" {
			if err := json.Unmarshal([]byte(s), &o); err != nil {
				return invalid(errOut, "options JSON: "+err.Error())
			}
		}
	}
	outPath := ""
	if out != nil {
		outPath = C.GoString(out)
	}
	ctx, cancel := context.WithCancel(context.Background())
	d := &downloadHandle{cancel: cancel, done: make(chan struct{})}
	id := downloads.add(d)
	target := C.GoString(link)
	go func() {
		defer close(d.done)
		res, err := nh.n.Download(ctx, target, outPath, &node.DownloadOptions{
			Route: node.Route(o.Route), Seed: o.Seed, StreamsPerSource: o.StreamsPerSource, NoResume: o.NoResume, MaxSources: o.MaxSources,
			Progress: func(p files.Progress) {
				d.mu.Lock()
				d.progress = p
				d.mu.Unlock()
			},
		})
		d.mu.Lock()
		d.result, d.err = res, err
		if res != nil {
			d.progress.Done = res.Manifest.Size
			d.progress.Size = res.Manifest.Size
		}
		d.mu.Unlock()
	}()
	*dl = C.uint64_t(id)
	return mkOK
}

// mk_download_poll waits up to timeout_ms and reports progress as JSON.
// Returns 0 while running, 1 when finished successfully, or an error code.
//
//export mk_download_poll
func mk_download_poll(dl C.uint64_t, timeoutMs C.int, progressJSON **C.char, errOut **C.char) C.int {
	d, ok := downloads.get(uint64(dl))
	if !ok {
		return invalid(errOut, "invalid download handle")
	}
	finished := false
	if timeoutMs < 0 {
		<-d.done
		finished = true
	} else {
		select {
		case <-d.done:
			finished = true
		case <-time.After(time.Duration(timeoutMs) * time.Millisecond):
		}
	}
	d.mu.Lock()
	p, res, derr := d.progress, d.result, d.err
	d.mu.Unlock()
	if progressJSON != nil {
		m := map[string]any{"name": p.Name, "size": p.Size, "done": p.Done, "percent": p.Percent(), "bytes_per_sec": p.BytesPerSec,
			"eta_ms": p.ETA.Milliseconds(), "sources": p.Sources, "streams": p.Streams, "finished": finished}
		if res != nil {
			m["path"] = res.Path
			m["hash"] = res.Manifest.Hash
			m["bytes"] = res.Bytes
			m["resumed"] = res.Resumed
			m["elapsed_ms"] = res.Elapsed.Milliseconds()
		}
		*progressJSON = cJSON(m)
	}
	if !finished {
		return 0
	}
	if derr != nil {
		return setErr(errOut, derr)
	}
	return 1
}

//export mk_download_cancel
func mk_download_cancel(dl C.uint64_t) C.int {
	d, ok := downloads.get(uint64(dl))
	if !ok {
		return mkErrInvalid
	}
	d.cancel()
	return mkOK
}

//export mk_download_free
func mk_download_free(dl C.uint64_t) {
	if d, ok := downloads.remove(uint64(dl)); ok {
		d.cancel()
		<-d.done
	}
}

// ===== embedded relay =====

type relayJSON struct {
	ID            string   `json:"id"`
	Listen        string   `json:"listen"`
	LegacyListen  string   `json:"legacy_listen"`
	Transport     string   `json:"transport"`
	Tokens        []string `json:"tokens"`
	Peers         []string `json:"peers"`
	ClusterSecret string   `json:"cluster_secret"`
	CertFile      string   `json:"cert_file"`
	KeyFile       string   `json:"key_file"`
	Log           bool     `json:"log"`
}

//export mk_relay_start
func mk_relay_start(configJSON *C.char, out *C.uint64_t, errOut **C.char) C.int {
	if out == nil {
		return invalid(errOut, "out is NULL")
	}
	var j relayJSON
	if configJSON != nil {
		if err := json.Unmarshal([]byte(C.GoString(configJSON)), &j); err != nil {
			return invalid(errOut, "config JSON: "+err.Error())
		}
	}
	cfg := relay.Config{ID: j.ID, Listen: j.Listen, LegacyListen: j.LegacyListen, Mode: netx.ParseMode(j.Transport),
		AuthTokens: j.Tokens, Peers: j.Peers, ClusterSecret: j.ClusterSecret, CertFile: j.CertFile, KeyFile: j.KeyFile}
	if j.Log {
		cfg.Logger = log.New(os.Stderr, "[magnetk-relay] ", log.LstdFlags)
	}
	s, err := relay.New(cfg)
	if err != nil {
		return setErr(errOut, err)
	}
	if err := s.Start(); err != nil {
		return setErr(errOut, err)
	}
	*out = C.uint64_t(relays.add(s))
	return mkOK
}

//export mk_relay_port
func mk_relay_port(r C.uint64_t) C.int {
	s, ok := relays.get(uint64(r))
	if !ok {
		return mkErrInvalid
	}
	return C.int(s.Port())
}

//export mk_relay_stats_json
func mk_relay_stats_json(r C.uint64_t) *C.char {
	s, ok := relays.get(uint64(r))
	if !ok {
		return nil
	}
	return cJSON(s.Stats())
}

//export mk_relay_close
func mk_relay_close(r C.uint64_t) C.int {
	s, ok := relays.remove(uint64(r))
	if !ok {
		return mkErrInvalid
	}
	s.Close()
	return mkOK
}
