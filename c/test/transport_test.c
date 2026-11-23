/*
 * Transport layer self-test: echo, bulk throughput, EOF/close semantics,
 * datagrams and timeouts over loopback.
 *
 *   transport_test [megabytes]
 */
#include "transport.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#ifdef _WIN32
#include <windows.h>
typedef HANDLE thread_t;
static double now_s(void) { LARGE_INTEGER f, c; QueryPerformanceFrequency(&f); QueryPerformanceCounter(&c); return (double)c.QuadPart / (double)f.QuadPart; }
static thread_t spawn(DWORD (WINAPI *fn)(LPVOID), void* arg) { return CreateThread(NULL, 0, fn, arg, 0, NULL); }
static void join(thread_t t) { WaitForSingleObject(t, INFINITE); CloseHandle(t); }
#define THREAD_FN(name) static DWORD WINAPI name(LPVOID arg)
#define THREAD_RET return 0
#else
#include <pthread.h>
#include <time.h>
typedef pthread_t thread_t;
static double now_s(void) { struct timespec ts; clock_gettime(CLOCK_MONOTONIC, &ts); return ts.tv_sec + ts.tv_nsec / 1e9; }
static thread_t spawn(void* (*fn)(void*), void* arg) { pthread_t t; pthread_create(&t, NULL, fn, arg); return t; }
static void join(thread_t t) { pthread_join(t, NULL); }
#define THREAD_FN(name) static void* name(void* arg)
#define THREAD_RET return NULL
#endif

static int failures = 0;
#define CHECK(cond, msg) do { if (!(cond)) { fprintf(stderr, "FAIL: %s (line %d)\n", msg, __LINE__); failures++; } } while (0)

typedef struct {
    magnetk_listener_t* listener;
    uint64_t total;
} server_ctx;

/* Server: accept one connection; stream 1 = echo, stream 2 = sink (count bytes, reply 8-byte total). */
THREAD_FN(server_main) {
    server_ctx* ctx = (server_ctx*)arg;
    magnetk_connection_t* conn = NULL;
    magnetk_status_t st = magnetk_listener_accept(ctx->listener, 10000, &conn);
    if (st != MAGNETK_OK) { fprintf(stderr, "accept conn: %s\n", magnetk_status_string(st)); THREAD_RET; }

    static uint8_t buf[1 << 20];
    for (int i = 0; i < 2; i++) {
        magnetk_stream_t* s = NULL;
        st = magnetk_connection_accept_stream(conn, 10000, &s);
        if (st != MAGNETK_OK) { fprintf(stderr, "accept stream: %s\n", magnetk_status_string(st)); break; }
        uint64_t total = 0;
        for (;;) {
            uint32_t n = 0;
            st = magnetk_stream_recv(s, buf, sizeof(buf), &n, 30000);
            if (st != MAGNETK_OK) break;
            total += n;
            if (i == 0) magnetk_stream_send(s, buf, n, 0, MAGNETK_INFINITE);
        }
        CHECK(st == MAGNETK_ERR_CLOSED, "server stream should end with EOF");
        if (i == 0) {
            magnetk_stream_shutdown_send(s);
        } else {
            ctx->total = total;
            magnetk_stream_send(s, (const uint8_t*)&total, sizeof(total), 1, MAGNETK_INFINITE);
        }
        magnetk_stream_flush(s, 10000);
        magnetk_stream_close(s);
    }

    /* datagram echo */
    uint8_t d[1500];
    uint32_t dn = 0;
    if (magnetk_connection_recv_datagram(conn, d, sizeof(d), &dn, 5000) == MAGNETK_OK) {
        magnetk_connection_send_datagram(conn, d, dn);
    }
    /* wait for client to hang up */
    magnetk_stream_t* none = NULL;
    st = magnetk_connection_accept_stream(conn, 10000, &none);
    CHECK(st == MAGNETK_ERR_CLOSED, "server should observe connection close");
    magnetk_connection_close(conn);
    THREAD_RET;
}

int main(int argc, char** argv) {
    int mb = argc > 1 ? atoi(argv[1]) : 256;

    magnetk_config_t cfg;
    magnetk_config_default(&cfg);
    cfg.bind_address = "127.0.0.1";
    cfg.port = 0;
    /* Tuning experiments: MK_PROFILE=0..3 MK_CC=0|1 MK_WINDOW_MB=n MK_CHUNK_KB=n */
    if (getenv("MK_PROFILE")) cfg.execution_profile = atoi(getenv("MK_PROFILE"));
    if (getenv("MK_CC")) cfg.congestion_control = atoi(getenv("MK_CC"));
    if (getenv("MK_WINDOW_MB")) {
        cfg.send_buffer_size = (uint32_t)atoi(getenv("MK_WINDOW_MB")) << 20;
        cfg.recv_buffer_size = cfg.send_buffer_size;
    }
    const char* host = getenv("MK_HOST") ? getenv("MK_HOST") : "127.0.0.1";
    if (getenv("MK_HOST")) cfg.bind_address = "0.0.0.0";

    magnetk_transport_t* server_t = NULL;
    magnetk_transport_t* client_t = NULL;
    CHECK(magnetk_transport_init(&cfg, &server_t) == MAGNETK_OK, "server transport init");
    CHECK(magnetk_transport_init(&cfg, &client_t) == MAGNETK_OK, "client transport init");
    if (!server_t || !client_t) return 1;

    magnetk_listener_t* l = NULL;
    CHECK(magnetk_listen(server_t, &l) == MAGNETK_OK, "listen");
    uint16_t port = magnetk_listener_port(l);
    CHECK(port != 0, "ephemeral port assigned");
    printf("listening on 127.0.0.1:%u\n", port);

    /* accept with timeout and nothing pending */
    magnetk_connection_t* none = NULL;
    CHECK(magnetk_listener_accept(l, 0, &none) == MAGNETK_WOULD_BLOCK, "non-blocking accept");

    /* connect to a closed port must fail fast */
    magnetk_connection_t* bad = NULL;
    double t0 = now_s();
    magnetk_status_t bst = magnetk_connect(client_t, "127.0.0.1", 1, 3000, &bad);
    CHECK(bst != MAGNETK_OK, "connect to closed port fails");
    printf("connect to closed port: %s after %.2fs\n", magnetk_status_string(bst), now_s() - t0);

    server_ctx sctx = { l, 0 };
    thread_t srv = spawn(server_main, &sctx);

    magnetk_connection_t* conn = NULL;
    t0 = now_s();
    magnetk_status_t st = magnetk_connect(client_t, host, port, 5000, &conn);
    CHECK(st == MAGNETK_OK, "connect");
    printf("handshake: %.2f ms\n", (now_s() - t0) * 1000);
    if (st != MAGNETK_OK) return 1;
    char remote[64];
    magnetk_connection_remote_addr(conn, remote, sizeof(remote));
    printf("remote: %s\n", remote);

    /* ---- echo ---- */
    magnetk_stream_t* s = NULL;
    CHECK(magnetk_stream_open(conn, &s) == MAGNETK_OK, "open echo stream");
    const char* msg = "hello magnetk";
    CHECK(magnetk_stream_send(s, (const uint8_t*)msg, (uint32_t)strlen(msg), 1, 5000) == MAGNETK_OK, "send echo");
    char echo[64] = {0};
    uint32_t got = 0, n = 0;
    while (got < strlen(msg)) {
        st = magnetk_stream_recv(s, (uint8_t*)echo + got, sizeof(echo) - 1 - got, &n, 5000);
        if (st != MAGNETK_OK) break;
        got += n;
    }
    CHECK(strcmp(echo, msg) == 0, "echo payload matches");
    st = magnetk_stream_recv(s, (uint8_t*)echo, sizeof(echo), &n, 5000);
    CHECK(st == MAGNETK_ERR_CLOSED, "echo stream EOF after FIN");
    magnetk_stream_close(s);

    /* ---- bulk ---- */
    CHECK(magnetk_stream_open(conn, &s) == MAGNETK_OK, "open bulk stream");
    uint32_t chunk = (getenv("MK_CHUNK_KB") ? (uint32_t)atoi(getenv("MK_CHUNK_KB")) : 256) * 1024;
    uint8_t* data = (uint8_t*)malloc(chunk);
    for (uint32_t i = 0; i < chunk; i++) data[i] = (uint8_t)(i * 31);
    uint64_t want = (uint64_t)mb * 1024 * 1024;
    uint64_t sent = 0;
    t0 = now_s();
    while (sent < want) {
        st = magnetk_stream_send(s, data, chunk, 0, 30000);
        if (st != MAGNETK_OK) { fprintf(stderr, "bulk send: %s\n", magnetk_status_string(st)); break; }
        sent += chunk;
    }
    magnetk_stream_shutdown_send(s);
    uint64_t echoed = 0;
    got = 0;
    while (got < sizeof(echoed)) {
        st = magnetk_stream_recv(s, (uint8_t*)&echoed + got, sizeof(echoed) - got, &n, 30000);
        if (st != MAGNETK_OK) break;
        got += n;
    }
    double dt = now_s() - t0;
    CHECK(echoed == want, "server received every byte");
    printf("bulk: %llu MB in %.2fs = %.1f MB/s (%.2f Gbit/s)\n",
           (unsigned long long)(want >> 20), dt, (double)want / dt / 1048576.0, (double)want * 8 / dt / 1e9);
    magnetk_stream_close(s);
    free(data);

    /* ---- datagram ---- */
    for (int i = 0; i < 50 && magnetk_connection_max_datagram(conn) == 0; i++) {
#ifdef _WIN32
        Sleep(10);
#endif
    }
    uint32_t maxd = magnetk_connection_max_datagram(conn);
    printf("max datagram: %u\n", maxd);
    CHECK(maxd > 0, "datagrams negotiated");
    st = magnetk_connection_send_datagram(conn, (const uint8_t*)"ping", 4);
    CHECK(st == MAGNETK_OK, "send datagram");
    uint8_t d[1500];
    uint32_t dn = 0;
    st = magnetk_connection_recv_datagram(conn, d, sizeof(d), &dn, 5000);
    CHECK(st == MAGNETK_OK && dn == 4 && memcmp(d, "ping", 4) == 0, "datagram echo");

    magnetk_conn_stats_t stats;
    if (magnetk_connection_stats(conn, &stats) == MAGNETK_OK) {
        printf("stats: rtt=%uus cwnd=%u sent=%llu lost=%llu\n", stats.rtt_us, stats.congestion_window,
               (unsigned long long)stats.send_bytes, (unsigned long long)stats.lost_packets);
    }

    magnetk_connection_close(conn);
    join(srv);
    magnetk_listener_close(l);
    magnetk_transport_shutdown(client_t);
    magnetk_transport_shutdown(server_t);

    if (failures) { printf("%d FAILURE(S)\n", failures); return 1; }
    printf("ALL TRANSPORT TESTS PASSED\n");
    return 0;
}
