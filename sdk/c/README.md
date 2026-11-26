# Magnetk C ABI (`libmagnetk`)

`libmagnetk` exposes the whole Magnetk engine — messaging, pub/sub,
request/reply, pipes, files, embedded relays — through a plain C interface,
so any language with a C FFI can use it. The Python and C++ SDKs are built
on it. The authoritative, fully commented header is
[`include/magnetk.h`](include/magnetk.h).

## Build

```bash
scripts/build.sh        # Linux/macOS -> build/lib/libmagnetk.so|.dylib (+ libmsquic)
scripts\build.bat       # Windows     -> build\lib\libmagnetk.dll (+ msquic.dll)
# or directly:
cd go && go build -buildmode=c-shared -o ../build/lib/libmagnetk.so ./capi
```

Link against it (`-L build/lib -lmagnetk`) or load it at run time
(`dlopen` / `LoadLibrary`); ship `msquic` next to it for QUIC.

## Conventions

* Every object is an opaque `uint64_t` handle (`mk_node`, `mk_pipe`,
  `mk_relay`, `mk_download`); 0 is invalid.
* Functions return `MK_OK` (0) or a negative `MK_ERR_*` code; when `err`
  is not NULL it receives a `malloc`'d message to release with `mk_free`.
* Strings and buffers returned by the library are released with `mk_free`
  (messages with `mk_message_free`).
* Blocking calls take `timeout_ms` (`-1` = forever) and may be called from
  any thread. The library never calls back into your code — receiving is
  a blocking `mk_recv` you run on your own thread.
* Options are JSON strings (`mk_node_open`, `mk_relay_start`,
  `mk_download_start`), so new options never break the ABI.

## Example

```c
#include <magnetk.h>
#include <stdio.h>

int main(void) {
    char* err = NULL;
    mk_relay relay;
    mk_relay_start("{\"listen\":\"127.0.0.1:0\"}", &relay, &err);
    char cfg[128];
    snprintf(cfg, sizeof cfg, "{\"relays\":[\"127.0.0.1:%d\"],\"id\":\"c-node\"}", mk_relay_port(relay));

    mk_node n;
    if (mk_node_open(cfg, &n, &err) != MK_OK) { fprintf(stderr, "%s\n", err); mk_free(err); return 1; }
    mk_subscribe(n, "demo.*", NULL, &err);
    mk_publish(n, "demo.hello", "hi", 2, 0, &err);

    mk_message* m;
    if (mk_recv(n, 2000, &m, &err) == 1) {   /* 1 = got a message, 0 = timeout */
        printf("%s on %s: %.*s\n", m->from, m->subject, m->len, (char*)m->data);
        mk_message_free(m);
    }
    mk_node_close(n);
    mk_relay_close(relay);
    return 0;
}
```

Full coverage of the API is in [`test/capi_test.c`](test/capi_test.c)
(19 checks, including a threaded request responder):

```bash
gcc -I sdk/c/include sdk/c/test/capi_test.c -L build/lib -lmagnetk -o capi_test && ./capi_test
```

## Functions

| Area | Functions |
|---|---|
| library | `mk_version`, `mk_quic_available`, `mk_free` |
| node | `mk_node_open`, `mk_node_close`, `mk_node_id`, `mk_node_info`, `mk_node_wait_connected`, `mk_ping`, `mk_list_nodes`, `mk_relay_stats`, `mk_watch_presence` |
| messages | `mk_send`, `mk_publish`, `mk_subscribe`, `mk_unsubscribe`, `mk_serve`, `mk_request`, `mk_recv`, `mk_respond`, `mk_message_free` |
| pipes | `mk_pipe_open`, `mk_pipe_accept`, `mk_pipe_info`, `mk_pipe_read`, `mk_pipe_write`, `mk_pipe_close_write`, `mk_pipe_close` |
| files | `mk_share`, `mk_unshare`, `mk_find_providers`, `mk_download_start`, `mk_download_poll`, `mk_download_cancel`, `mk_download_free` |
| relay | `mk_relay_start`, `mk_relay_port`, `mk_relay_stats_json`, `mk_relay_close` |

Resource limits for the host process: `MAGNETK_MAX_PROCS`,
`MAGNETK_MEMORY_LIMIT_MB`, `MAGNETK_GC_PERCENT` (environment).

## Identity and secure chat

Open the node with `"identity_file":"alice.key"` (and optionally
`"chat_require_approval":true`). Chat messages and acks then arrive through
`mk_recv` as `MK_KIND_CHAT` with JSON in `data`.

```c
char* out = NULL;
mk_identity(n, &out, &err);                    /* {"fingerprint","formatted","address"} */
mk_free(out);
mk_chat_send(n, "bob", "hello", NULL, 0, NULL, 0, &out, &err);   /* encrypted + signed */
mk_free(out);
mk_chat_contact(n, "safety", "bob", &out, &err);                 /* {"peer","safety_number"} */
mk_free(out);
mk_chat_contact(n, "allow", "carol", NULL, &err);                 /* accept a message request */
```

| Function | |
|---|---|
| `mk_identity` | this node's fingerprint and `magnetk://` address |
| `mk_chat_send` | text and/or attachment, end-to-end encrypted when both sides have keys |
| `mk_chat_contact` | `allow`, `block`, `verified`, `accept` (changed key), `safety`, `contact`, `expect` |
| `mk_chat_send_file` | any file from disk, encrypted and sent through the file engine (kind `file`) |

Download options (`mk_download_start` JSON): `offset`/`length` (byte range),
`no_compression`, `pipeline` (0 = adaptive), `max_pipeline`, `max_sources`,
`streams_per_source`, `route`, `seed`, `no_resume`. Progress JSON also has
`window` and `wire_bytes`.

Model and limits: [SECURITY.md](../../docs/SECURITY.md).
