/*
 * Magnetk Transport Layer v2 - MsQuic implementation.
 *
 * Object lifetime
 * ---------------
 * listener   refs: app handle + one per server connection still in handshake
 * connection refs: MsQuic (until SHUTDOWN_COMPLETE) + app handle + one per
 *                  stream object that belongs to it
 * stream     refs: MsQuic (until SHUTDOWN_COMPLETE) + app handle (an accepted
 *                  stream's app ref is owned by the accept queue until handed out)
 *
 * The MsQuic handle of an object is closed only when its last reference is
 * dropped, so every function that receives a live application handle may
 * call into MsQuic without racing a shutdown callback.
 *
 * Receive path: RECEIVE events are copied into a per-stream ring buffer in
 * the callback and acknowledged immediately, so MsQuic never waits for the
 * application. When the ring is full the callback accepts only part of the
 * data, which pauses delivery: QUIC flow control then pushes back on the
 * sender and memory stays bounded. Readers re-enable delivery once the ring
 * has drained to half.
 *
 * Send path: one heap block per send (descriptor + payload), freed on
 * SEND_COMPLETE (= acknowledged, since MsQuic send buffering is disabled).
 * The unacked byte count per stream is capped by max(send window, ideal
 * send buffer reported by MsQuic).
 */

#ifdef _WIN32
  #ifndef WIN32_LEAN_AND_MEAN
    #define WIN32_LEAN_AND_MEAN
  #endif
  #ifndef _WIN32_WINNT
    #define _WIN32_WINNT 0x0A00
  #endif
  #include <winsock2.h>
  #include <ws2tcpip.h>
  #include <windows.h>
  #include <wincrypt.h>
  #include <ncrypt.h>
#else
  #include <pthread.h>
  #include <time.h>
  #include <errno.h>
  #include <arpa/inet.h>
  #include <netinet/in.h>
  #include <sys/socket.h>
#endif

/* BBR congestion control is declared behind MsQuic's preview flag. */
#ifndef QUIC_API_ENABLE_PREVIEW_FEATURES
  #define QUIC_API_ENABLE_PREVIEW_FEATURES 1
#endif
#include <msquic.h>
#include <stdlib.h>
#include <string.h>
#include <stdio.h>

#include "transport.h"

/* ===================================================================== */
/* Platform primitives                                                    */
/* ===================================================================== */

#ifdef _WIN32
typedef SRWLOCK            mk_lock;
typedef CONDITION_VARIABLE mk_cond;
static void mk_lock_init(mk_lock* l)     { InitializeSRWLock(l); }
static void mk_lock_destroy(mk_lock* l)  { (void)l; }
static void mk_lock_acquire(mk_lock* l)  { AcquireSRWLockExclusive(l); }
static void mk_lock_release(mk_lock* l)  { ReleaseSRWLockExclusive(l); }
static void mk_cond_init(mk_cond* c)     { InitializeConditionVariable(c); }
static void mk_cond_destroy(mk_cond* c)  { (void)c; }
static void mk_cond_broadcast(mk_cond* c){ WakeAllConditionVariable(c); }
static void mk_cond_wait(mk_cond* c, mk_lock* l, uint32_t ms) {
    SleepConditionVariableSRW(c, l, ms == MAGNETK_INFINITE ? INFINITE : ms, 0);
}
static uint64_t mk_now_ms(void) { return GetTickCount64(); }
static long mk_inc(volatile long* p) { return InterlockedIncrement(p); }
static long mk_dec(volatile long* p) { return InterlockedDecrement(p); }
#else
typedef pthread_mutex_t mk_lock;
typedef pthread_cond_t  mk_cond;
static void mk_lock_init(mk_lock* l)     { pthread_mutex_init(l, NULL); }
static void mk_lock_destroy(mk_lock* l)  { pthread_mutex_destroy(l); }
static void mk_lock_acquire(mk_lock* l)  { pthread_mutex_lock(l); }
static void mk_lock_release(mk_lock* l)  { pthread_mutex_unlock(l); }
static void mk_cond_init(mk_cond* c) {
#if defined(__APPLE__)
    pthread_cond_init(c, NULL);
#else
    pthread_condattr_t attr;
    pthread_condattr_init(&attr);
    pthread_condattr_setclock(&attr, CLOCK_MONOTONIC);
    pthread_cond_init(c, &attr);
    pthread_condattr_destroy(&attr);
#endif
}
static void mk_cond_destroy(mk_cond* c)  { pthread_cond_destroy(c); }
static void mk_cond_broadcast(mk_cond* c){ pthread_cond_broadcast(c); }
static void mk_cond_wait(mk_cond* c, mk_lock* l, uint32_t ms) {
    if (ms == MAGNETK_INFINITE) { pthread_cond_wait(c, l); return; }
#if defined(__APPLE__)
    struct timespec rel = { (time_t)(ms / 1000), (long)(ms % 1000) * 1000000L };
    pthread_cond_timedwait_relative_np(c, l, &rel);
#else
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    ts.tv_sec  += ms / 1000;
    ts.tv_nsec += (long)(ms % 1000) * 1000000L;
    if (ts.tv_nsec >= 1000000000L) { ts.tv_sec++; ts.tv_nsec -= 1000000000L; }
    pthread_cond_timedwait(c, l, &ts);
#endif
}
static uint64_t mk_now_ms(void) {
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return (uint64_t)ts.tv_sec * 1000u + (uint64_t)ts.tv_nsec / 1000000u;
}
static long mk_inc(volatile long* p) { return __atomic_add_fetch(p, 1, __ATOMIC_ACQ_REL); }
static long mk_dec(volatile long* p) { return __atomic_sub_fetch(p, 1, __ATOMIC_ACQ_REL); }
#endif

static uint64_t mk_deadline(uint32_t timeout_ms) {
    if (timeout_ms == MAGNETK_INFINITE) return UINT64_MAX;
    return mk_now_ms() + timeout_ms;
}

/* Remaining wait in ms (MAGNETK_INFINITE for no deadline, 0 when expired). */
static uint32_t mk_remaining(uint64_t deadline) {
    if (deadline == UINT64_MAX) return MAGNETK_INFINITE;
    uint64_t now = mk_now_ms();
    if (now >= deadline) return 0;
    uint64_t left = deadline - now;
    return left > 0x7FFFFFFFu ? 0x7FFFFFFFu : (uint32_t)left;
}

static char* mk_strdup(const char* s) {
    if (!s || !*s) return NULL;
    size_t n = strlen(s) + 1;
    char* out = (char*)malloc(n);
    if (out) memcpy(out, s, n);
    return out;
}

static uint32_t mk_pow2_ceil(uint32_t v) {
    if (v < 4096) return 4096;
    if (v > 0x40000000u) return 0x40000000u;
    v--;
    v |= v >> 1; v |= v >> 2; v |= v >> 4; v |= v >> 8; v |= v >> 16;
    return v + 1;
}

/* ===================================================================== */
/* Internal structures                                                    */
/* ===================================================================== */

typedef struct mk_send_block {
    QUIC_BUFFER qb;
    uint32_t    length;
    /* payload follows */
} mk_send_block;

typedef struct mk_datagram {
    uint8_t* data;
    uint32_t length;
} mk_datagram;

#define MK_DGRAM_QUEUE_CAP 256

struct magnetk_transport {
    const QUIC_API_TABLE* api;
    HQUIC                 registration;
    HQUIC                 config_client;
    HQUIC                 config_server;   /* created on first listen */
    mk_lock               lock;
    QUIC_SETTINGS         settings;
    QUIC_BUFFER           alpn;
    char                  alpn_str[256];
    char*                 bind_address;
    char*                 cert_file;
    char*                 key_file;
    char*                 cert_hash;
    int                   cert_machine_store;
    uint16_t              port;
    uint32_t              max_connections;
    uint32_t              send_window;
    uint32_t              ring_max;
    magnetk_notify_fn     notify;
    volatile long         server_conns;
#ifdef _WIN32
    PCCERT_CONTEXT        cert_context;
#endif
};

struct magnetk_listener {
    volatile long         refs;
    magnetk_transport_t*  t;
    HQUIC                 handle;
    mk_lock               lock;
    mk_cond               cond;
    uint64_t              token;
    int                   closed;
    magnetk_connection_t* queue_head;
    magnetk_connection_t* queue_tail;
    uint32_t              queue_len;
};

struct magnetk_connection {
    volatile long         refs;
    magnetk_transport_t*  t;
    HQUIC                 handle;
    mk_lock               lock;
    mk_cond               cond;
    uint64_t              token;
    int                   is_server;
    int                   connected;
    int                   shutting_down;
    int                   shutdown_complete;
    int                   app_released;
    int                   msquic_ref;
    magnetk_status_t      close_reason;
    magnetk_listener_t*   pending_listener;  /* server conn until handshake done */
    magnetk_connection_t* next;              /* listener accept queue link */
    magnetk_stream_t*     sq_head;           /* accepted-stream queue */
    magnetk_stream_t*     sq_tail;
    uint32_t              sq_len;
    magnetk_stream_t*     live_head;         /* streams still holding a MsQuic ref */
    mk_datagram           dq[MK_DGRAM_QUEUE_CAP];
    uint32_t              dq_head;
    uint32_t              dq_len;
    int                   dgram_send_enabled;
    uint32_t              dgram_max;
    char                  remote[64];
};

struct magnetk_stream {
    volatile long         refs;
    magnetk_connection_t* conn;
    const QUIC_API_TABLE* api;
    HQUIC                 handle;
    mk_lock               lock;
    mk_cond               cond;
    uint64_t              token;
    uint64_t              id;
    magnetk_stream_t*     next;             /* connection accept queue link */
    magnetk_stream_t*     live_prev;        /* conn->live_head list (conn->lock) */
    magnetk_stream_t*     live_next;
    int                   live_linked;
    int                   msquic_ref;
    int                   shutdown_complete;
    int                   app_closed;
    /* receive */
    uint8_t*              rx_ring;           /* received, not yet read */
    uint32_t              rx_cap;
    uint32_t              rx_head;
    uint32_t              rx_len;
    uint32_t              rx_max;            /* ring limit; beyond it MsQuic is paused */
    int                   rx_paused;         /* receive callbacks disabled (ring full) */
    int                   rx_fin;
    int                   rx_reset;
    /* send */
    uint64_t              tx_inflight;
    uint64_t              tx_limit;
    int                   tx_error;
    int                   tx_fin;
};

static QUIC_STATUS QUIC_API listener_callback(HQUIC, void*, QUIC_LISTENER_EVENT*);
static QUIC_STATUS QUIC_API conn_callback(HQUIC, void*, QUIC_CONNECTION_EVENT*);
static QUIC_STATUS QUIC_API stream_callback(HQUIC, void*, QUIC_STREAM_EVENT*);
static QUIC_STATUS QUIC_API refused_stream_callback(HQUIC, void*, QUIC_STREAM_EVENT*);

static void mk_notify(magnetk_transport_t* t, uint64_t token, uint32_t events) {
    magnetk_notify_fn fn = t->notify;
    if (fn && token) fn(token, events);
}

/* MAGNETK_TRACE=1 prints object lifecycle events to stderr (debugging). */
static int mk_trace_on = -1;
static int mk_tracing(void) {
    if (mk_trace_on < 0) {
        const char* v = getenv("MAGNETK_TRACE");
        mk_trace_on = (v && *v && *v != '0') ? 1 : 0;
    }
    return mk_trace_on;
}
#define MK_TRACE(...) do { if (mk_tracing()) { fprintf(stderr, "[magnetk-trace] " __VA_ARGS__); fputc(0x0A, stderr); } } while (0)

/* ===================================================================== */
/* Utility                                                                */
/* ===================================================================== */

MAGNETK_API const char* magnetk_status_string(magnetk_status_t status) {
    switch (status) {
        case MAGNETK_OK:                 return "OK";
        case MAGNETK_ERR_INIT:           return "initialization failed";
        case MAGNETK_ERR_CONFIG:         return "configuration error";
        case MAGNETK_ERR_LISTEN:         return "listen failed";
        case MAGNETK_ERR_CONNECT:        return "connection failed";
        case MAGNETK_ERR_SEND:           return "send failed";
        case MAGNETK_ERR_RECV:           return "receive failed";
        case MAGNETK_ERR_TIMEOUT:        return "timeout";
        case MAGNETK_ERR_CLOSED:         return "closed";
        case MAGNETK_ERR_TLS:            return "TLS error";
        case MAGNETK_ERR_MEMORY:         return "out of memory";
        case MAGNETK_ERR_INVALID_PARAM:  return "invalid parameter";
        case MAGNETK_ERR_STREAM:         return "stream error";
        case MAGNETK_ERR_NOT_READY:      return "not ready";
        case MAGNETK_WOULD_BLOCK:        return "would block";
        case MAGNETK_ERR_RESET:          return "stream reset by peer";
        case MAGNETK_ERR_TOO_LARGE:      return "datagram too large";
        default:                         return "unknown error";
    }
}

MAGNETK_API void magnetk_config_default(magnetk_config_t* config) {
    if (!config) return;
    memset(config, 0, sizeof(*config));
    config->bind_address         = "0.0.0.0";
    config->port                 = 4002;
    config->alpn                 = "magnetk/2";
    config->no_cert_validation   = 1;
    config->max_connections      = 10000;
    config->max_streams_per_conn = 1024;
    config->idle_timeout_ms      = 60000;
    config->handshake_timeout_ms = 10000;
    config->keepalive_ms         = 15000;
    config->send_buffer_size     = 8u * 1024u * 1024u;
    config->recv_buffer_size     = 8u * 1024u * 1024u;
    config->conn_flow_window     = 0;
    config->execution_profile    = MAGNETK_PROFILE_MAX_THROUGHPUT;
    config->congestion_control   = MAGNETK_CC_CUBIC;
    config->enable_datagrams     = 1;
}

static magnetk_status_t mk_map_quic_status(QUIC_STATUS st) {
    if (st == QUIC_STATUS_CONNECTION_TIMEOUT || st == QUIC_STATUS_CONNECTION_IDLE) return MAGNETK_ERR_TIMEOUT;
    if (st == QUIC_STATUS_ALPN_NEG_FAILURE || st == QUIC_STATUS_BAD_CERTIFICATE ||
        st == QUIC_STATUS_TLS_ERROR || st == QUIC_STATUS_HANDSHAKE_FAILURE ||
        st == QUIC_STATUS_CERT_EXPIRED || st == QUIC_STATUS_CERT_UNTRUSTED_ROOT) return MAGNETK_ERR_TLS;
    return MAGNETK_ERR_CONNECT;
}

static void mk_addr_to_string(const QUIC_ADDR* a, char* out, size_t n) {
    char ip[64] = {0};
    out[0] = 0;
#ifdef _WIN32
    int fam = a->si_family;
#else
    int fam = a->Ip.sa_family;
#endif
    if (fam == AF_INET) {
        inet_ntop(AF_INET, (void*)&a->Ipv4.sin_addr, ip, sizeof(ip));
        snprintf(out, n, "%s:%u", ip, (unsigned)ntohs(a->Ipv4.sin_port));
    } else if (fam == AF_INET6) {
        inet_ntop(AF_INET6, (void*)&a->Ipv6.sin6_addr, ip, sizeof(ip));
        snprintf(out, n, "[%s]:%u", ip, (unsigned)ntohs(a->Ipv6.sin6_port));
    }
}

static int mk_parse_bind(const char* s, uint16_t port, QUIC_ADDR* a) {
    memset(a, 0, sizeof(*a));
    if (!s || !*s || strcmp(s, "0.0.0.0") == 0 || strcmp(s, "::") == 0 || strcmp(s, "*") == 0) {
#ifdef _WIN32
        a->si_family = QUIC_ADDRESS_FAMILY_UNSPEC;
#else
        a->Ip.sa_family = QUIC_ADDRESS_FAMILY_UNSPEC;
#endif
    } else if (inet_pton(AF_INET, s, &a->Ipv4.sin_addr) == 1) {
#ifdef _WIN32
        a->si_family = AF_INET;
#else
        a->Ip.sa_family = AF_INET;
#endif
    } else if (inet_pton(AF_INET6, s, &a->Ipv6.sin6_addr) == 1) {
#ifdef _WIN32
        a->si_family = AF_INET6;
#else
        a->Ip.sa_family = AF_INET6;
#endif
    } else {
        return 0;
    }
    a->Ipv4.sin_port = htons(port);  /* same offset for IPv6 */
    return 1;
}

/* ===================================================================== */
/* Windows certificates                                                   */
/* ===================================================================== */

#ifdef _WIN32
/* Self-signed certificate backed by a single persisted CNG key that is
 * reused across runs (older versions created a new key container on every
 * start and never deleted it). */
static PCCERT_CONTEXT mk_win_self_signed(void) {
    const wchar_t* container = L"magnetk-self-signed-v2";
    NCRYPT_PROV_HANDLE prov = 0;
    NCRYPT_KEY_HANDLE key = 0;
    PCCERT_CONTEXT cert = NULL;
    CERT_NAME_BLOB subject = {0};

    if (NCryptOpenStorageProvider(&prov, MS_KEY_STORAGE_PROVIDER, 0) != ERROR_SUCCESS) return NULL;

    if (NCryptOpenKey(prov, &key, container, 0, NCRYPT_SILENT_FLAG) != ERROR_SUCCESS) {
        key = 0;
        if (NCryptCreatePersistedKey(prov, &key, NCRYPT_RSA_ALGORITHM, container, 0, 0) != ERROR_SUCCESS) goto out;
        DWORD bits = 2048;
        NCryptSetProperty(key, NCRYPT_LENGTH_PROPERTY, (PBYTE)&bits, sizeof(bits), 0);
        if (NCryptFinalizeKey(key, NCRYPT_SILENT_FLAG) != ERROR_SUCCESS) goto out;
    }

    if (!CertStrToNameW(X509_ASN_ENCODING, L"CN=magnetk", CERT_X500_NAME_STR, NULL, NULL, &subject.cbData, NULL)) goto out;
    subject.pbData = (BYTE*)malloc(subject.cbData);
    if (!subject.pbData) goto out;
    if (!CertStrToNameW(X509_ASN_ENCODING, L"CN=magnetk", CERT_X500_NAME_STR, NULL, subject.pbData, &subject.cbData, NULL)) goto out;

    CRYPT_KEY_PROV_INFO kpi;
    memset(&kpi, 0, sizeof(kpi));
    kpi.pwszContainerName = (LPWSTR)container;
    kpi.pwszProvName = (LPWSTR)MS_KEY_STORAGE_PROVIDER;
    kpi.dwFlags = NCRYPT_SILENT_FLAG;
    kpi.dwKeySpec = AT_KEYEXCHANGE;

    CRYPT_ALGORITHM_IDENTIFIER sig = { (LPSTR)szOID_RSA_SHA256RSA, {0, NULL} };

    SYSTEMTIME start, end;
    FILETIME ft;
    ULARGE_INTEGER u;
    GetSystemTimeAsFileTime(&ft);
    u.LowPart = ft.dwLowDateTime; u.HighPart = ft.dwHighDateTime;
    u.QuadPart -= 24ull * 3600ull * 10000000ull;             /* tolerate clock skew */
    ft.dwLowDateTime = u.LowPart; ft.dwHighDateTime = u.HighPart;
    FileTimeToSystemTime(&ft, &start);
    u.QuadPart += 2ull * 365ull * 24ull * 3600ull * 10000000ull;
    ft.dwLowDateTime = u.LowPart; ft.dwHighDateTime = u.HighPart;
    FileTimeToSystemTime(&ft, &end);

    cert = CertCreateSelfSignCertificate(key, &subject, 0, &kpi, &sig, &start, &end, NULL);

out:
    if (subject.pbData) free(subject.pbData);
    if (key) NCryptFreeObject(key);
    if (prov) NCryptFreeObject(prov);
    return cert;
}

static int mk_hex_to_bytes(const char* hex, uint8_t* out, size_t out_len) {
    size_t n = 0;
    for (const char* p = hex; *p && n < out_len * 2; p++) {
        char c = *p;
        int v;
        if (c >= '0' && c <= '9') v = c - '0';
        else if (c >= 'a' && c <= 'f') v = c - 'a' + 10;
        else if (c >= 'A' && c <= 'F') v = c - 'A' + 10;
        else if (c == ' ' || c == ':') continue;
        else return 0;
        if (n % 2 == 0) out[n / 2] = (uint8_t)(v << 4); else out[n / 2] |= (uint8_t)v;
        n++;
    }
    return n == out_len * 2;
}
#endif

/* Creates the server configuration on first use (client-only transports
 * never pay for certificate generation). Caller holds t->lock. */
static magnetk_status_t mk_ensure_server_config(magnetk_transport_t* t) {
    if (t->config_server) return MAGNETK_OK;

    HQUIC cfg = NULL;
    QUIC_STATUS st = t->api->ConfigurationOpen(t->registration, &t->alpn, 1,
                                               &t->settings, sizeof(t->settings), NULL, &cfg);
    if (QUIC_FAILED(st)) {
        fprintf(stderr, "[magnetk] server ConfigurationOpen failed: 0x%x\n", (unsigned)st);
        return MAGNETK_ERR_CONFIG;
    }

    QUIC_CREDENTIAL_CONFIG cred;
    memset(&cred, 0, sizeof(cred));

#ifdef _WIN32
    QUIC_CERTIFICATE_HASH_STORE hs;
    memset(&hs, 0, sizeof(hs));
    if (t->cert_hash) {
        if (!mk_hex_to_bytes(t->cert_hash, hs.ShaHash, sizeof(hs.ShaHash))) {
            fprintf(stderr, "[magnetk] cert_hash must be a 40 character SHA-1 thumbprint\n");
            t->api->ConfigurationClose(cfg);
            return MAGNETK_ERR_TLS;
        }
        strncpy(hs.StoreName, "My", sizeof(hs.StoreName) - 1);
        hs.Flags = t->cert_machine_store ? QUIC_CERTIFICATE_HASH_STORE_FLAG_MACHINE_STORE
                                         : QUIC_CERTIFICATE_HASH_STORE_FLAG_NONE;
        cred.Type = QUIC_CREDENTIAL_TYPE_CERTIFICATE_HASH_STORE;
        cred.CertificateHashStore = &hs;
    } else {
        if (t->cert_file && t->key_file) {
            fprintf(stderr, "[magnetk] note: Schannel cannot load PEM files; using a self-signed "
                            "certificate (set cert_hash to use a store certificate)\n");
        }
        if (!t->cert_context) t->cert_context = mk_win_self_signed();
        if (!t->cert_context) {
            fprintf(stderr, "[magnetk] failed to create self-signed certificate\n");
            t->api->ConfigurationClose(cfg);
            return MAGNETK_ERR_TLS;
        }
        cred.Type = QUIC_CREDENTIAL_TYPE_CERTIFICATE_CONTEXT;
        cred.CertificateContext = (QUIC_CERTIFICATE*)t->cert_context;
    }
#else
    QUIC_CERTIFICATE_FILE cf;
    memset(&cf, 0, sizeof(cf));
    if (!t->cert_file || !t->key_file) {
        fprintf(stderr, "[magnetk] listening requires cert_file and key_file (PEM) on this platform\n");
        t->api->ConfigurationClose(cfg);
        return MAGNETK_ERR_TLS;
    }
    cf.CertificateFile = t->cert_file;
    cf.PrivateKeyFile = t->key_file;
    cred.Type = QUIC_CREDENTIAL_TYPE_CERTIFICATE_FILE;
    cred.CertificateFile = &cf;
#endif

    st = t->api->ConfigurationLoadCredential(cfg, &cred);
    if (QUIC_FAILED(st)) {
        fprintf(stderr, "[magnetk] server ConfigurationLoadCredential failed: 0x%x\n", (unsigned)st);
        t->api->ConfigurationClose(cfg);
        return MAGNETK_ERR_TLS;
    }
    t->config_server = cfg;
    return MAGNETK_OK;
}

/* ===================================================================== */
/* Transport                                                              */
/* ===================================================================== */

MAGNETK_API magnetk_status_t magnetk_transport_init(
    const magnetk_config_t* config,
    magnetk_transport_t**   out_transport)
{
    if (!config || !out_transport) return MAGNETK_ERR_INVALID_PARAM;
    *out_transport = NULL;

    magnetk_transport_t* t = (magnetk_transport_t*)calloc(1, sizeof(*t));
    if (!t) return MAGNETK_ERR_MEMORY;
    mk_lock_init(&t->lock);

    t->bind_address       = mk_strdup(config->bind_address);
    t->cert_file          = mk_strdup(config->cert_file);
    t->key_file           = mk_strdup(config->key_file);
    t->cert_hash          = mk_strdup(config->cert_hash);
    t->cert_machine_store = config->cert_machine_store;
    t->port               = config->port;
    t->max_connections    = config->max_connections ? config->max_connections : 10000;
    t->send_window        = config->send_buffer_size ? config->send_buffer_size : 8u * 1024u * 1024u;
    {
        /* Per-stream receive ring: a quarter of the QUIC window, 256 KiB..4 MiB.
         * The rest of the window stays buffered inside MsQuic. */
        uint32_t rw = config->recv_buffer_size ? config->recv_buffer_size : 8u * 1024u * 1024u;
        uint32_t rm = rw / 4;
        if (rm < 256u * 1024u) rm = 256u * 1024u;
        if (rm > 4u * 1024u * 1024u) rm = 4u * 1024u * 1024u;
        t->ring_max = rm;
    }

    const char* alpn = (config->alpn && *config->alpn) ? config->alpn : "magnetk/2";
    size_t alpn_len = strlen(alpn);
    if (alpn_len > sizeof(t->alpn_str) - 1) alpn_len = sizeof(t->alpn_str) - 1;
    memcpy(t->alpn_str, alpn, alpn_len);
    t->alpn.Buffer = (uint8_t*)t->alpn_str;
    t->alpn.Length = (uint32_t)alpn_len;

    QUIC_STATUS st = MsQuicOpen2(&t->api);
    if (QUIC_FAILED(st)) {
        fprintf(stderr, "[magnetk] MsQuicOpen2 failed: 0x%x\n", (unsigned)st);
        goto fail_init;
    }

    QUIC_REGISTRATION_CONFIG reg = { "magnetk", QUIC_EXECUTION_PROFILE_LOW_LATENCY };
    switch (config->execution_profile) {
        case MAGNETK_PROFILE_MAX_THROUGHPUT: reg.ExecutionProfile = QUIC_EXECUTION_PROFILE_TYPE_MAX_THROUGHPUT; break;
        case MAGNETK_PROFILE_SCAVENGER:      reg.ExecutionProfile = QUIC_EXECUTION_PROFILE_TYPE_SCAVENGER; break;
        case MAGNETK_PROFILE_REAL_TIME:      reg.ExecutionProfile = QUIC_EXECUTION_PROFILE_TYPE_REAL_TIME; break;
        default:                             reg.ExecutionProfile = QUIC_EXECUTION_PROFILE_LOW_LATENCY; break;
    }
    st = t->api->RegistrationOpen(&reg, &t->registration);
    if (QUIC_FAILED(st)) {
        fprintf(stderr, "[magnetk] RegistrationOpen failed: 0x%x\n", (unsigned)st);
        goto fail_api;
    }

    /* ---- settings shared by client and server ---- */
    QUIC_SETTINGS* s = &t->settings;
    memset(s, 0, sizeof(*s));
    uint32_t recv_window = mk_pow2_ceil(config->recv_buffer_size ? config->recv_buffer_size : 8u * 1024u * 1024u);
    uint32_t conn_window = config->conn_flow_window ? config->conn_flow_window : recv_window * 4u;
    if (conn_window < recv_window) conn_window = recv_window;

    s->IdleTimeoutMs = config->idle_timeout_ms ? config->idle_timeout_ms : 60000;
    s->IsSet.IdleTimeoutMs = TRUE;
    s->HandshakeIdleTimeoutMs = config->handshake_timeout_ms ? config->handshake_timeout_ms : 10000;
    s->IsSet.HandshakeIdleTimeoutMs = TRUE;
    if (config->keepalive_ms) {
        s->KeepAliveIntervalMs = config->keepalive_ms;
        s->IsSet.KeepAliveIntervalMs = TRUE;
    }
    s->PeerBidiStreamCount = config->max_streams_per_conn ? config->max_streams_per_conn : 1024;
    s->IsSet.PeerBidiStreamCount = TRUE;
    s->PeerUnidiStreamCount = 0;
    s->IsSet.PeerUnidiStreamCount = TRUE;
    s->SendBufferingEnabled = FALSE;           /* we own (and bound) send buffers */
    s->IsSet.SendBufferingEnabled = TRUE;
    s->StreamRecvWindowDefault = recv_window;
    s->IsSet.StreamRecvWindowDefault = TRUE;
    s->ConnFlowControlWindow = conn_window;
    s->IsSet.ConnFlowControlWindow = TRUE;
    s->CongestionControlAlgorithm = (config->congestion_control == MAGNETK_CC_BBR)
        ? QUIC_CONGESTION_CONTROL_ALGORITHM_BBR : QUIC_CONGESTION_CONTROL_ALGORITHM_CUBIC;
    s->IsSet.CongestionControlAlgorithm = TRUE;
    /* HyStart++ leaves slow start before the window overshoots, which keeps
     * queueing delay (and latency for small messages sharing the
     * connection) low during bulk transfers. */
    s->HyStartEnabled = TRUE;
    s->IsSet.HyStartEnabled = TRUE;
    s->DatagramReceiveEnabled = config->enable_datagrams ? TRUE : FALSE;
    s->IsSet.DatagramReceiveEnabled = TRUE;

    st = t->api->ConfigurationOpen(t->registration, &t->alpn, 1, s, sizeof(*s), NULL, &t->config_client);
    if (QUIC_FAILED(st)) {
        fprintf(stderr, "[magnetk] client ConfigurationOpen failed: 0x%x\n", (unsigned)st);
        goto fail_reg;
    }

    QUIC_CREDENTIAL_CONFIG cc;
    memset(&cc, 0, sizeof(cc));
    cc.Type = QUIC_CREDENTIAL_TYPE_NONE;
    cc.Flags = QUIC_CREDENTIAL_FLAG_CLIENT;
    if (config->no_cert_validation) cc.Flags |= QUIC_CREDENTIAL_FLAG_NO_CERTIFICATE_VALIDATION;
    st = t->api->ConfigurationLoadCredential(t->config_client, &cc);
    if (QUIC_FAILED(st)) {
        fprintf(stderr, "[magnetk] client ConfigurationLoadCredential failed: 0x%x\n", (unsigned)st);
        t->api->ConfigurationClose(t->config_client);
        goto fail_reg;
    }

    *out_transport = t;
    return MAGNETK_OK;

fail_reg:
    t->api->RegistrationClose(t->registration);
fail_api:
    MsQuicClose(t->api);
fail_init:
    free(t->bind_address); free(t->cert_file); free(t->key_file); free(t->cert_hash);
    mk_lock_destroy(&t->lock);
    free(t);
    return MAGNETK_ERR_INIT;
}

MAGNETK_API void magnetk_transport_set_notify(magnetk_transport_t* t, magnetk_notify_fn fn) {
    if (t) t->notify = fn;
}

MAGNETK_API void magnetk_transport_shutdown(magnetk_transport_t* t) {
    if (!t) return;
    MK_TRACE("transport %p shutdown begin (server conns %ld)", (void*)t, t->server_conns);
    t->notify = NULL;
    /* Close everything still running (peers are told, so they fail over
     * immediately instead of waiting for an idle timeout), then wait for
     * all handles to close. */
    t->api->RegistrationShutdown(t->registration, QUIC_CONNECTION_SHUTDOWN_FLAG_NONE, 0);
    if (t->config_client) t->api->ConfigurationClose(t->config_client);
    if (t->config_server) t->api->ConfigurationClose(t->config_server);
    MK_TRACE("transport %p RegistrationClose", (void*)t);
    t->api->RegistrationClose(t->registration);
    MK_TRACE("transport %p closed", (void*)t);
    MsQuicClose(t->api);
#ifdef _WIN32
    if (t->cert_context) CertFreeCertificateContext(t->cert_context);
#endif
    free(t->bind_address); free(t->cert_file); free(t->key_file); free(t->cert_hash);
    mk_lock_destroy(&t->lock);
    free(t);
}

/* ===================================================================== */
/* Reference counting                                                     */
/* ===================================================================== */

static void listener_release(magnetk_listener_t* l) {
    if (mk_dec(&l->refs) != 0) return;
    mk_cond_destroy(&l->cond);
    mk_lock_destroy(&l->lock);
    free(l);
}

static void conn_release(magnetk_connection_t* c) {
    long r = mk_dec(&c->refs);
    MK_TRACE("conn %p release -> %ld", (void*)c, r);
    if (r != 0) return;
    if (c->handle) c->t->api->ConnectionClose(c->handle);
    for (uint32_t i = 0; i < c->dq_len; i++) {
        free(c->dq[(c->dq_head + i) % MK_DGRAM_QUEUE_CAP].data);
    }
    mk_cond_destroy(&c->cond);
    mk_lock_destroy(&c->lock);
    free(c);
}

static void stream_release(magnetk_stream_t* s) {
    long r = mk_dec(&s->refs);
    MK_TRACE("stream %p (conn %p) release -> %ld", (void*)s, (void*)s->conn, r);
    if (r != 0) return;
    if (s->handle) s->api->StreamClose(s->handle);
    free(s->rx_ring);
    mk_cond_destroy(&s->cond);
    mk_lock_destroy(&s->lock);
    magnetk_connection_t* c = s->conn;
    free(s);
    if (c) conn_release(c);
}

static void conn_unlink_stream(magnetk_connection_t* c, magnetk_stream_t* s) {
    mk_lock_acquire(&c->lock);
    if (s->live_linked) {
        if (s->live_prev) s->live_prev->live_next = s->live_next; else c->live_head = s->live_next;
        if (s->live_next) s->live_next->live_prev = s->live_prev;
        s->live_prev = s->live_next = NULL;
        s->live_linked = 0;
    }
    mk_lock_release(&c->lock);
}

static magnetk_stream_t* stream_alloc(magnetk_connection_t* c) {
    magnetk_stream_t* s = (magnetk_stream_t*)calloc(1, sizeof(*s));
    if (!s) return NULL;
    s->refs = 2;                 /* MsQuic + application */
    s->msquic_ref = 1;
    s->conn = c;
    mk_inc(&c->refs);
    s->api = c->t->api;
    s->tx_limit = c->t->send_window;
    s->rx_max = c->t->ring_max;
    mk_lock_init(&s->lock);
    mk_cond_init(&s->cond);
    mk_lock_acquire(&c->lock);
    s->live_next = c->live_head;
    if (c->live_head) c->live_head->live_prev = s;
    c->live_head = s;
    s->live_linked = 1;
    mk_lock_release(&c->lock);
    return s;
}

/* Drops MsQuic's reference on every stream that never reported
 * SHUTDOWN_COMPLETE (e.g. a stream whose start was still queued when the
 * connection went down - MsQuic does not indicate completion for those).
 * Called once the connection itself is shut down, so nothing more can
 * happen on the wire. */
static void conn_sweep_streams(magnetk_connection_t* c) {
    mk_lock_acquire(&c->lock);
    magnetk_stream_t* list = c->live_head;
    c->live_head = NULL;
    for (magnetk_stream_t* it = list; it; it = it->live_next) it->live_linked = 0;
    mk_lock_release(&c->lock);

    while (list) {
        magnetk_stream_t* s = list;
        list = s->live_next;
        s->live_prev = s->live_next = NULL;
        mk_lock_acquire(&s->lock);
        int held = s->msquic_ref;
        uint64_t token = 0;
        if (held) {
            s->msquic_ref = 0;
            s->shutdown_complete = 1;
            if (!s->rx_fin) s->rx_reset = 1;
            s->tx_inflight = 0;
            token = s->token;
            mk_cond_broadcast(&s->cond);
        }
        mk_lock_release(&s->lock);
        if (held) {
            MK_TRACE("stream %p swept at connection shutdown", (void*)s);
            mk_notify(c->t, token, MAGNETK_EV_CLOSED | MAGNETK_EV_READABLE | MAGNETK_EV_WRITABLE);
            stream_release(s);
        }
    }
}

static magnetk_connection_t* conn_alloc(magnetk_transport_t* t) {
    magnetk_connection_t* c = (magnetk_connection_t*)calloc(1, sizeof(*c));
    if (!c) return NULL;
    c->refs = 2;                 /* MsQuic + application */
    c->msquic_ref = 1;
    c->t = t;
    c->close_reason = MAGNETK_ERR_CLOSED;
    mk_lock_init(&c->lock);
    mk_cond_init(&c->cond);
    return c;
}

/* ===================================================================== */
/* Listener                                                               */
/* ===================================================================== */

static QUIC_STATUS QUIC_API listener_callback(HQUIC handle, void* ctx, QUIC_LISTENER_EVENT* ev) {
    magnetk_listener_t* l = (magnetk_listener_t*)ctx;
    magnetk_transport_t* t = l->t;
    (void)handle;

    if (ev->Type != QUIC_LISTENER_EVENT_NEW_CONNECTION) return QUIC_STATUS_SUCCESS;

    mk_lock_acquire(&l->lock);
    int closed = l->closed;
    mk_lock_release(&l->lock);
    if (closed) return QUIC_STATUS_CONNECTION_REFUSED;

    if ((uint32_t)mk_inc(&t->server_conns) > t->max_connections) {
        mk_dec(&t->server_conns);
        return QUIC_STATUS_CONNECTION_REFUSED;
    }

    magnetk_connection_t* c = conn_alloc(t);
    if (!c) { mk_dec(&t->server_conns); return QUIC_STATUS_OUT_OF_MEMORY; }
    c->is_server = 1;
    c->handle = ev->NEW_CONNECTION.Connection;
    c->pending_listener = l;
    mk_inc(&l->refs);
    if (ev->NEW_CONNECTION.Info && ev->NEW_CONNECTION.Info->RemoteAddress) {
        mk_addr_to_string(ev->NEW_CONNECTION.Info->RemoteAddress, c->remote, sizeof(c->remote));
    }

    t->api->SetCallbackHandler(c->handle, (void*)conn_callback, c);
    QUIC_STATUS st = t->api->ConnectionSetConfiguration(c->handle, t->config_server);
    if (QUIC_FAILED(st)) {
        /* Let the normal SHUTDOWN_COMPLETE path release everything. */
        t->api->ConnectionShutdown(c->handle, QUIC_CONNECTION_SHUTDOWN_FLAG_SILENT, 0);
    }
    return QUIC_STATUS_SUCCESS;
}

MAGNETK_API magnetk_status_t magnetk_listen(magnetk_transport_t* t, magnetk_listener_t** out) {
    if (!t) return MAGNETK_ERR_INVALID_PARAM;
    return magnetk_listen_on(t, t->bind_address, t->port, out);
}

MAGNETK_API magnetk_status_t magnetk_listen_on(
    magnetk_transport_t* t, const char* bind_address, uint16_t port, magnetk_listener_t** out)
{
    if (!t || !out) return MAGNETK_ERR_INVALID_PARAM;
    *out = NULL;

    mk_lock_acquire(&t->lock);
    magnetk_status_t ms = mk_ensure_server_config(t);
    mk_lock_release(&t->lock);
    if (ms != MAGNETK_OK) return ms;

    QUIC_ADDR addr;
    if (!mk_parse_bind(bind_address, port, &addr)) {
        fprintf(stderr, "[magnetk] invalid bind address '%s'\n", bind_address ? bind_address : "");
        return MAGNETK_ERR_CONFIG;
    }

    magnetk_listener_t* l = (magnetk_listener_t*)calloc(1, sizeof(*l));
    if (!l) return MAGNETK_ERR_MEMORY;
    l->refs = 1;
    l->t = t;
    mk_lock_init(&l->lock);
    mk_cond_init(&l->cond);

    QUIC_STATUS st = t->api->ListenerOpen(t->registration, listener_callback, l, &l->handle);
    if (QUIC_FAILED(st)) {
        listener_release(l);
        return MAGNETK_ERR_LISTEN;
    }
    st = t->api->ListenerStart(l->handle, &t->alpn, 1, &addr);
    if (QUIC_FAILED(st)) {
        fprintf(stderr, "[magnetk] ListenerStart failed on port %u: 0x%x\n", (unsigned)port, (unsigned)st);
        t->api->ListenerClose(l->handle);
        listener_release(l);
        return MAGNETK_ERR_LISTEN;
    }
    *out = l;
    return MAGNETK_OK;
}

MAGNETK_API void magnetk_listener_set_token(magnetk_listener_t* l, uint64_t token) {
    if (!l) return;
    mk_lock_acquire(&l->lock);
    l->token = token;
    int ready = l->queue_len > 0;
    mk_lock_release(&l->lock);
    if (ready) mk_notify(l->t, token, MAGNETK_EV_ACCEPT);
}

MAGNETK_API uint16_t magnetk_listener_port(magnetk_listener_t* l) {
    if (!l || !l->handle) return 0;
    QUIC_ADDR addr;
    uint32_t size = sizeof(addr);
    memset(&addr, 0, sizeof(addr));
    if (QUIC_FAILED(l->t->api->GetParam(l->handle, QUIC_PARAM_LISTENER_LOCAL_ADDRESS, &size, &addr))) return 0;
    return ntohs(addr.Ipv4.sin_port);
}

MAGNETK_API magnetk_status_t magnetk_listener_accept(
    magnetk_listener_t* l, uint32_t timeout_ms, magnetk_connection_t** out)
{
    if (!l || !out) return MAGNETK_ERR_INVALID_PARAM;
    *out = NULL;
    uint64_t deadline = mk_deadline(timeout_ms);
    mk_lock_acquire(&l->lock);
    for (;;) {
        if (l->queue_head) {
            magnetk_connection_t* c = l->queue_head;
            l->queue_head = c->next;
            if (!l->queue_head) l->queue_tail = NULL;
            l->queue_len--;
            c->next = NULL;
            mk_lock_release(&l->lock);
            *out = c;
            return MAGNETK_OK;
        }
        if (l->closed) { mk_lock_release(&l->lock); return MAGNETK_ERR_CLOSED; }
        if (timeout_ms == 0) { mk_lock_release(&l->lock); return MAGNETK_WOULD_BLOCK; }
        uint32_t rem = mk_remaining(deadline);
        if (rem == 0) { mk_lock_release(&l->lock); return MAGNETK_ERR_TIMEOUT; }
        mk_cond_wait(&l->cond, &l->lock, rem);
    }
}

MAGNETK_API void magnetk_listener_close(magnetk_listener_t* l) {
    if (!l) return;
    mk_lock_acquire(&l->lock);
    if (l->closed) { mk_lock_release(&l->lock); return; }
    l->closed = 1;
    l->token = 0;
    magnetk_connection_t* q = l->queue_head;
    l->queue_head = l->queue_tail = NULL;
    l->queue_len = 0;
    mk_cond_broadcast(&l->cond);
    mk_lock_release(&l->lock);

    /* Blocks until MsQuic stops delivering NEW_CONNECTION events. */
    l->t->api->ListenerClose(l->handle);
    l->handle = NULL;

    while (q) {
        magnetk_connection_t* next = q->next;
        q->next = NULL;
        magnetk_connection_close(q);
        q = next;
    }
    listener_release(l);
}

/* Called for a server connection whose handshake finished or failed. */
static void conn_hand_to_listener(magnetk_connection_t* c, int ok) {
    magnetk_listener_t* l = c->pending_listener;
    c->pending_listener = NULL;
    if (!l) return;

    int queued = 0;
    uint64_t token = 0;
    if (ok) {
        mk_lock_acquire(&l->lock);
        if (!l->closed) {
            c->next = NULL;
            if (l->queue_tail) l->queue_tail->next = c; else l->queue_head = c;
            l->queue_tail = c;
            l->queue_len++;
            queued = 1;
            token = l->token;
            mk_cond_broadcast(&l->cond);
        }
        mk_lock_release(&l->lock);
    }
    if (queued) {
        mk_notify(l->t, token, MAGNETK_EV_ACCEPT);
    } else {
        /* Nobody will ever accept it: drop the application reference. */
        mk_lock_acquire(&c->lock);
        c->app_released = 1;
        mk_lock_release(&c->lock);
        if (ok) c->t->api->ConnectionShutdown(c->handle, QUIC_CONNECTION_SHUTDOWN_FLAG_NONE, 0);
        conn_release(c);
    }
    listener_release(l);
}

/* ===================================================================== */
/* Connection                                                             */
/* ===================================================================== */

/* Handler for peer streams we refuse: just close the handle when done. */
static QUIC_STATUS QUIC_API refused_stream_callback(HQUIC handle, void* ctx, QUIC_STREAM_EVENT* ev) {
    const QUIC_API_TABLE* api = (const QUIC_API_TABLE*)ctx;
    if (ev->Type == QUIC_STREAM_EVENT_SHUTDOWN_COMPLETE) api->StreamClose(handle);
    return QUIC_STATUS_SUCCESS;
}

static void conn_drain_streams(magnetk_connection_t* c, magnetk_stream_t** out_list) {
    /* caller holds c->lock */
    *out_list = c->sq_head;
    c->sq_head = c->sq_tail = NULL;
    c->sq_len = 0;
}

static QUIC_STATUS QUIC_API conn_callback(HQUIC handle, void* ctx, QUIC_CONNECTION_EVENT* ev) {
    magnetk_connection_t* c = (magnetk_connection_t*)ctx;
    magnetk_transport_t* t = c->t;
    uint64_t token;

    switch (ev->Type) {
    case QUIC_CONNECTION_EVENT_CONNECTED: {
        QUIC_ADDR addr;
        uint32_t size = sizeof(addr);
        memset(&addr, 0, sizeof(addr));
        char remote[64] = {0};
        if (QUIC_SUCCEEDED(t->api->GetParam(handle, QUIC_PARAM_CONN_REMOTE_ADDRESS, &size, &addr))) {
            mk_addr_to_string(&addr, remote, sizeof(remote));
        }
        mk_lock_acquire(&c->lock);
        c->connected = 1;
        if (remote[0]) memcpy(c->remote, remote, sizeof(c->remote));
        token = c->token;
        mk_cond_broadcast(&c->cond);
        mk_lock_release(&c->lock);
        if (c->is_server) conn_hand_to_listener(c, 1);
        mk_notify(t, token, MAGNETK_EV_CONNECTED);
        return QUIC_STATUS_SUCCESS;
    }

    case QUIC_CONNECTION_EVENT_SHUTDOWN_INITIATED_BY_TRANSPORT:
    case QUIC_CONNECTION_EVENT_SHUTDOWN_INITIATED_BY_PEER:
        mk_lock_acquire(&c->lock);
        c->shutting_down = 1;
        if (ev->Type == QUIC_CONNECTION_EVENT_SHUTDOWN_INITIATED_BY_TRANSPORT && !c->connected) {
            c->close_reason = mk_map_quic_status(ev->SHUTDOWN_INITIATED_BY_TRANSPORT.Status);
        }
        token = c->token;
        mk_cond_broadcast(&c->cond);
        mk_lock_release(&c->lock);
        mk_notify(t, token, MAGNETK_EV_CLOSED);
        return QUIC_STATUS_SUCCESS;

    case QUIC_CONNECTION_EVENT_SHUTDOWN_COMPLETE: {
        MK_TRACE("conn %p SHUTDOWN_COMPLETE (server=%d)", (void*)c, c->is_server);
        magnetk_stream_t* pending = NULL;
        mk_lock_acquire(&c->lock);
        c->shutting_down = 1;
        c->shutdown_complete = 1;
        int held = c->msquic_ref;
        c->msquic_ref = 0;
        conn_drain_streams(c, &pending);
        token = c->token;
        mk_cond_broadcast(&c->cond);
        mk_lock_release(&c->lock);

        if (c->is_server) {
            mk_dec(&t->server_conns);
            if (c->pending_listener) conn_hand_to_listener(c, 0);   /* handshake never finished */
        }
        while (pending) {                        /* streams nobody accepted */
            magnetk_stream_t* next = pending->next;
            pending->next = NULL;
            magnetk_stream_close(pending);
            pending = next;
        }
        conn_sweep_streams(c);
        mk_notify(t, token, MAGNETK_EV_CLOSED);
        if (held) conn_release(c);               /* MsQuic reference */
        return QUIC_STATUS_SUCCESS;
    }

    case QUIC_CONNECTION_EVENT_PEER_STREAM_STARTED: {
        HQUIC sh = ev->PEER_STREAM_STARTED.Stream;
        mk_lock_acquire(&c->lock);
        int refuse = c->app_released || c->shutting_down;
        mk_lock_release(&c->lock);
        if (refuse) {
            t->api->SetCallbackHandler(sh, (void*)refused_stream_callback, (void*)t->api);
            t->api->StreamShutdown(sh, QUIC_STREAM_SHUTDOWN_FLAG_ABORT, 0);
            return QUIC_STATUS_SUCCESS;
        }
        magnetk_stream_t* s = stream_alloc(c);
        if (!s) return QUIC_STATUS_OUT_OF_MEMORY;
        s->handle = sh;
        uint32_t sz = sizeof(s->id);
        t->api->GetParam(sh, QUIC_PARAM_STREAM_ID, &sz, &s->id);
        t->api->SetCallbackHandler(sh, (void*)stream_callback, s);

        mk_lock_acquire(&c->lock);
        s->next = NULL;
        if (c->sq_tail) c->sq_tail->next = s; else c->sq_head = s;
        c->sq_tail = s;
        c->sq_len++;
        token = c->token;
        mk_cond_broadcast(&c->cond);
        mk_lock_release(&c->lock);
        mk_notify(t, token, MAGNETK_EV_ACCEPT);
        return QUIC_STATUS_SUCCESS;
    }

    case QUIC_CONNECTION_EVENT_DATAGRAM_STATE_CHANGED:
        mk_lock_acquire(&c->lock);
        c->dgram_send_enabled = ev->DATAGRAM_STATE_CHANGED.SendEnabled ? 1 : 0;
        c->dgram_max = ev->DATAGRAM_STATE_CHANGED.MaxSendLength;
        mk_lock_release(&c->lock);
        return QUIC_STATUS_SUCCESS;

    case QUIC_CONNECTION_EVENT_DATAGRAM_RECEIVED: {
        const QUIC_BUFFER* b = ev->DATAGRAM_RECEIVED.Buffer;
        uint8_t* copy = (uint8_t*)malloc(b->Length ? b->Length : 1);
        if (!copy) return QUIC_STATUS_SUCCESS;
        memcpy(copy, b->Buffer, b->Length);
        mk_lock_acquire(&c->lock);
        if (c->dq_len == MK_DGRAM_QUEUE_CAP) {   /* drop oldest: datagrams are unreliable anyway */
            free(c->dq[c->dq_head].data);
            c->dq_head = (c->dq_head + 1) % MK_DGRAM_QUEUE_CAP;
            c->dq_len--;
        }
        uint32_t slot = (c->dq_head + c->dq_len) % MK_DGRAM_QUEUE_CAP;
        c->dq[slot].data = copy;
        c->dq[slot].length = b->Length;
        c->dq_len++;
        token = c->token;
        mk_cond_broadcast(&c->cond);
        mk_lock_release(&c->lock);
        mk_notify(t, token, MAGNETK_EV_DATAGRAM);
        return QUIC_STATUS_SUCCESS;
    }

    case QUIC_CONNECTION_EVENT_DATAGRAM_SEND_STATE_CHANGED:
        if (QUIC_DATAGRAM_SEND_STATE_IS_FINAL(ev->DATAGRAM_SEND_STATE_CHANGED.State)) {
            free(ev->DATAGRAM_SEND_STATE_CHANGED.ClientContext);
        }
        return QUIC_STATUS_SUCCESS;

    default:
        return QUIC_STATUS_SUCCESS;
    }
}

MAGNETK_API magnetk_status_t magnetk_connect(
    magnetk_transport_t* t, const char* host, uint16_t port,
    uint32_t timeout_ms, magnetk_connection_t** out)
{
    if (!t || !host || !*host || !out || port == 0) return MAGNETK_ERR_INVALID_PARAM;
    *out = NULL;

    magnetk_connection_t* c = conn_alloc(t);
    if (!c) return MAGNETK_ERR_MEMORY;

    QUIC_STATUS st = t->api->ConnectionOpen(t->registration, conn_callback, c, &c->handle);
    if (QUIC_FAILED(st)) {
        c->handle = NULL;
        c->refs = 1;
        conn_release(c);
        return MAGNETK_ERR_CONNECT;
    }
    st = t->api->ConnectionStart(c->handle, t->config_client, QUIC_ADDRESS_FAMILY_UNSPEC, host, port);
    if (QUIC_FAILED(st)) {
        /* Never started: we own both references. */
        c->msquic_ref = 0;
        c->refs = 1;
        conn_release(c);
        return MAGNETK_ERR_CONNECT;
    }

    uint64_t deadline = mk_deadline(timeout_ms);
    magnetk_status_t result = MAGNETK_OK;
    mk_lock_acquire(&c->lock);
    for (;;) {
        if (c->connected) break;
        if (c->shutting_down) { result = c->close_reason == MAGNETK_ERR_CLOSED ? MAGNETK_ERR_CONNECT : c->close_reason; break; }
        uint32_t rem = mk_remaining(deadline);
        if (rem == 0) { result = MAGNETK_ERR_TIMEOUT; break; }
        mk_cond_wait(&c->cond, &c->lock, rem);
    }
    mk_lock_release(&c->lock);

    if (result != MAGNETK_OK) {
        magnetk_connection_close(c);
        return result;
    }
    *out = c;
    return MAGNETK_OK;
}

MAGNETK_API magnetk_status_t magnetk_connection_accept_stream(
    magnetk_connection_t* c, uint32_t timeout_ms, magnetk_stream_t** out)
{
    if (!c || !out) return MAGNETK_ERR_INVALID_PARAM;
    *out = NULL;
    uint64_t deadline = mk_deadline(timeout_ms);
    mk_lock_acquire(&c->lock);
    for (;;) {
        if (c->sq_head) {
            magnetk_stream_t* s = c->sq_head;
            c->sq_head = s->next;
            if (!c->sq_head) c->sq_tail = NULL;
            c->sq_len--;
            s->next = NULL;
            mk_lock_release(&c->lock);
            *out = s;
            return MAGNETK_OK;
        }
        if (c->shutting_down) { mk_lock_release(&c->lock); return MAGNETK_ERR_CLOSED; }
        if (timeout_ms == 0) { mk_lock_release(&c->lock); return MAGNETK_WOULD_BLOCK; }
        uint32_t rem = mk_remaining(deadline);
        if (rem == 0) { mk_lock_release(&c->lock); return MAGNETK_ERR_TIMEOUT; }
        mk_cond_wait(&c->cond, &c->lock, rem);
    }
}

MAGNETK_API void magnetk_connection_set_token(magnetk_connection_t* c, uint64_t token) {
    if (!c) return;
    mk_lock_acquire(&c->lock);
    c->token = token;
    uint32_t ev = 0;
    if (c->sq_len) ev |= MAGNETK_EV_ACCEPT;
    if (c->dq_len) ev |= MAGNETK_EV_DATAGRAM;
    if (c->shutting_down) ev |= MAGNETK_EV_CLOSED;
    mk_lock_release(&c->lock);
    if (ev) mk_notify(c->t, token, ev);
}

MAGNETK_API int magnetk_connection_is_alive(magnetk_connection_t* c) {
    if (!c) return 0;
    mk_lock_acquire(&c->lock);
    int alive = c->connected && !c->shutting_down;
    mk_lock_release(&c->lock);
    return alive;
}

MAGNETK_API int magnetk_connection_remote_addr(magnetk_connection_t* c, char* buf, int buf_len) {
    if (!c || !buf || buf_len <= 0) return 0;
    mk_lock_acquire(&c->lock);
    int n = (int)strlen(c->remote);
    if (n >= buf_len) n = buf_len - 1;
    memcpy(buf, c->remote, (size_t)n);
    buf[n] = 0;
    mk_lock_release(&c->lock);
    return n;
}

MAGNETK_API magnetk_status_t magnetk_connection_stats(magnetk_connection_t* c, magnetk_conn_stats_t* out) {
    if (!c || !out) return MAGNETK_ERR_INVALID_PARAM;
    memset(out, 0, sizeof(*out));
    QUIC_STATISTICS_V2 st;
    uint32_t size = sizeof(st);
    memset(&st, 0, sizeof(st));
    if (QUIC_FAILED(c->t->api->GetParam(c->handle, QUIC_PARAM_CONN_STATISTICS_V2, &size, &st))) {
        return MAGNETK_ERR_NOT_READY;
    }
    out->rtt_us            = st.Rtt;
    out->min_rtt_us        = st.MinRtt;
    out->congestion_window = st.SendCongestionWindow;
    out->path_mtu          = st.SendPathMtu;
    out->send_bytes        = st.SendTotalBytes;
    out->recv_bytes        = st.RecvTotalBytes;
    out->send_packets      = st.SendTotalPackets;
    out->lost_packets      = st.SendSuspectedLostPackets - st.SendSpuriousLostPackets;
    return MAGNETK_OK;
}

MAGNETK_API uint32_t magnetk_connection_max_datagram(magnetk_connection_t* c) {
    if (!c) return 0;
    mk_lock_acquire(&c->lock);
    uint32_t m = (c->dgram_send_enabled && !c->shutting_down) ? c->dgram_max : 0;
    mk_lock_release(&c->lock);
    return m;
}

MAGNETK_API magnetk_status_t magnetk_connection_send_datagram(
    magnetk_connection_t* c, const uint8_t* data, uint32_t length)
{
    if (!c || (!data && length)) return MAGNETK_ERR_INVALID_PARAM;
    mk_lock_acquire(&c->lock);
    int down = c->shutting_down;
    int enabled = c->dgram_send_enabled;
    uint32_t max = c->dgram_max;
    mk_lock_release(&c->lock);
    if (down) return MAGNETK_ERR_CLOSED;
    if (!enabled) return MAGNETK_ERR_NOT_READY;
    if (length > max) return MAGNETK_ERR_TOO_LARGE;

    mk_send_block* b = (mk_send_block*)malloc(sizeof(mk_send_block) + (length ? length : 1));
    if (!b) return MAGNETK_ERR_MEMORY;
    uint8_t* payload = (uint8_t*)(b + 1);
    if (length) memcpy(payload, data, length);
    b->qb.Buffer = payload;
    b->qb.Length = length;
    b->length = length;
    QUIC_STATUS st = c->t->api->DatagramSend(c->handle, &b->qb, 1, QUIC_SEND_FLAG_NONE, b);
    if (QUIC_FAILED(st)) { free(b); return MAGNETK_ERR_SEND; }
    return MAGNETK_OK;
}

MAGNETK_API magnetk_status_t magnetk_connection_recv_datagram(
    magnetk_connection_t* c, uint8_t* buffer, uint32_t buffer_size,
    uint32_t* out_length, uint32_t timeout_ms)
{
    if (!c || !buffer || !out_length) return MAGNETK_ERR_INVALID_PARAM;
    *out_length = 0;
    uint64_t deadline = mk_deadline(timeout_ms);
    mk_lock_acquire(&c->lock);
    for (;;) {
        if (c->dq_len) {
            mk_datagram d = c->dq[c->dq_head];
            c->dq[c->dq_head].data = NULL;
            c->dq_head = (c->dq_head + 1) % MK_DGRAM_QUEUE_CAP;
            c->dq_len--;
            mk_lock_release(&c->lock);
            uint32_t n = d.length < buffer_size ? d.length : buffer_size;   /* truncates like UDP */
            memcpy(buffer, d.data, n);
            free(d.data);
            *out_length = n;
            return MAGNETK_OK;
        }
        if (c->shutting_down) { mk_lock_release(&c->lock); return MAGNETK_ERR_CLOSED; }
        if (timeout_ms == 0) { mk_lock_release(&c->lock); return MAGNETK_WOULD_BLOCK; }
        uint32_t rem = mk_remaining(deadline);
        if (rem == 0) { mk_lock_release(&c->lock); return MAGNETK_ERR_TIMEOUT; }
        mk_cond_wait(&c->cond, &c->lock, rem);
    }
}

MAGNETK_API void magnetk_connection_shutdown(magnetk_connection_t* c, uint64_t error_code) {
    if (!c) return;
    mk_lock_acquire(&c->lock);
    int done = c->shutdown_complete;
    mk_lock_release(&c->lock);
    if (!done && c->handle) {
        c->t->api->ConnectionShutdown(c->handle, QUIC_CONNECTION_SHUTDOWN_FLAG_NONE, error_code & 0x3FFFFFFFFFFFFFFFull);
    }
}

MAGNETK_API void magnetk_connection_close(magnetk_connection_t* c) {
    if (!c) return;
    MK_TRACE("conn %p app close", (void*)c);
    magnetk_stream_t* pending = NULL;
    mk_lock_acquire(&c->lock);
    if (c->app_released) { mk_lock_release(&c->lock); return; }
    c->app_released = 1;
    c->token = 0;
    conn_drain_streams(c, &pending);
    int done = c->shutdown_complete;
    mk_lock_release(&c->lock);

    while (pending) {
        magnetk_stream_t* next = pending->next;
        pending->next = NULL;
        magnetk_stream_close(pending);
        pending = next;
    }
    if (!done && c->handle) {
        c->t->api->ConnectionShutdown(c->handle, QUIC_CONNECTION_SHUTDOWN_FLAG_NONE, 0);
    }
    conn_release(c);
}

/* ===================================================================== */
/* Streams                                                                */
/* ===================================================================== */

static QUIC_STATUS QUIC_API stream_callback(HQUIC handle, void* ctx, QUIC_STREAM_EVENT* ev) {
    magnetk_stream_t* s = (magnetk_stream_t*)ctx;
    magnetk_transport_t* t = s->conn->t;
    uint64_t token;
    (void)handle;

    switch (ev->Type) {
    case QUIC_STREAM_EVENT_START_COMPLETE:
        if (QUIC_FAILED(ev->START_COMPLETE.Status)) {
            mk_lock_acquire(&s->lock);
            s->tx_error = 1;
            token = s->token;
            mk_cond_broadcast(&s->cond);
            mk_lock_release(&s->lock);
            mk_notify(t, token, MAGNETK_EV_WRITABLE | MAGNETK_EV_READABLE);
        } else {
            mk_lock_acquire(&s->lock);
            s->id = ev->START_COMPLETE.ID;
            mk_lock_release(&s->lock);
        }
        return QUIC_STATUS_SUCCESS;

    case QUIC_STREAM_EVENT_RECEIVE: {
        /* Copy into the stream's ring and return at once, so MsQuic keeps
         * delivering without waiting for the reader. When the ring is full
         * only part is accepted: MsQuic then pauses delivery (flow control
         * pushes back on the sender) until the reader re-enables it. */
        int fin = (ev->RECEIVE.Flags & QUIC_RECEIVE_FLAG_FIN) ? 1 : 0;
        uint64_t total = ev->RECEIVE.TotalBufferLength;
        mk_lock_acquire(&s->lock);
        if (s->app_closed) {                     /* nobody will read it */
            mk_lock_release(&s->lock);
            return QUIC_STATUS_SUCCESS;
        }
        uint32_t room = s->rx_max > s->rx_len ? s->rx_max - s->rx_len : 0;
        uint32_t accept = total < room ? (uint32_t)total : room;
        if (accept > 0 && s->rx_len + accept > s->rx_cap) {
            uint32_t cap = s->rx_cap ? s->rx_cap : 64u * 1024u;
            while (cap < s->rx_len + accept) cap *= 2;
            if (cap > s->rx_max) cap = s->rx_max;
            uint8_t* nr = (uint8_t*)malloc(cap);
            if (!nr) {
                accept = s->rx_cap - s->rx_len;  /* take what fits in the current ring */
            } else {
                uint32_t first = s->rx_cap - s->rx_head;   /* linearise existing data */
                if (first > s->rx_len) first = s->rx_len;
                if (s->rx_len) {
                    memcpy(nr, s->rx_ring + s->rx_head, first);
                    memcpy(nr + first, s->rx_ring, s->rx_len - first);
                }
                free(s->rx_ring);
                s->rx_ring = nr;
                s->rx_cap = cap;
                s->rx_head = 0;
            }
        }
        uint32_t copied = 0;
        for (uint32_t i = 0; i < ev->RECEIVE.BufferCount && copied < accept; i++) {
            const QUIC_BUFFER* b = &ev->RECEIVE.Buffers[i];
            uint32_t take = b->Length;
            if (take > accept - copied) take = accept - copied;
            uint32_t tail = (s->rx_head + s->rx_len) % s->rx_cap;
            uint32_t first = s->rx_cap - tail;
            if (first > take) first = take;
            memcpy(s->rx_ring + tail, b->Buffer, first);
            if (take > first) memcpy(s->rx_ring, b->Buffer + first, take - first);
            s->rx_len += take;
            copied += take;
        }
        int partial = copied < total;
        if (partial) {
            s->rx_paused = 1;
            ev->RECEIVE.TotalBufferLength = copied;   /* MsQuic pauses until re-enabled */
        } else if (fin) {
            s->rx_fin = 1;
        }
        token = s->token;
        mk_cond_broadcast(&s->cond);
        mk_lock_release(&s->lock);
        if (copied || (fin && !partial)) mk_notify(t, token, MAGNETK_EV_READABLE);
        return QUIC_STATUS_SUCCESS;
    }

    case QUIC_STREAM_EVENT_SEND_COMPLETE: {
        mk_send_block* b = (mk_send_block*)ev->SEND_COMPLETE.ClientContext;
        uint32_t len = b ? b->length : 0;
        free(b);
        mk_lock_acquire(&s->lock);
        s->tx_inflight = s->tx_inflight >= len ? s->tx_inflight - len : 0;
        if (ev->SEND_COMPLETE.Canceled) s->tx_error = 1;
        token = s->token;
        mk_cond_broadcast(&s->cond);
        mk_lock_release(&s->lock);
        mk_notify(t, token, MAGNETK_EV_WRITABLE);
        return QUIC_STATUS_SUCCESS;
    }

    case QUIC_STREAM_EVENT_IDEAL_SEND_BUFFER_SIZE:
        mk_lock_acquire(&s->lock);
        if (ev->IDEAL_SEND_BUFFER_SIZE.ByteCount > s->tx_limit) {
            /* Follow MsQuic's bandwidth-delay estimate, capped for memory. */
            uint64_t ideal = ev->IDEAL_SEND_BUFFER_SIZE.ByteCount;
            s->tx_limit = ideal > (64ull << 20) ? (64ull << 20) : ideal;
        }
        token = s->token;
        mk_cond_broadcast(&s->cond);
        mk_lock_release(&s->lock);
        mk_notify(t, token, MAGNETK_EV_WRITABLE);
        return QUIC_STATUS_SUCCESS;

    case QUIC_STREAM_EVENT_PEER_SEND_SHUTDOWN:
        MK_TRACE("stream %p PEER_SEND_SHUTDOWN (fin)", (void*)s);
        mk_lock_acquire(&s->lock);
        if (!s->rx_paused) s->rx_fin = 1;   /* else the final RECEIVE carries FIN */
        token = s->token;
        mk_cond_broadcast(&s->cond);
        mk_lock_release(&s->lock);
        mk_notify(t, token, MAGNETK_EV_READABLE);
        return QUIC_STATUS_SUCCESS;

    case QUIC_STREAM_EVENT_PEER_SEND_ABORTED:
        MK_TRACE("stream %p PEER_SEND_ABORTED", (void*)s);
        mk_lock_acquire(&s->lock);
        s->rx_reset = 1;
        token = s->token;
        mk_cond_broadcast(&s->cond);
        mk_lock_release(&s->lock);
        mk_notify(t, token, MAGNETK_EV_READABLE);
        return QUIC_STATUS_SUCCESS;

    case QUIC_STREAM_EVENT_PEER_RECEIVE_ABORTED:
        MK_TRACE("stream %p PEER_RECEIVE_ABORTED", (void*)s);
        mk_lock_acquire(&s->lock);
        s->tx_error = 1;
        token = s->token;
        mk_cond_broadcast(&s->cond);
        mk_lock_release(&s->lock);
        mk_notify(t, token, MAGNETK_EV_WRITABLE);
        return QUIC_STATUS_SUCCESS;

    case QUIC_STREAM_EVENT_SHUTDOWN_COMPLETE: {
        MK_TRACE("stream %p (conn %p) SHUTDOWN_COMPLETE", (void*)s, (void*)s->conn);
        mk_lock_acquire(&s->lock);
        s->shutdown_complete = 1;
        if (!s->rx_fin) s->rx_reset = 1;         /* data already in the ring stays readable */
        s->tx_inflight = 0;
        int held = s->msquic_ref;
        s->msquic_ref = 0;
        token = s->token;
        mk_cond_broadcast(&s->cond);
        mk_lock_release(&s->lock);
        mk_notify(t, token, MAGNETK_EV_CLOSED | MAGNETK_EV_READABLE | MAGNETK_EV_WRITABLE);
        if (held) {
            conn_unlink_stream(s->conn, s);
            stream_release(s);
        }
        return QUIC_STATUS_SUCCESS;
    }

    default:
        return QUIC_STATUS_SUCCESS;
    }
}

MAGNETK_API magnetk_status_t magnetk_stream_open(magnetk_connection_t* c, magnetk_stream_t** out) {
    if (!c || !out) return MAGNETK_ERR_INVALID_PARAM;
    *out = NULL;
    mk_lock_acquire(&c->lock);
    int ok = c->connected && !c->shutting_down && !c->app_released;
    mk_lock_release(&c->lock);
    if (!ok) return MAGNETK_ERR_CLOSED;

    magnetk_stream_t* s = stream_alloc(c);
    if (!s) return MAGNETK_ERR_MEMORY;

    QUIC_STATUS st = c->t->api->StreamOpen(c->handle, QUIC_STREAM_OPEN_FLAG_NONE, stream_callback, s, &s->handle);
    if (QUIC_FAILED(st)) {
        conn_unlink_stream(c, s);
        s->handle = NULL;
        s->msquic_ref = 0;
        s->refs = 1;
        stream_release(s);
        return MAGNETK_ERR_STREAM;
    }
    st = c->t->api->StreamStart(s->handle, QUIC_STREAM_START_FLAG_IMMEDIATE);
    if (QUIC_FAILED(st)) {
        mk_lock_acquire(&s->lock);
        int held = s->msquic_ref;
        s->msquic_ref = 0;
        mk_lock_release(&s->lock);
        conn_unlink_stream(c, s);
        if (held) stream_release(s);             /* no SHUTDOWN_COMPLETE will follow */
        stream_release(s);
        return MAGNETK_ERR_STREAM;
    }
    *out = s;
    return MAGNETK_OK;
}

MAGNETK_API void magnetk_stream_set_token(magnetk_stream_t* s, uint64_t token) {
    if (!s) return;
    mk_lock_acquire(&s->lock);
    s->token = token;
    uint32_t ev = 0;
    if (s->rx_len || s->rx_fin || s->rx_reset) ev |= MAGNETK_EV_READABLE;
    if (s->tx_error || s->shutdown_complete) ev |= MAGNETK_EV_WRITABLE;
    mk_lock_release(&s->lock);
    if (ev) mk_notify(s->conn->t, token, ev);
}

MAGNETK_API uint64_t magnetk_stream_id(magnetk_stream_t* s) {
    if (!s) return 0;
    mk_lock_acquire(&s->lock);
    uint64_t id = s->id;
    mk_lock_release(&s->lock);
    return id;
}

MAGNETK_API magnetk_status_t magnetk_stream_send(
    magnetk_stream_t* s, const uint8_t* data, uint32_t length, int fin, uint32_t timeout_ms)
{
    if (!s || (!data && length)) return MAGNETK_ERR_INVALID_PARAM;
    if (length == 0 && !fin) return MAGNETK_OK;
    if (length == 0 && fin) return magnetk_stream_shutdown_send(s);

    uint64_t deadline = mk_deadline(timeout_ms);
    mk_lock_acquire(&s->lock);
    for (;;) {
        if (s->app_closed || s->shutdown_complete || s->tx_error) { mk_lock_release(&s->lock); return MAGNETK_ERR_SEND; }
        if (s->tx_fin) { mk_lock_release(&s->lock); return MAGNETK_ERR_CLOSED; }
        if (s->tx_inflight == 0 || s->tx_inflight + length <= s->tx_limit) break;
        if (timeout_ms == 0) { mk_lock_release(&s->lock); return MAGNETK_WOULD_BLOCK; }
        uint32_t rem = mk_remaining(deadline);
        if (rem == 0) { mk_lock_release(&s->lock); return MAGNETK_ERR_TIMEOUT; }
        mk_cond_wait(&s->cond, &s->lock, rem);
    }
    s->tx_inflight += length;
    if (fin) s->tx_fin = 1;
    mk_lock_release(&s->lock);

    mk_send_block* b = (mk_send_block*)malloc(sizeof(mk_send_block) + length);
    if (!b) {
        mk_lock_acquire(&s->lock);
        s->tx_inflight -= length;
        if (fin) s->tx_fin = 0;
        mk_lock_release(&s->lock);
        return MAGNETK_ERR_MEMORY;
    }
    uint8_t* payload = (uint8_t*)(b + 1);
    memcpy(payload, data, length);
    b->qb.Buffer = payload;
    b->qb.Length = length;
    b->length = length;

    QUIC_STATUS st = s->api->StreamSend(s->handle, &b->qb, 1, fin ? QUIC_SEND_FLAG_FIN : QUIC_SEND_FLAG_NONE, b);
    if (QUIC_FAILED(st)) {
        free(b);
        mk_lock_acquire(&s->lock);
        s->tx_inflight -= length;
        s->tx_error = 1;
        mk_lock_release(&s->lock);
        return MAGNETK_ERR_SEND;
    }
    return MAGNETK_OK;
}

MAGNETK_API magnetk_status_t magnetk_stream_recv(
    magnetk_stream_t* s, uint8_t* buffer, uint32_t buffer_size,
    uint32_t* out_length, uint32_t timeout_ms)
{
    if (!s || !buffer || !out_length || buffer_size == 0) return MAGNETK_ERR_INVALID_PARAM;
    *out_length = 0;
    uint64_t deadline = mk_deadline(timeout_ms);

    mk_lock_acquire(&s->lock);
    for (;;) {
        if (s->rx_len) {
            uint32_t n = s->rx_len < buffer_size ? s->rx_len : buffer_size;
            uint32_t first = s->rx_cap - s->rx_head;
            if (first > n) first = n;
            memcpy(buffer, s->rx_ring + s->rx_head, first);
            if (n > first) memcpy(buffer + first, s->rx_ring, n - first);
            s->rx_head = (s->rx_head + n) % s->rx_cap;
            s->rx_len -= n;
            if (s->rx_len == 0) s->rx_head = 0;
            int resume = 0;
            if (s->rx_paused && s->rx_len <= s->rx_max / 2 && !s->shutdown_complete) {
                s->rx_paused = 0;
                resume = 1;
            }
            HQUIC h = s->handle;
            mk_lock_release(&s->lock);
            if (resume) s->api->StreamReceiveSetEnabled(h, TRUE);
            *out_length = n;
            return MAGNETK_OK;
        }
        if (s->rx_fin) { mk_lock_release(&s->lock); return MAGNETK_ERR_CLOSED; }
        if (s->rx_reset) { mk_lock_release(&s->lock); return MAGNETK_ERR_RESET; }
        if (s->shutdown_complete || s->app_closed) { mk_lock_release(&s->lock); return MAGNETK_ERR_CLOSED; }
        if (timeout_ms == 0) { mk_lock_release(&s->lock); return MAGNETK_WOULD_BLOCK; }
        uint32_t rem = mk_remaining(deadline);
        if (rem == 0) { mk_lock_release(&s->lock); return MAGNETK_ERR_TIMEOUT; }
        mk_cond_wait(&s->cond, &s->lock, rem);
    }
}

MAGNETK_API magnetk_status_t magnetk_stream_flush(magnetk_stream_t* s, uint32_t timeout_ms) {
    if (!s) return MAGNETK_ERR_INVALID_PARAM;
    uint64_t deadline = mk_deadline(timeout_ms);
    mk_lock_acquire(&s->lock);
    for (;;) {
        if (s->tx_inflight == 0) { mk_lock_release(&s->lock); return MAGNETK_OK; }
        if (s->tx_error || s->shutdown_complete) { mk_lock_release(&s->lock); return MAGNETK_ERR_SEND; }
        if (timeout_ms == 0) { mk_lock_release(&s->lock); return MAGNETK_WOULD_BLOCK; }
        uint32_t rem = mk_remaining(deadline);
        if (rem == 0) { mk_lock_release(&s->lock); return MAGNETK_ERR_TIMEOUT; }
        mk_cond_wait(&s->cond, &s->lock, rem);
    }
}

MAGNETK_API magnetk_status_t magnetk_stream_shutdown_send(magnetk_stream_t* s) {
    if (!s) return MAGNETK_ERR_INVALID_PARAM;
    mk_lock_acquire(&s->lock);
    if (s->tx_fin) { mk_lock_release(&s->lock); return MAGNETK_OK; }
    if (s->shutdown_complete || s->app_closed) { mk_lock_release(&s->lock); return MAGNETK_ERR_CLOSED; }
    s->tx_fin = 1;
    mk_lock_release(&s->lock);
    QUIC_STATUS st = s->api->StreamShutdown(s->handle, QUIC_STREAM_SHUTDOWN_FLAG_GRACEFUL, 0);
    return QUIC_FAILED(st) ? MAGNETK_ERR_STREAM : MAGNETK_OK;
}

MAGNETK_API void magnetk_stream_abort(magnetk_stream_t* s, uint64_t error_code) {
    if (!s) return;
    mk_lock_acquire(&s->lock);
    int done = s->shutdown_complete;
    s->tx_fin = 1;
    s->tx_error = 1;
    mk_cond_broadcast(&s->cond);
    mk_lock_release(&s->lock);
    if (!done) {
        s->api->StreamShutdown(s->handle, QUIC_STREAM_SHUTDOWN_FLAG_ABORT, error_code & 0x3FFFFFFFFFFFFFFFull);
    }
}

MAGNETK_API void magnetk_stream_close(magnetk_stream_t* s) {
    if (!s) return;
    MK_TRACE("stream %p app close", (void*)s);
    mk_lock_acquire(&s->lock);
    if (s->app_closed) { mk_lock_release(&s->lock); return; }
    s->app_closed = 1;
    s->token = 0;
    int done = s->shutdown_complete;
    int need_fin = !s->tx_fin && !s->tx_error;
    int need_stop = !s->rx_fin && !s->rx_reset;
    s->tx_fin = 1;
    mk_cond_broadcast(&s->cond);
    mk_lock_release(&s->lock);

    MK_TRACE("stream %p close: done=%d stop=%d fin=%d", (void*)s, done, need_stop, need_fin);
    if (!done && s->handle) {
        if (need_stop) s->api->StreamShutdown(s->handle, QUIC_STREAM_SHUTDOWN_FLAG_ABORT_RECEIVE, 0);
        if (need_fin) s->api->StreamShutdown(s->handle, QUIC_STREAM_SHUTDOWN_FLAG_GRACEFUL, 0);
    }
    stream_release(s);                           /* application reference */
}

/* ===================================================================== */
/* Compatibility                                                          */
/* ===================================================================== */

MAGNETK_API magnetk_status_t magnetk_stream_send_sync(
    magnetk_stream_t* s, const uint8_t* data, uint32_t length, int fin)
{
    magnetk_status_t st = magnetk_stream_send(s, data, length, fin, 30000);
    return st;
}

MAGNETK_API magnetk_status_t magnetk_stream_recv_sync(
    magnetk_stream_t* s, uint8_t* buffer, uint32_t buffer_size,
    uint32_t* out_length, int* out_fin, uint32_t timeout_ms)
{
    magnetk_status_t st = magnetk_stream_recv(s, buffer, buffer_size, out_length, timeout_ms);
    if (out_fin) *out_fin = (st == MAGNETK_ERR_CLOSED) ? 1 : 0;
    return st;
}
