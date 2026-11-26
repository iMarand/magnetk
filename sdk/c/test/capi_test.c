/* End-to-end test of the C API: embedded relay, two nodes, messaging,
 * request/reply, pipes and a file transfer. */
#include "magnetk.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>

static int failures = 0;
#define CHECK(cond, what) do { if (!(cond)) { fprintf(stderr, "FAIL: %s (line %d)\n", what, __LINE__); failures++; } else { printf("ok   %s\n", what); } } while (0)

#ifdef _WIN32
#include <windows.h>
typedef HANDLE thread_t;
static DWORD WINAPI thread_main(LPVOID arg);
static thread_t start_thread(void (*fn)(mk_node*), mk_node* arg);
#else
#include <pthread.h>
typedef pthread_t thread_t;
#endif

static void (*g_fn)(mk_node*);
static mk_node* g_arg;
#ifdef _WIN32
static DWORD WINAPI thread_main(LPVOID arg) { (void)arg; g_fn(g_arg); return 0; }
static thread_t start_thread(void (*fn)(mk_node*), mk_node* arg) { g_fn = fn; g_arg = arg; return CreateThread(NULL, 0, thread_main, NULL, 0, NULL); }
static void join_thread(thread_t t) { WaitForSingleObject(t, INFINITE); CloseHandle(t); }
#else
static void* thread_main(void* arg) { (void)arg; g_fn(g_arg); return NULL; }
static thread_t start_thread(void (*fn)(mk_node*), mk_node* arg) { pthread_t t; g_fn = fn; g_arg = arg; pthread_create(&t, NULL, thread_main, NULL); return t; }
static void join_thread(thread_t t) { pthread_join(t, NULL); }
#endif

/* Answers two requests: upper-cases the payload, or fails on "fail". */
static void responder(mk_node* node) {
    for (int handled = 0; handled < 2; ) {
        mk_message* m = NULL;
        char* err = NULL;
        int rc = mk_recv(*node, 5000, &m, &err);
        if (rc != 1) { if (err) mk_free(err); return; }
        if (m->kind == MK_KIND_REQUEST) {
            if (m->len == 4 && memcmp(m->data, "fail", 4) == 0) {
                mk_respond(*node, m->request, "asked to fail", 13, 1, NULL);
            } else {
                char up[64];
                for (int i = 0; i < m->len && i < 63; i++) up[i] = (char)(((char*)m->data)[i] - 32);
                mk_respond(*node, m->request, up, m->len, 0, NULL);
            }
            handled++;
        }
        mk_message_free(m);
    }
}

static void die_on(int rc, char* err, const char* what) {
    if (rc < 0) {
        fprintf(stderr, "FATAL %s: rc=%d %s\n", what, rc, err ? err : "");
        exit(1);
    }
    if (err) mk_free(err);
}

int main(void) {
    char* err = NULL;
    printf("libmagnetk %s, QUIC available: %d\n", mk_version(), mk_quic_available());

    mk_relay relay = 0;
    die_on(mk_relay_start("{\"listen\":\"127.0.0.1:0\"}", &relay, &err), err, "relay start");
    int port = mk_relay_port(relay);
    char cfg[256];

    mk_node a = 0, b = 0;
    snprintf(cfg, sizeof cfg, "{\"relays\":[\"127.0.0.1:%d\"],\"id\":\"c-alice\",\"cache_dir\":\"-\"}", port);
    die_on(mk_node_open(cfg, &a, &err), err, "open a");
    snprintf(cfg, sizeof cfg, "{\"relays\":[\"127.0.0.1:%d\"],\"id\":\"c-bob\",\"cache_dir\":\"-\",\"listen\":true,\"listen_addr\":\"127.0.0.1:0\"}", port);
    die_on(mk_node_open(cfg, &b, &err), err, "open b");

    char* id = mk_node_id(b);
    CHECK(id && strcmp(id, "c-bob") == 0, "node id");
    mk_free(id);

    /* direct message with receipt */
    int rc = mk_send(a, "c-bob", "hello", 5, MK_SEND_RECEIPT, 5000, &err);
    CHECK(rc == MK_OK, "send with receipt");
    mk_message* m = NULL;
    rc = mk_recv(b, 5000, &m, &err);
    CHECK(rc == 1 && m && m->kind == MK_KIND_DIRECT && m->len == 5 && memcmp(m->data, "hello", 5) == 0 && strcmp(m->from, "c-alice") == 0, "receive direct");
    mk_message_free(m);

    rc = mk_send(a, "nobody", "x", 1, MK_SEND_RECEIPT, 5000, &err);
    CHECK(rc == MK_ERR_NO_ROUTE, "no route error code");
    if (err) { mk_free(err); err = NULL; }

    /* pub/sub */
    die_on(mk_subscribe(b, "sensor.*", NULL, &err), err, "subscribe");
    die_on(mk_publish(a, "sensor.temp", "21.5", 4, 0, &err), err, "publish");
    rc = mk_recv(b, 5000, &m, &err);
    CHECK(rc == 1 && m->kind == MK_KIND_PUBLISH && strcmp(m->subject, "sensor.temp") == 0, "receive publication");
    mk_message_free(m);

    /* request / reply via a served topic, answered by a responder thread */
    die_on(mk_serve(b, "svc.upper", &err), err, "serve");
    thread_t th = start_thread(responder, &b);
    void* resp = NULL; int resp_len = 0;
    rc = mk_request(a, NULL, "svc.upper", "abc", 3, 5000, &resp, &resp_len, &err);
    CHECK(rc == MK_OK && resp_len == 3 && memcmp(resp, "ABC", 3) == 0, "request to topic answered");
    mk_free(resp);
    rc = mk_request(a, "c-bob", NULL, "fail", 4, 5000, &resp, &resp_len, &err);
    CHECK(rc == MK_ERR_REMOTE && err && strstr(err, "asked to fail"), "remote error propagates");
    if (err) { mk_free(err); err = NULL; }
    join_thread(th);

    rc = mk_ping(a, 2000, NULL, &err);
    CHECK(rc == MK_OK, "ping");

    /* pipe through the relay */
    mk_pipe p = 0;
    /* b must accept: first call enables accepting, so poll once */
    mk_pipe dummy = 0;
    mk_pipe_accept(b, 0, &dummy, &err);
    rc = mk_pipe_open(a, "c-bob", "echo", "meta-1", "relay", 5000, &p, &err);
    CHECK(rc == MK_OK && p != 0, "pipe open (relay)");
    mk_pipe q = 0;
    rc = mk_pipe_accept(b, 5000, &q, &err);
    CHECK(rc == 1 && q != 0, "pipe accept");
    char* info = mk_pipe_info(q);
    CHECK(info && strstr(info, "\"peer\":\"c-alice\"") && strstr(info, "\"meta\":\"meta-1\""), "pipe info");
    mk_free(info);
    rc = mk_pipe_write(p, "ping-data", 9, 5000, &err);
    CHECK(rc == 9, "pipe write");
    mk_pipe_close_write(p);
    char buf[64]; int total = 0;
    for (;;) {
        int n = mk_pipe_read(q, buf + total, (int)sizeof(buf) - total, 5000, &err);
        if (n <= 0) break;
        total += n;
    }
    CHECK(total == 9 && memcmp(buf, "ping-data", 9) == 0, "pipe read until EOF");
    mk_pipe_close(p);
    mk_pipe_close(q);

    /* file share + download */
    const char* path = "capi_test_file.bin";
    FILE* f = fopen(path, "wb");
    for (int i = 0; i < 3 * 1024 * 1024; i++) fputc((i * 131) & 0xFF, f);
    fclose(f);
    char* shared = NULL;
    rc = mk_share(b, path, 0, &shared, &err);
    CHECK(rc == MK_OK && shared && strstr(shared, "magnetk:?"), "share");
    char link[1024] = {0};
    char* ls = shared ? strstr(shared, "\"link\":\"") : NULL;
    if (ls) {
        ls += 8;
        char* le = strchr(ls, '"');
        if (le && le - ls < (long)sizeof(link)) memcpy(link, ls, (size_t)(le - ls));
    }
    /* JSON escapes & as & */
    {
        char clean[1024]; int j = 0;
        for (int i = 0; link[i] && j < 1000; i++) {
            if (strncmp(link + i, "\\u0026", 6) == 0) { clean[j++] = '&'; i += 5; }
            else clean[j++] = link[i];
        }
        clean[j] = 0;
        strcpy(link, clean);
    }
    mk_free(shared);
    mk_download dl = 0;
    rc = mk_download_start(a, link, "capi_test_out.bin", "{\"route\":\"auto\"}", &dl, &err);
    CHECK(rc == MK_OK, "download start");
    char* prog = NULL;
    int st = 0;
    for (int i = 0; i < 100 && st == 0; i++) {
        st = mk_download_poll(dl, 200, &prog, &err);
        if (prog) { if (st != 0) printf("     progress: %s\n", prog); mk_free(prog); prog = NULL; }
    }
    CHECK(st == 1, "download finished");
    if (err) { fprintf(stderr, "download error: %s\n", err); mk_free(err); err = NULL; }
    mk_download_free(dl);
    /* compare */
    FILE* f1 = fopen(path, "rb"); FILE* f2 = fopen("capi_test_out.bin", "rb");
    int same = f1 && f2;
    while (same) {
        int c1 = fgetc(f1), c2 = fgetc(f2);
        if (c1 != c2) same = 0;
        if (c1 == EOF || c2 == EOF) break;
    }
    if (f1) fclose(f1);
    if (f2) fclose(f2);
    CHECK(same, "downloaded file identical");

    char* stats = mk_relay_stats_json(relay);
    CHECK(stats && strstr(stats, "\"nodes\":2"), "relay stats");
    mk_free(stats);

    /* secure chat: two nodes with identity keys, end-to-end encrypted */
    mk_node ca, cb;
    snprintf(cfg, sizeof cfg, "{\"relays\":[\"127.0.0.1:%d\"],\"id\":\"c-carol\",\"cache_dir\":\"-\",\"identity_file\":\"capi_test_carol.key\"}", port);
    die_on(mk_node_open(cfg, &ca, &err), err, "open carol");
    snprintf(cfg, sizeof cfg, "{\"relays\":[\"127.0.0.1:%d\"],\"id\":\"c-dave\",\"cache_dir\":\"-\",\"identity_file\":\"capi_test_dave.key\",\"chat_dir\":\".\"}", port);
    die_on(mk_node_open(cfg, &cb, &err), err, "open dave");
    char* ident = NULL;
    rc = mk_identity(ca, &ident, &err);
    CHECK(rc == MK_OK && ident && strstr(ident, "magnetk://c-carol@") && strstr(ident, "key="), "identity address");
    mk_free(ident);
    char* sent = NULL;
    rc = mk_chat_send(ca, "c-dave", "secret hello", NULL, 0, NULL, 0, &sent, &err);
    CHECK(rc == MK_OK && sent && strstr(sent, "\"encrypted\":true"), "chat send encrypted");
    mk_free(sent);
    rc = mk_recv(cb, 5000, &m, &err);
    CHECK(rc == 1 && m->kind == MK_KIND_CHAT && strstr((char*)m->data, "secret hello") && strstr((char*)m->data, "\"encrypted\":true"), "chat received and decrypted");
    if (rc == 1) mk_message_free(m);
    rc = mk_recv(ca, 5000, &m, &err);
    CHECK(rc == 1 && m->kind == MK_KIND_CHAT && strcmp(m->subject, "ack") == 0, "chat ack");
    if (rc == 1) mk_message_free(m);
    char* sn1 = NULL; char* sn2 = NULL;
    mk_chat_contact(ca, "safety", "c-dave", &sn1, &err);
    mk_chat_contact(cb, "safety", "c-carol", &sn2, &err);
    CHECK(sn1 && sn2 && strcmp(strstr(sn1, "safety_number"), strstr(sn2, "safety_number")) == 0, "same safety number on both sides");
    mk_free(sn1); mk_free(sn2);
    mk_node_close(ca);
    mk_node_close(cb);
    remove("capi_test_carol.key"); remove("capi_test_dave.key"); remove("known-peers.json");

    mk_node_close(a);
    mk_node_close(b);
    mk_relay_close(relay);
    remove(path);
    remove("capi_test_out.bin");
    if (failures) { printf("%d FAILURE(S)\n", failures); return 1; }
    printf("ALL C API TESTS PASSED\n");
    return 0;
}
