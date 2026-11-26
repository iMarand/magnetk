/*
 * Magnetk C API  (libmagnetk.dll / libmagnetk.so / libmagnetk.dylib)
 *
 * The full Magnetk engine behind a plain C interface: messaging (direct,
 * publish/subscribe, request/reply, datagrams), pipes (byte streams between
 * nodes, peer-to-peer or through a relay), file sharing/downloading and an
 * embeddable relay. The Python and C++ SDKs are built on this header; any
 * language with a C FFI can use it.
 *
 * Conventions
 *   - Objects are opaque 64-bit handles; 0 is never a valid handle.
 *   - Functions return MK_OK (0) or a negative MK_ERR_* code. When `err` is
 *     not NULL it receives a message on failure; free it with mk_free().
 *   - Strings and buffers returned by the library (except mk_version) are
 *     owned by the caller and must be released with mk_free().
 *   - timeout_ms: -1 waits forever, 0 polls, >0 waits that many ms.
 *   - Every function is thread-safe. Nothing calls back into the caller:
 *     incoming messages are pulled with mk_recv(), so the library is safe to
 *     use from any runtime (Python, JVM, .NET, ...).
 *   - The runtime library msquic.dll / libmsquic.so must be next to
 *     libmagnetk (or on the loader path) for QUIC; without it Magnetk uses
 *     TCP+TLS automatically.
 */
#ifndef MAGNETK_H
#define MAGNETK_H

#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

/* ---------- status codes ---------- */
#define MK_OK                  0
#define MK_ERR                -1   /* generic failure */
#define MK_ERR_TIMEOUT        -2
#define MK_ERR_CLOSED         -3   /* node / pipe closed */
#define MK_ERR_NO_ROUTE       -4   /* destination node not connected */
#define MK_ERR_UNAUTHORIZED   -5   /* relay rejected the token, or the node id belongs to another identity key */
#define MK_ERR_NOT_CONNECTED  -6   /* no relay connection (or it dropped) */
#define MK_ERR_REMOTE         -7   /* remote request handler returned an error */
#define MK_ERR_REJECTED       -8   /* remote node refused the pipe */
#define MK_ERR_NO_PROVIDERS   -9   /* nobody shares that file */
#define MK_ERR_INVALID       -10   /* bad argument / handle */
#define MK_ERR_DROPPED       -11   /* receiver too slow, message dropped */

/* ---------- message kinds (mk_message.kind) ---------- */
#define MK_KIND_DIRECT    1   /* sent to this node with mk_send */
#define MK_KIND_PUBLISH   2   /* matched one of your subscriptions */
#define MK_KIND_REQUEST   3   /* answer with mk_respond(message.request) */
#define MK_KIND_PRESENCE  4   /* subject "join"/"leave", from = node id */
#define MK_KIND_CHAT      5   /* secure chat: subject = "text"|"audio"|"ack", data = JSON (see mk_chat_send) */

/* ---------- send flags ---------- */
#define MK_SEND_RECEIPT     1   /* wait for the relay's delivery receipt */
#define MK_SEND_STORE       2   /* keep at the relay if the receiver is offline */
#define MK_SEND_UNRELIABLE  4   /* QUIC datagram: lowest latency, may be lost */

typedef uint64_t mk_node;
typedef uint64_t mk_pipe;
typedef uint64_t mk_download;
typedef uint64_t mk_relay;

typedef struct mk_message {
    int32_t     kind;      /* MK_KIND_* */
    uint64_t    id;
    char*       from;      /* sender node id */
    char*       subject;   /* topic, or "join"/"leave" for presence */
    void*       data;      /* payload (may be NULL when len == 0) */
    int32_t     len;
    int32_t     datagram;  /* 1 if it arrived as an unreliable datagram */
    uint64_t    request;   /* non-zero for requests: pass to mk_respond */
} mk_message;

/* ---------- library ---------- */
const char* mk_version(void);          /* static string, do not free */
int         mk_quic_available(void);   /* 1 if QUIC (MsQuic) is usable */
void        mk_free(void* p);

/* ---------- node ----------
 * config_json keys (all optional except relays):
 *   relays [str], id, token, meta, transport ("auto"|"quic"|"tcp"),
 *   route ("auto"|"direct"|"relay"), listen (bool), listen_addr, advertise [str],
 *   upnp, verify_peer, ca_file, cert_file, key_file, watch_presence,
 *   inbox_size, request_timeout_ms, heartbeat_ms, chunk_size,
 *   streams_per_source, cache_dir ("-" = off), link_relays [str],
 *   sub_buffer, slow_consumer_wait_ms, connect_timeout_ms, congestion ("cubic" default | "bbr"),
 *   profile ("max_throughput"|"low_latency"|"realtime"|"scavenger"), log (bool),
 *   pipeline (chunk requests in flight per stream, 0 = adaptive), no_compression (bool)
 * identity and secure chat:
 *   identity_file     Ed25519 identity key, created if missing ("~/..." allowed).
 *                     Relays bind the node id to it; chat is signed and encrypted with it.
 *   chat (bool)       decode secure chat in mk_recv (default: on when identity_file is set)
 *   chat_dir          where received attachments are saved
 *   known_peers_file  pinned keys and contacts (default: next to identity_file)
 *   chat_require_approval    strangers arrive as requests until mk_chat_contact(.., "allow", ..)
 *   chat_require_encryption  refuse to send or accept unencrypted chat
 * If relays is empty, MAGNETK_RELAYS (comma separated) is used.
 * mk_node_open blocks until the first relay connection succeeds. */
int   mk_node_open(const char* config_json, mk_node* out, char** err);
int   mk_node_close(mk_node node);
char* mk_node_id(mk_node node);                       /* free */
char* mk_node_info(mk_node node);                     /* JSON, free */
int   mk_node_wait_connected(mk_node node, int timeout_ms, char** err);

/* ---------- identity and secure chat ----------
 * Chat messages are end-to-end encrypted (X25519 + AES-256-GCM) and signed
 * (Ed25519) when both sides have identity keys; the relay sees only who,
 * when and how big. They arrive through mk_recv as MK_KIND_CHAT with data =
 * JSON: {"id","from","to","kind","text","mime","dur_ms","size","sha256","t",
 * "path" (saved attachment),"verified" (attachment hash ok),"encrypted",
 * "trust" ("verified"|"new"|"changed"|"unsigned"|"invalid"),"fingerprint",
 * "request" (stranger: no ack until allowed),"safety_checked","rtt_ms" (acks),
 * "error"}. */
/* {"fingerprint","formatted","address"} (address = magnetk://id@relay?key=...) */
int mk_identity(mk_node node, char** out_json, char** err);
/* send text and/or an attachment (voice note); to = node id; out_json = the sent message */
int mk_chat_send(mk_node node, const char* to, const char* text, const void* attachment, int attachment_len,
                 const char* mime, int dur_ms, char** out_json, char** err);
/* action: "allow" | "block" | "verified" (safety number compared) | "accept" (a changed key)
 *       | "safety" (out_json = {"peer","safety_number"}) | "contact" (out_json = state)
 *       | "expect" (peer = "node=FINGERPRINT", e.g. from a key= address) */
int mk_chat_contact(mk_node node, const char* action, const char* peer, char** out_json, char** err);
/* send any file from disk (up to 4 GiB): big files are encrypted with a fresh key, shared
 * through the file engine (chunked, resumable, peer to peer when possible) and the sealed
 * message carries the link and key. The receiver gets MK_KIND_CHAT kind "file" twice:
 * "pending": true while downloading, then with "path" set once verified. */
int mk_chat_send_file(mk_node node, const char* to, const char* path, const char* caption,
                      const char* mime, int dur_ms, char** out_json, char** err);

/* ---------- messaging ---------- */
int mk_send(mk_node node, const char* to, const void* data, int len,
            int flags, int timeout_ms, char** err);
int mk_publish(mk_node node, const char* topic, const void* data, int len,
               int flags, char** err);

/* Topics are dot separated: "a.b.c". Patterns: "*" = one token, ">" = rest.
 * Subscribers sharing a queue name split the messages (load balancing).
 * Matching messages are delivered through mk_recv. */
int mk_subscribe(mk_node node, const char* pattern, const char* queue, char** err);
int mk_unsubscribe(mk_node node, const char* pattern, char** err);

/* Serve requests sent to a topic (load-balanced with other servers of the
 * same topic). Requests arrive through mk_recv with kind MK_KIND_REQUEST. */
int mk_serve(mk_node node, const char* topic, char** err);

/* Request to node `to`, or (to == NULL / "") to one server of `topic`.
 * *out (free with mk_free) receives the response. */
int mk_request(mk_node node, const char* to, const char* topic,
               const void* data, int len, int timeout_ms,
               void** out, int* out_len, char** err);

/* Next incoming message. Returns 1 with *out set, 0 on timeout, or an
 * error. Free the message with mk_message_free. */
int  mk_recv(mk_node node, int timeout_ms, mk_message** out, char** err);
void mk_message_free(mk_message* msg);

/* Answer a request (message.request). is_error = 1 sends an error string. */
int mk_respond(mk_node node, uint64_t request, const void* data, int len,
               int is_error, char** err);

int   mk_watch_presence(mk_node node, int on, char** err);
int   mk_ping(mk_node node, int timeout_ms, int64_t* out_micros, char** err);
char* mk_list_nodes(mk_node node, const char* prefix, char** err);   /* JSON, free */
char* mk_relay_stats(mk_node node, char** err);                      /* JSON, free */

/* ---------- pipes ----------
 * route: NULL/"" = node default, "auto", "direct" or "relay".
 * Incoming pipes for services without a handler are returned by
 * mk_pipe_accept (accepting starts with the first call). */
int   mk_pipe_open(mk_node node, const char* to, const char* service,
                   const char* meta, const char* route, int timeout_ms,
                   mk_pipe* out, char** err);
int   mk_pipe_accept(mk_node node, int timeout_ms, mk_pipe* out, char** err); /* 1, 0 timeout, <0 */
char* mk_pipe_info(mk_pipe pipe);   /* JSON {peer, service, meta, direct}, free */
/* returns bytes read (>0), 0 at end of stream, MK_ERR_TIMEOUT, or an error */
int   mk_pipe_read(mk_pipe pipe, void* buf, int len, int timeout_ms, char** err);
/* returns bytes written, MK_ERR_TIMEOUT, or an error */
int   mk_pipe_write(mk_pipe pipe, const void* buf, int len, int timeout_ms, char** err);
int   mk_pipe_close_write(mk_pipe pipe);   /* send EOF, keep reading */
int   mk_pipe_close(mk_pipe pipe);

/* ---------- files ---------- */
/* Hash, serve and register a file. *out_json (free) =
 * {hash, name, size, chunk_size, root, path, link}. chunk_size 0 = auto. */
int   mk_share(mk_node node, const char* path, int64_t chunk_size,
               char** out_json, char** err);
int   mk_unshare(mk_node node, const char* hash, char** err);
char* mk_find_providers(mk_node node, const char* hash, char** err);  /* JSON, free */

/* Start a download of a magnetk link or 64-char hash. out = file, directory
 * or NULL (current directory). opts_json (optional): {route, seed,
 * streams_per_source, no_resume, max_sources,
 * pipeline (chunk requests in flight per stream; 0 = adaptive), max_pipeline,
 * offset, length (byte range: only those bytes are fetched and written; needs a link),
 * no_compression (do not ask for compressed chunks)}. */
int  mk_download_start(mk_node node, const char* link, const char* out,
                       const char* opts_json, mk_download* dl, char** err);
/* Wait up to timeout_ms; *progress_json (free) = {name, size, done, percent,
 * bytes_per_sec, eta_ms, sources, streams, window, wire_bytes, finished,
 * path?, hash?, bytes?, resumed?, wire_bytes?, offset?, ...}.
 * Returns 0 while running, 1 when finished OK, or an error code. */
int  mk_download_poll(mk_download dl, int timeout_ms, char** progress_json, char** err);
int  mk_download_cancel(mk_download dl);
void mk_download_free(mk_download dl);

/* ---------- embedded relay ----------
 * config_json: {listen ("127.0.0.1:0"), legacy_listen, transport, id,
 *               tokens [str], peers [str], cluster_secret, cert_file,
 *               key_file, log} */
int   mk_relay_start(const char* config_json, mk_relay* out, char** err);
int   mk_relay_port(mk_relay relay);
char* mk_relay_stats_json(mk_relay relay);   /* JSON, free */
int   mk_relay_close(mk_relay relay);

#ifdef __cplusplus
}
#endif

#endif /* MAGNETK_H */
