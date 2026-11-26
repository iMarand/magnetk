package wire

// Version is the protocol version carried in HELLO/WELCOME.
const Version uint8 = 2

// Frame types. v1 types (0x01-0x41, 0xFF) are defined in package protocol.
const (
	// Session control (control stream)
	THello   uint8 = 0x50
	TWelcome uint8 = 0x51
	TError   uint8 = 0x52
	TPing    uint8 = 0x53
	TPong    uint8 = 0x54
	TBye     uint8 = 0x55

	// Messaging
	TSend      uint8 = 0x60 // node -> relay: direct message
	TDeliver   uint8 = 0x61 // relay -> node: incoming message/request
	TAck       uint8 = 0x62 // relay -> node: delivery report
	TPub       uint8 = 0x63 // node -> relay: publish to topic
	TSub       uint8 = 0x64
	TUnsub     uint8 = 0x65
	TReq       uint8 = 0x66 // node -> relay: request (to node or to topic)
	TReply     uint8 = 0x67 // both directions: reply to a request
	TPresence  uint8 = 0x69
	TWatch     uint8 = 0x6A
	TLookup    uint8 = 0x6B
	TLookupRes uint8 = 0x6C
	TStatsReq  uint8 = 0x6D
	TStatsRes  uint8 = 0x6E

	// File registry
	TFilePublish   uint8 = 0x70
	TFileUnpublish uint8 = 0x71
	TFileFind      uint8 = 0x72
	TFileProviders uint8 = 0x73

	// Pipes (first frames on a data stream)
	TPipeOpen     uint8 = 0x80
	TPipeIncoming uint8 = 0x81
	TPipeAccept   uint8 = 0x82
	TPipeReject   uint8 = 0x83

	// Relay federation (relay <-> relay links only)
	TRouteAdd    uint8 = 0x90 // nodes attached to the sending relay
	TRouteDel    uint8 = 0x91
	TInterestAdd uint8 = 0x92 // subscription patterns with local subscribers
	TInterestDel uint8 = 0x93
	TFwdPub      uint8 = 0x94 // publication forwarded between relays

	// File transfer (inside an "mk.file" pipe)
	TManifestReq uint8 = 0xA0
	TManifestRes uint8 = 0xA1
	TChunkReq    uint8 = 0xA2
	TChunkData   uint8 = 0xA3
	TFileError   uint8 = 0xA4
	TChunkDataZ  uint8 = 0xA5 // deflate-compressed chunk: index u32, raw length u32, data
)

// ChunkReq flags.
const (
	// ChunkAcceptCompressed: the requester decodes TChunkDataZ.
	ChunkAcceptCompressed uint8 = 1
)

// HELLO flags.
const (
	HelloRelayLink     uint32 = 1 << 0 // the dialer is a federated relay
	HelloWatchPresence uint32 = 1 << 1
)

// Message flags (Send/Pub/Req/Deliver).
const (
	FlagAck       uint8 = 1 << 0 // sender wants an ACK frame
	FlagStore     uint8 = 1 << 1 // queue at the relay if the target is offline
	FlagDatagram  uint8 = 1 << 2 // arrived as an unreliable datagram
	FlagForwarded uint8 = 1 << 7 // already crossed a relay link (no re-forward)
)

// Deliver kinds.
const (
	KindDirect  uint8 = 1
	KindPublish uint8 = 2
	KindRequest uint8 = 3
)

// ACK status codes.
const (
	AckDelivered uint8 = 0
	AckQueued    uint8 = 1
	AckNoRoute   uint8 = 2
	AckDropped   uint8 = 3
	AckRejected  uint8 = 4
)

// Reply status codes.
const (
	ReplyOK      uint8 = 0
	ReplyError   uint8 = 1
	ReplyNoRoute uint8 = 2
	ReplyTimeout uint8 = 3
)

// Error codes.
const (
	ErrCodeBadRequest   uint16 = 1
	ErrCodeUnauthorized uint16 = 2
	ErrCodeNoRoute      uint16 = 3
	ErrCodeNotFound     uint16 = 4
	ErrCodeTooLarge     uint16 = 5
	ErrCodeRateLimited  uint16 = 6
	ErrCodeOverloaded   uint16 = 7
	ErrCodeInternal     uint16 = 8
	ErrCodeVersion      uint16 = 9
	ErrCodeDuplicate    uint16 = 10
)

// Presence events.
const (
	PresenceJoin  uint8 = 1
	PresenceLeave uint8 = 2
)

// Lookup kinds.
const (
	LookupNode   uint8 = 1
	LookupPrefix uint8 = 2
)

// Pipe "via" values.
const (
	ViaDirect uint8 = 0
	ViaRelay  uint8 = 1
)

// ===== Session =====

type Hello struct {
	Version uint8
	NodeID  string
	Token   string
	Flags   uint32
	Meta    string
	Client  string
	Addrs   []string // direct-connect candidates ("host:port")

	// Optional identity (appended in 2.1; older peers omit it): an Ed25519
	// public key and its signature over identity.HelloPayload(NodeID, TS).
	PubKey []byte
	Sig    []byte
	TS     uint64 // unix ms
}

func (m *Hello) Encode() []byte {
	return NewEnc(THello, 64+len(m.NodeID)+len(m.Token)+len(m.Meta)).
		U8(m.Version).Str(m.NodeID).Str(m.Token).U32(m.Flags).Str(m.Meta).Str(m.Client).Strs(m.Addrs).
		Bytes(m.PubKey).Bytes(m.Sig).U64(m.TS).Frame()
}

func DecodeHello(b []byte) (m Hello, err error) {
	d := NewDec(b)
	m.Version, m.NodeID, m.Token, m.Flags, m.Meta, m.Client = d.U8(), d.Str(), d.Str(), d.U32(), d.Str(), d.Str()
	m.Addrs = d.Strs()
	if d.Remaining() > 0 { // identity fields (2.1+)
		m.PubKey, m.Sig, m.TS = d.Bytes(), d.Bytes(), d.U64()
		if len(m.PubKey) == 0 {
			m.PubKey, m.Sig = nil, nil
		}
	}
	return m, d.Err()
}

type Welcome struct {
	Version    uint8
	NodeID     string // assigned node id
	RelayID    string
	Observed   string // client address as seen by the relay
	MaxFrame   uint32
	ServerTime uint64 // unix ms
}

func (m *Welcome) Encode() []byte {
	return NewEnc(TWelcome, 96).U8(m.Version).Str(m.NodeID).Str(m.RelayID).Str(m.Observed).U32(m.MaxFrame).U64(m.ServerTime).Frame()
}

func DecodeWelcome(b []byte) (m Welcome, err error) {
	d := NewDec(b)
	m.Version, m.NodeID, m.RelayID, m.Observed, m.MaxFrame, m.ServerTime = d.U8(), d.Str(), d.Str(), d.Str(), d.U32(), d.U64()
	return m, d.Err()
}

type Error struct {
	Code uint16
	Ref  uint64
	Msg  string
}

func (m *Error) Encode() []byte {
	return NewEnc(TError, 16+len(m.Msg)).U16(m.Code).U64(m.Ref).Str(m.Msg).Frame()
}

func DecodeError(b []byte) (m Error, err error) {
	d := NewDec(b)
	m.Code, m.Ref, m.Msg = d.U16(), d.U64(), d.Str()
	return m, d.Err()
}

func EncodePing(v uint64) []byte { return NewEnc(TPing, 8).U64(v).Frame() }
func EncodePong(v uint64) []byte { return NewEnc(TPong, 8).U64(v).Frame() }

func DecodeU64(b []byte) (uint64, error) {
	d := NewDec(b)
	v := d.U64()
	return v, d.Err()
}

// ===== Messaging =====

// Send is a direct message. From is empty when sent by a node; relays fill it.
type Send struct {
	ID      uint64
	From    string
	To      string
	Flags   uint8
	Payload []byte
}

func (m *Send) Encode() []byte {
	return NewEnc(TSend, 16+len(m.From)+len(m.To)+len(m.Payload)).
		U64(m.ID).Str(m.From).Str(m.To).U8(m.Flags).Raw(m.Payload).Frame()
}

func DecodeSend(b []byte) (m Send, err error) {
	d := NewDec(b)
	m.ID, m.From, m.To, m.Flags = d.U64(), d.Str(), d.Str(), d.U8()
	m.Payload = d.Rest()
	return m, d.Err()
}

// EncodeSendBody returns the SEND body without frame header (for datagrams).
func (m *Send) EncodeBody() []byte { return m.Encode()[HeaderSize:] }

// Deliver carries an incoming message, publication or request to a node.
type Deliver struct {
	ID      uint64
	Kind    uint8
	From    string
	Subject string // topic for publications, target topic for topic requests
	ReqID   uint64
	Flags   uint8
	Payload []byte
}

func (m *Deliver) Encode() []byte {
	return NewEnc(TDeliver, 32+len(m.From)+len(m.Subject)+len(m.Payload)).
		U64(m.ID).U8(m.Kind).Str(m.From).Str(m.Subject).U64(m.ReqID).U8(m.Flags).Raw(m.Payload).Frame()
}

func DecodeDeliver(b []byte) (m Deliver, err error) {
	d := NewDec(b)
	m.ID, m.Kind, m.From, m.Subject, m.ReqID, m.Flags = d.U64(), d.U8(), d.Str(), d.Str(), d.U64(), d.U8()
	m.Payload = d.Rest()
	return m, d.Err()
}

// Ack reports the fate of a Send/Pub with FlagAck. To is used when the ack
// travels back across a relay link.
type Ack struct {
	ID     uint64
	To     string
	Status uint8
	Detail string
}

func (m *Ack) Encode() []byte {
	return NewEnc(TAck, 16+len(m.To)+len(m.Detail)).U64(m.ID).Str(m.To).U8(m.Status).Str(m.Detail).Frame()
}

func DecodeAck(b []byte) (m Ack, err error) {
	d := NewDec(b)
	m.ID, m.To, m.Status, m.Detail = d.U64(), d.Str(), d.U8(), d.Str()
	return m, d.Err()
}

type Pub struct {
	ID      uint64
	From    string
	Topic   string
	Flags   uint8
	Payload []byte
}

func (m *Pub) Encode() []byte {
	return NewEnc(TPub, 16+len(m.From)+len(m.Topic)+len(m.Payload)).
		U64(m.ID).Str(m.From).Str(m.Topic).U8(m.Flags).Raw(m.Payload).Frame()
}

func DecodePub(b []byte) (m Pub, err error) {
	d := NewDec(b)
	m.ID, m.From, m.Topic, m.Flags = d.U64(), d.Str(), d.Str(), d.U8()
	m.Payload = d.Rest()
	return m, d.Err()
}

// Sub subscribes to a topic. Topics are dot separated; "*" matches one token
// and ">" matches the rest. Subscribers sharing a Queue name form a group in
// which each message goes to exactly one member.
type Sub struct {
	Topic string
	Queue string
}

func (m *Sub) Encode() []byte {
	return NewEnc(TSub, 4+len(m.Topic)+len(m.Queue)).Str(m.Topic).Str(m.Queue).Frame()
}

func DecodeSub(b []byte) (m Sub, err error) {
	d := NewDec(b)
	m.Topic, m.Queue = d.Str(), d.Str()
	return m, d.Err()
}

func EncodeUnsub(topic string) []byte { return NewEnc(TUnsub, 2+len(topic)).Str(topic).Frame() }

func DecodeUnsub(b []byte) (string, error) {
	d := NewDec(b)
	s := d.Str()
	return s, d.Err()
}

// Req is a request. It is routed to node To, or (To empty) to one
// subscriber of Topic.
type Req struct {
	ID        uint64
	From      string
	To        string
	Topic     string
	TimeoutMs uint32
	Flags     uint8
	Payload   []byte
}

func (m *Req) Encode() []byte {
	return NewEnc(TReq, 24+len(m.From)+len(m.To)+len(m.Topic)+len(m.Payload)).
		U64(m.ID).Str(m.From).Str(m.To).Str(m.Topic).U32(m.TimeoutMs).U8(m.Flags).Raw(m.Payload).Frame()
}

func DecodeReq(b []byte) (m Req, err error) {
	d := NewDec(b)
	m.ID, m.From, m.To, m.Topic, m.TimeoutMs, m.Flags = d.U64(), d.Str(), d.Str(), d.Str(), d.U32(), d.U8()
	m.Payload = d.Rest()
	return m, d.Err()
}

// Reply answers a request. Nodes set To (the requester); relays set From.
type Reply struct {
	ID      uint64
	From    string
	To      string
	Status  uint8
	Payload []byte
}

func (m *Reply) Encode() []byte {
	return NewEnc(TReply, 16+len(m.From)+len(m.To)+len(m.Payload)).
		U64(m.ID).Str(m.From).Str(m.To).U8(m.Status).Raw(m.Payload).Frame()
}

func DecodeReply(b []byte) (m Reply, err error) {
	d := NewDec(b)
	m.ID, m.From, m.To, m.Status = d.U64(), d.Str(), d.Str(), d.U8()
	m.Payload = d.Rest()
	return m, d.Err()
}

type Presence struct {
	Event  uint8
	NodeID string
	Meta   string
}

func (m *Presence) Encode() []byte {
	return NewEnc(TPresence, 8+len(m.NodeID)+len(m.Meta)).U8(m.Event).Str(m.NodeID).Str(m.Meta).Frame()
}

func DecodePresence(b []byte) (m Presence, err error) {
	d := NewDec(b)
	m.Event, m.NodeID, m.Meta = d.U8(), d.Str(), d.Str()
	return m, d.Err()
}

func EncodeWatch(on bool) []byte {
	v := uint8(0)
	if on {
		v = 1
	}
	return NewEnc(TWatch, 1).U8(v).Frame()
}

// NodeInfo describes a node known to a relay.
type NodeInfo struct {
	NodeID   string
	Addrs    []string // direct-connect candidates
	Observed string   // address the relay sees
	Meta     string
	Relay    string // relay id the node is attached to
}

func (n *NodeInfo) enc(e *Enc) {
	e.Str(n.NodeID).Strs(n.Addrs).Str(n.Observed).Str(n.Meta).Str(n.Relay)
}

func decNodeInfo(d *Dec) NodeInfo {
	return NodeInfo{NodeID: d.Str(), Addrs: d.Strs(), Observed: d.Str(), Meta: d.Str(), Relay: d.Str()}
}

type Lookup struct {
	ID   uint64
	Kind uint8
	Key  string
}

func (m *Lookup) Encode() []byte {
	return NewEnc(TLookup, 12+len(m.Key)).U64(m.ID).U8(m.Kind).Str(m.Key).Frame()
}

func DecodeLookup(b []byte) (m Lookup, err error) {
	d := NewDec(b)
	m.ID, m.Kind, m.Key = d.U64(), d.U8(), d.Str()
	return m, d.Err()
}

type LookupRes struct {
	ID    uint64
	Nodes []NodeInfo
}

func (m *LookupRes) Encode() []byte {
	e := NewEnc(TLookupRes, 16+64*len(m.Nodes)).U64(m.ID).U16(uint16(len(m.Nodes)))
	for i := range m.Nodes {
		m.Nodes[i].enc(e)
	}
	return e.Frame()
}

func DecodeLookupRes(b []byte) (m LookupRes, err error) {
	d := NewDec(b)
	m.ID = d.U64()
	n := int(d.U16())
	for i := 0; i < n && d.Err() == nil; i++ {
		m.Nodes = append(m.Nodes, decNodeInfo(d))
	}
	return m, d.Err()
}

func EncodeStatsReq(id uint64) []byte { return NewEnc(TStatsReq, 8).U64(id).Frame() }

func EncodeStatsRes(id uint64, json []byte) []byte {
	return NewEnc(TStatsRes, 8+len(json)).U64(id).Raw(json).Frame()
}

func DecodeStatsRes(b []byte) (uint64, []byte, error) {
	d := NewDec(b)
	id := d.U64()
	return id, d.Rest(), d.Err()
}

// ===== File registry =====

type FilePublish struct {
	Provider  string
	Hash      string
	Name      string
	Size      uint64
	ChunkSize uint32
	Root      string // manifest root (sha256 of chunk hashes)
}

func (m *FilePublish) Encode() []byte {
	return NewEnc(TFilePublish, 160+len(m.Name)).
		Str(m.Provider).Str(m.Hash).Str(m.Name).U64(m.Size).U32(m.ChunkSize).Str(m.Root).Frame()
}

func DecodeFilePublish(b []byte) (m FilePublish, err error) {
	d := NewDec(b)
	m.Provider, m.Hash, m.Name, m.Size, m.ChunkSize, m.Root = d.Str(), d.Str(), d.Str(), d.U64(), d.U32(), d.Str()
	return m, d.Err()
}

type FileUnpublish struct {
	Provider string
	Hash     string
}

func (m *FileUnpublish) Encode() []byte {
	return NewEnc(TFileUnpublish, 4+len(m.Provider)+len(m.Hash)).Str(m.Provider).Str(m.Hash).Frame()
}

func DecodeFileUnpublish(b []byte) (m FileUnpublish, err error) {
	d := NewDec(b)
	m.Provider, m.Hash = d.Str(), d.Str()
	return m, d.Err()
}

type FileFind struct {
	ID   uint64
	Hash string
}

func (m *FileFind) Encode() []byte {
	return NewEnc(TFileFind, 12+len(m.Hash)).U64(m.ID).Str(m.Hash).Frame()
}

func DecodeFileFind(b []byte) (m FileFind, err error) {
	d := NewDec(b)
	m.ID, m.Hash = d.U64(), d.Str()
	return m, d.Err()
}

type FileProviders struct {
	ID        uint64
	Hash      string
	Name      string
	Size      uint64
	ChunkSize uint32
	Root      string
	Providers []NodeInfo
}

func (m *FileProviders) Encode() []byte {
	e := NewEnc(TFileProviders, 160+64*len(m.Providers)).
		U64(m.ID).Str(m.Hash).Str(m.Name).U64(m.Size).U32(m.ChunkSize).Str(m.Root).U16(uint16(len(m.Providers)))
	for i := range m.Providers {
		m.Providers[i].enc(e)
	}
	return e.Frame()
}

func DecodeFileProviders(b []byte) (m FileProviders, err error) {
	d := NewDec(b)
	m.ID, m.Hash, m.Name, m.Size, m.ChunkSize, m.Root = d.U64(), d.Str(), d.Str(), d.U64(), d.U32(), d.Str()
	n := int(d.U16())
	for i := 0; i < n && d.Err() == nil; i++ {
		m.Providers = append(m.Providers, decNodeInfo(d))
	}
	return m, d.Err()
}

// ===== Pipes =====

// PipeOpen is the first frame of a stream a node opens to its relay to reach
// another node. From is set only on relay links.
type PipeOpen struct {
	From    string
	To      string
	Service string
	Meta    string
}

func (m *PipeOpen) Encode() []byte {
	return NewEnc(TPipeOpen, 8+len(m.From)+len(m.To)+len(m.Service)+len(m.Meta)).
		Str(m.From).Str(m.To).Str(m.Service).Str(m.Meta).Frame()
}

func DecodePipeOpen(b []byte) (m PipeOpen, err error) {
	d := NewDec(b)
	m.From, m.To, m.Service, m.Meta = d.Str(), d.Str(), d.Str(), d.Str()
	return m, d.Err()
}

// PipeIncoming is the first frame on a stream delivered to the target node
// (by a relay, or directly by the opening peer).
type PipeIncoming struct {
	From    string
	Service string
	Meta    string
	Via     uint8
	// Token authenticates direct (non-relayed) pipes when the network uses
	// a shared token; relays leave it empty because they already checked.
	Token string
}

func (m *PipeIncoming) Encode() []byte {
	return NewEnc(TPipeIncoming, 10+len(m.From)+len(m.Service)+len(m.Meta)+len(m.Token)).
		Str(m.From).Str(m.Service).Str(m.Meta).U8(m.Via).Str(m.Token).Frame()
}

func DecodePipeIncoming(b []byte) (m PipeIncoming, err error) {
	d := NewDec(b)
	m.From, m.Service, m.Meta, m.Via, m.Token = d.Str(), d.Str(), d.Str(), d.U8(), d.Str()
	return m, d.Err()
}

func EncodePipeAccept() []byte { return NewEnc(TPipeAccept, 0).Frame() }

type PipeReject struct {
	Code   uint16
	Reason string
}

func (m *PipeReject) Encode() []byte {
	return NewEnc(TPipeReject, 4+len(m.Reason)).U16(m.Code).Str(m.Reason).Frame()
}

func DecodePipeReject(b []byte) (m PipeReject, err error) {
	d := NewDec(b)
	m.Code, m.Reason = d.U16(), d.Str()
	return m, d.Err()
}

// ===== Federation =====

// EncodeRouteAdd announces nodes attached to the sending relay.
func EncodeRouteAdd(nodes []NodeInfo) []byte {
	e := NewEnc(TRouteAdd, 8+64*len(nodes)).U32(uint32(len(nodes)))
	for i := range nodes {
		nodes[i].enc(e)
	}
	return e.Frame()
}

func DecodeRouteAdd(b []byte) ([]NodeInfo, error) {
	d := NewDec(b)
	n := int(d.U32())
	var out []NodeInfo
	for i := 0; i < n && d.Err() == nil; i++ {
		out = append(out, decNodeInfo(d))
	}
	return out, d.Err()
}

// EncodeStrList encodes a frame whose body is a list of strings
// (TRouteDel, TInterestAdd, TInterestDel).
func EncodeStrList(typ uint8, items []string) []byte {
	e := NewEnc(typ, 4+32*len(items)).U32(uint32(len(items)))
	for _, s := range items {
		e.Str(s)
	}
	return e.Frame()
}

func DecodeStrList(b []byte) ([]string, error) {
	d := NewDec(b)
	n := int(d.U32())
	var out []string
	for i := 0; i < n && d.Err() == nil; i++ {
		out = append(out, d.Str())
	}
	return out, d.Err()
}

// FwdPub is a publication forwarded between relays. ServedQueues lists queue
// groups that already received the message, so each group gets it once.
type FwdPub struct {
	ID           uint64
	From         string
	Topic        string
	Flags        uint8
	ServedQueues []string
	Payload      []byte
}

func (m *FwdPub) Encode() []byte {
	return NewEnc(TFwdPub, 24+len(m.From)+len(m.Topic)+len(m.Payload)).
		U64(m.ID).Str(m.From).Str(m.Topic).U8(m.Flags).Strs(m.ServedQueues).Raw(m.Payload).Frame()
}

func DecodeFwdPub(b []byte) (m FwdPub, err error) {
	d := NewDec(b)
	m.ID, m.From, m.Topic, m.Flags = d.U64(), d.Str(), d.Str(), d.U8()
	m.ServedQueues = d.Strs()
	m.Payload = d.Rest()
	return m, d.Err()
}

// ===== File transfer =====

func EncodeManifestReq(hash string) []byte {
	return NewEnc(TManifestReq, 2+len(hash)).Str(hash).Frame()
}

func DecodeManifestReq(b []byte) (string, error) {
	d := NewDec(b)
	s := d.Str()
	return s, d.Err()
}

// ManifestRes describes a file: chunk hashes are raw 32-byte SHA-256 digests.
type ManifestRes struct {
	Hash        string
	Name        string
	Size        uint64
	ChunkSize   uint32
	ChunkHashes [][32]byte
}

func (m *ManifestRes) Encode() []byte {
	e := NewEnc(TManifestRes, 64+len(m.Name)+32*len(m.ChunkHashes)).
		Str(m.Hash).Str(m.Name).U64(m.Size).U32(m.ChunkSize).U32(uint32(len(m.ChunkHashes)))
	for i := range m.ChunkHashes {
		e.Raw(m.ChunkHashes[i][:])
	}
	return e.Frame()
}

func DecodeManifestRes(b []byte) (m ManifestRes, err error) {
	d := NewDec(b)
	m.Hash, m.Name, m.Size, m.ChunkSize = d.Str(), d.Str(), d.U64(), d.U32()
	n := int(d.U32())
	if d.Err() != nil {
		return m, d.Err()
	}
	rest := d.Rest()
	if len(rest) != n*32 {
		return m, ErrMalformed
	}
	m.ChunkHashes = make([][32]byte, n)
	for i := 0; i < n; i++ {
		copy(m.ChunkHashes[i][:], rest[i*32:])
	}
	return m, nil
}

type ChunkReq struct {
	Hash    string
	Indices []uint32
	Flags   uint8 // ChunkAccept* (2.1+)
}

func (m *ChunkReq) Encode() []byte {
	e := NewEnc(TChunkReq, 5+len(m.Hash)+4*len(m.Indices)).Str(m.Hash).U32(uint32(len(m.Indices)))
	for _, i := range m.Indices {
		e.U32(i)
	}
	if m.Flags != 0 { // appended in 2.1; older providers ignore it
		e.U8(m.Flags)
	}
	return e.Frame()
}

func DecodeChunkReq(b []byte) (m ChunkReq, err error) {
	d := NewDec(b)
	m.Hash = d.Str()
	n := int(d.U32())
	if d.Err() != nil || n > 1<<20 {
		return m, ErrMalformed
	}
	m.Indices = make([]uint32, 0, n)
	for i := 0; i < n && d.Err() == nil; i++ {
		m.Indices = append(m.Indices, d.U32())
	}
	if d.Err() == nil && d.Remaining() > 0 {
		m.Flags = d.U8()
	}
	return m, d.Err()
}

// ChunkDataZHeader is the header of a compressed chunk frame carrying n
// compressed bytes of a chunk whose raw length is rawLen.
func ChunkDataZHeader(index uint32, rawLen, n int) []byte {
	var h [HeaderSize + 8]byte
	l := uint32(8 + n)
	h[0], h[1], h[2], h[3] = byte(l>>24), byte(l>>16), byte(l>>8), byte(l)
	h[4] = TChunkDataZ
	h[5], h[6], h[7], h[8] = byte(index>>24), byte(index>>16), byte(index>>8), byte(index)
	r := uint32(rawLen)
	h[9], h[10], h[11], h[12] = byte(r>>24), byte(r>>16), byte(r>>8), byte(r)
	return h[:]
}

// ChunkDataHeader returns the frame header + index for a chunk of n bytes;
// the caller writes the data right after it (avoids copying the chunk).
func ChunkDataHeader(index uint32, n int) []byte {
	var h [HeaderSize + 4]byte
	l := uint32(4 + n)
	h[0], h[1], h[2], h[3] = byte(l>>24), byte(l>>16), byte(l>>8), byte(l)
	h[4] = TChunkData
	h[5], h[6], h[7], h[8] = byte(index>>24), byte(index>>16), byte(index>>8), byte(index)
	return h[:]
}

type FileError struct {
	Code  uint16
	Index uint32
	Msg   string
}

func (m *FileError) Encode() []byte {
	return NewEnc(TFileError, 8+len(m.Msg)).U16(m.Code).U32(m.Index).Str(m.Msg).Frame()
}

func DecodeFileError(b []byte) (m FileError, err error) {
	d := NewDec(b)
	m.Code, m.Index, m.Msg = d.U16(), d.U32(), d.Str()
	return m, d.Err()
}
