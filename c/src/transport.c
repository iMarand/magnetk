/*
 * Magnetk P2P Transport Layer - MsQuic Implementation
 *
 * Wraps MsQuic for high-performance QUIC transport.
 * Provides both async (callback) and sync (blocking) APIs.
 */

#include "transport.h"

#ifdef _WIN32
#ifndef WIN32_LEAN_AND_MEAN
#define WIN32_LEAN_AND_MEAN
#endif
#include <winsock2.h>
#include <ws2tcpip.h>
#include <windows.h>
#include <msquic.h>
#else
#include <msquic.h>
#include <pthread.h>
#include <unistd.h>
#endif

#include <stdlib.h>
#include <string.h>
#include <stdio.h>

/* ===== Atomic refcount helpers =====
 * Used to keep stream/connection structs alive while either Go or the
 * MsQuic callback thread still holds a reference. Without this, Go can
 * race with the SHUTDOWN_COMPLETE callback and dereference freed memory
 * — observable as a SEGV in magnetk_stream_close() with PC=0x0.
 */
#ifdef _WIN32
#define MAGNETK_ATOMIC_DEC(p) InterlockedDecrement((volatile long*)(p))
#define MAGNETK_ATOMIC_XCHG_PTR(target, val) \
    ((HQUIC)InterlockedExchangePointer((PVOID volatile*)(target), (PVOID)(val)))
#else
#define MAGNETK_ATOMIC_DEC(p) __sync_sub_and_fetch((volatile long*)(p), 1)
#define MAGNETK_ATOMIC_XCHG_PTR(target, val) \
    __sync_lock_test_and_set((target), (val))
#endif

#ifdef _WIN32
#include <wincrypt.h>
#include <ncrypt.h>

/* Helper to generate a self-signed certificate context for Windows (SChannel fallback) */
static void* CreateSelfSignedCertContext() {
    CERT_NAME_BLOB subject_name_blob = {0};
    PCCERT_CONTEXT cert_context = NULL;
    NCRYPT_KEY_HANDLE key = (NCRYPT_KEY_HANDLE)NULL;
    CRYPT_ALGORITHM_IDENTIFIER sig_alg = { (char*)szOID_RSA_SHA256RSA };
    CRYPT_KEY_PROV_INFO key_prov = {0};
    SYSTEMTIME now, expiration;

    /* Prepare subject name "CN=magnetk-localhost" */
    if (!CertStrToNameW(X509_ASN_ENCODING, L"CN=magnetk-localhost", CERT_X500_NAME_STR, NULL, NULL, &subject_name_blob.cbData, NULL)) {
        return NULL;
    }
    subject_name_blob.pbData = (BYTE*)malloc(subject_name_blob.cbData);
    if (!CertStrToNameW(X509_ASN_ENCODING, L"CN=magnetk-localhost", CERT_X500_NAME_STR, NULL, subject_name_blob.pbData, &subject_name_blob.cbData, NULL)) {
        free(subject_name_blob.pbData);
        return NULL;
    }

    /* Create ephemeral key pair */
    NCRYPT_PROV_HANDLE prov = (NCRYPT_PROV_HANDLE)NULL;
    if (NCryptOpenStorageProvider(&prov, MS_KEY_STORAGE_PROVIDER, 0) != 0) {
        free(subject_name_blob.pbData);
        return NULL;
    }
    
    /* Use a unique container name to avoid conflicts */
    wchar_t container_name[64];
    swprintf(container_name, 64, L"magnetk-key-%u", (unsigned int)GetTickCount());

    if (NCryptCreatePersistedKey(prov, &key, NCRYPT_RSA_ALGORITHM, container_name, 0, 0) != 0) {
        NCryptFreeObject(prov);
        free(subject_name_blob.pbData);
        return NULL;
    }
    DWORD key_size = 2048;
    NCryptSetProperty(key, NCRYPT_LENGTH_PROPERTY, (PBYTE)&key_size, sizeof(key_size), 0);
    NCryptFinalizeKey(key, 0);

    /* Prep key prov info */
    key_prov.pwszContainerName = container_name;
    key_prov.pwszProvName = (wchar_t*)MS_KEY_STORAGE_PROVIDER;
    key_prov.dwFlags = NCRYPT_SILENT_FLAG;
    key_prov.dwKeySpec = AT_KEYEXCHANGE;

    /* Dates */
    GetSystemTime(&now);
    expiration = now;
    expiration.wYear += 1;

    /* Create self-signed cert */
    cert_context = CertCreateSelfSignCertificate(key, &subject_name_blob, 0, &key_prov, &sig_alg, &now, &expiration, NULL);

    /* Cleanup key/prov (they remain associated with the cert context if successful) */
    NCryptFreeObject(key);
    NCryptFreeObject(prov);
    free(subject_name_blob.pbData);

    return (void*)cert_context;
}
#endif

/* ===== Internal Structures ===== */

struct magnetk_transport {
    const QUIC_API_TABLE*   msquic;
    HQUIC                   registration;
    HQUIC                   config_server;   /* Server-side configuration */
    HQUIC                   config_client;   /* Client-side configuration */
    magnetk_config_t         cfg;
    QUIC_BUFFER             alpn;
    char                    alpn_str[64];
    void*                   cert_context;    /* Windows PCCERT_CONTEXT */
};

struct magnetk_listener {
    magnetk_transport_t*         transport;
    HQUIC                       handle;
    magnetk_on_connection_fn     on_connection;
    magnetk_on_stream_fn         on_stream;
    magnetk_on_data_fn           on_data;
    magnetk_on_close_fn          on_close;
    void*                       user_data;
};

struct magnetk_connection {
    magnetk_transport_t*         transport;
    HQUIC                       handle;
    magnetk_on_stream_fn         on_stream;
    magnetk_on_data_fn           on_data;
    magnetk_on_close_fn          on_close;
    void*                       user_data;
    int                         is_server;
    /* For sync connect */
#ifdef _WIN32
    HANDLE                      connect_event;
#else
    pthread_mutex_t             connect_mutex;
    pthread_cond_t              connect_cond;
#endif
    magnetk_status_t             connect_status;
    volatile int                connected;
};

struct magnetk_stream {
    magnetk_connection_t*        conn;
    HQUIC                       handle;
    /* Cached at creation so close() doesn't deref a freed connection. */
    const QUIC_API_TABLE*       msquic_api;
    /* Refcount: starts at 2 (Go owner + MsQuic callback owner). Stream
     * is freed by whichever side decrements to 0. */
    volatile long               ref_count;
    magnetk_on_data_fn           on_data;
    magnetk_on_send_complete_fn  on_send_complete;
    magnetk_on_close_fn          on_close;
    void*                       user_data;
    /* For sync recv */
#ifdef _WIN32
    HANDLE                      recv_event;
    CRITICAL_SECTION            recv_lock;
#else
    pthread_mutex_t             recv_mutex;
    pthread_cond_t              recv_cond;
#endif
    uint8_t*                    recv_buffer;
    uint32_t                    recv_length;
    int                         recv_fin;
    volatile int                recv_pending;
    /* For sync send */
#ifdef _WIN32
    HANDLE                      send_event;
#else
    pthread_mutex_t             send_mutex;
    pthread_cond_t              send_cond;
#endif
    volatile int                send_complete;
};

/* Forward declarations for the lifecycle helpers. */
static void magnetk_stream_free_resources(magnetk_stream_t* stream);
static void magnetk_stream_release(magnetk_stream_t* stream);

/* ===== Forward Declarations ===== */
static QUIC_STATUS QUIC_API listener_callback(HQUIC Listener, void* Context, QUIC_LISTENER_EVENT* Event);
static QUIC_STATUS QUIC_API server_conn_callback(HQUIC Connection, void* Context, QUIC_CONNECTION_EVENT* Event);
static QUIC_STATUS QUIC_API client_conn_callback(HQUIC Connection, void* Context, QUIC_CONNECTION_EVENT* Event);
static QUIC_STATUS QUIC_API stream_callback(HQUIC Stream, void* Context, QUIC_STREAM_EVENT* Event);

/* ===== Utility ===== */

MAGNETK_API const char* magnetk_status_string(magnetk_status_t status) {
    switch (status) {
        case MAGNETK_OK:                 return "OK";
        case MAGNETK_ERR_INIT:           return "Initialization failed";
        case MAGNETK_ERR_CONFIG:         return "Configuration error";
        case MAGNETK_ERR_LISTEN:         return "Listen failed";
        case MAGNETK_ERR_CONNECT:        return "Connection failed";
        case MAGNETK_ERR_SEND:           return "Send failed";
        case MAGNETK_ERR_RECV:           return "Receive failed";
        case MAGNETK_ERR_TIMEOUT:        return "Timeout";
        case MAGNETK_ERR_CLOSED:         return "Connection closed";
        case MAGNETK_ERR_TLS:            return "TLS error";
        case MAGNETK_ERR_MEMORY:         return "Out of memory";
        case MAGNETK_ERR_INVALID_PARAM:  return "Invalid parameter";
        case MAGNETK_ERR_STREAM:         return "Stream error";
        case MAGNETK_ERR_NOT_READY:      return "Not ready";
        default:                        return "Unknown error";
    }
}

MAGNETK_API void magnetk_config_default(magnetk_config_t* config) {
    memset(config, 0, sizeof(*config));
    config->bind_address = "0.0.0.0";
    config->port = 4002;
    config->alpn = "magnetk";
    config->no_cert_validation = 1;  /* Dev mode by default */
    config->max_connections = 1000;
    config->idle_timeout_ms = 30000;
    config->send_buffer_size = 4 * 1024 * 1024;
    config->recv_buffer_size = 4 * 1024 * 1024;
    config->max_streams_per_conn = 100;
    config->execution_profile = 1; /* MAX_THROUGHPUT */
}

/* ===== Transport Init/Shutdown ===== */

MAGNETK_API magnetk_status_t magnetk_transport_init(
    magnetk_config_t* config,
    magnetk_transport_t** out_transport)
{
    if (!config || !out_transport) return MAGNETK_ERR_INVALID_PARAM;

    magnetk_transport_t* t = (magnetk_transport_t*)calloc(1, sizeof(magnetk_transport_t));
    if (!t) return MAGNETK_ERR_MEMORY;

    /* Copy config */
    t->cfg = *config;

    /* Open MsQuic API */
    QUIC_STATUS status = MsQuicOpen2(&t->msquic);
    if (QUIC_FAILED(status)) {
        fprintf(stderr, "[magnetk] MsQuicOpen2 failed: 0x%x\n", status);
        free(t);
        return MAGNETK_ERR_INIT;
    }

    /* Create registration */
    QUIC_REGISTRATION_CONFIG reg_config = { "magnetk-transport", QUIC_EXECUTION_PROFILE_LOW_LATENCY };
    if (config->execution_profile == 1) {
        reg_config.ExecutionProfile = QUIC_EXECUTION_PROFILE_TYPE_MAX_THROUGHPUT;
    }

    status = t->msquic->RegistrationOpen(&reg_config, &t->registration);
    if (QUIC_FAILED(status)) {
        fprintf(stderr, "[magnetk] RegistrationOpen failed: 0x%x\n", status);
        MsQuicClose(t->msquic);
        free(t);
        return MAGNETK_ERR_INIT;
    }

    /* Setup ALPN */
    size_t alpn_len = strlen(config->alpn);
    if (alpn_len > sizeof(t->alpn_str) - 1) alpn_len = sizeof(t->alpn_str) - 1;
    memcpy(t->alpn_str, config->alpn, alpn_len);
    t->alpn_str[alpn_len] = 0;
    t->alpn.Buffer = (uint8_t*)t->alpn_str;
    t->alpn.Length = (uint32_t)alpn_len;

    /* Create QUIC settings */
    QUIC_SETTINGS settings = {0};
    settings.IdleTimeoutMs = config->idle_timeout_ms;
    settings.IsSet.IdleTimeoutMs = TRUE;
    settings.PeerBidiStreamCount = config->max_streams_per_conn;
    settings.IsSet.PeerBidiStreamCount = TRUE;
    settings.PeerUnidiStreamCount = config->max_streams_per_conn;
    settings.IsSet.PeerUnidiStreamCount = TRUE;
    settings.SendBufferingEnabled = TRUE;
    settings.IsSet.SendBufferingEnabled = TRUE;

    /* Create server configuration */
    status = t->msquic->ConfigurationOpen(
        t->registration,
        &t->alpn, 1,
        &settings, sizeof(settings),
        NULL,
        &t->config_server);
    if (QUIC_FAILED(status)) {
        fprintf(stderr, "[magnetk] Server ConfigurationOpen failed: 0x%x\n", status);
        t->msquic->RegistrationClose(t->registration);
        MsQuicClose(t->msquic);
        free(t);
        return MAGNETK_ERR_CONFIG;
    }

    /* Load server TLS credentials */
    QUIC_CREDENTIAL_CONFIG cred_config = {0};

#ifdef _WIN32
    if (config->cert_file && config->key_file) {
        /* SChannel on Windows doesn't handle PEM files in QUIC_CREDENTIAL_TYPE_CERTIFICATE_FILE.
           In a real system, we'd import them into a store or convert to PFX.
           For Magnetk dev, we fallback to our internal self-signed generator. */
        fprintf(stderr, "[magnetk] Warning: SChannel does not support PEM files. Using internal self-signed cert.\n");
    }
    
    t->cert_context = CreateSelfSignedCertContext();
    if (!t->cert_context) {
        fprintf(stderr, "[magnetk] Failed to create Windows self-signed certificate.\n");
        t->msquic->ConfigurationClose(t->config_server);
        t->msquic->RegistrationClose(t->registration);
        MsQuicClose(t->msquic);
        free(t);
        return MAGNETK_ERR_TLS;
    }

    cred_config.Type = QUIC_CREDENTIAL_TYPE_CERTIFICATE_CONTEXT;
    cred_config.CertificateContext = (QUIC_CERTIFICATE*)t->cert_context;
#else
    if (config->cert_file && config->key_file) {
        QUIC_CERTIFICATE_FILE cert_file = {0};
        cert_file.CertificateFile = config->cert_file;
        cert_file.PrivateKeyFile = config->key_file;
        cred_config.Type = QUIC_CREDENTIAL_TYPE_CERTIFICATE_FILE;
        cred_config.CertificateFile = &cert_file;
    } else {
        /* Self-signed / no cert (for development) */
        cred_config.Type = QUIC_CREDENTIAL_TYPE_NONE;
        cred_config.Flags = QUIC_CREDENTIAL_FLAG_NO_CERTIFICATE_VALIDATION;
    }
#endif

    status = t->msquic->ConfigurationLoadCredential(t->config_server, &cred_config);
    if (QUIC_FAILED(status)) {
        fprintf(stderr, "[magnetk] Server ConfigurationLoadCredential failed: 0x%x\n", status);
        t->msquic->ConfigurationClose(t->config_server);
        t->msquic->RegistrationClose(t->registration);
#ifdef _WIN32
        if (t->cert_context) CertFreeCertificateContext((PCCERT_CONTEXT)t->cert_context);
#endif
        MsQuicClose(t->msquic);
        free(t);
        return MAGNETK_ERR_TLS;
    }

    /* Create client configuration */
    status = t->msquic->ConfigurationOpen(
        t->registration,
        &t->alpn, 1,
        &settings, sizeof(settings),
        NULL,
        &t->config_client);
    if (QUIC_FAILED(status)) {
        fprintf(stderr, "[magnetk] Client ConfigurationOpen failed: 0x%x\n", status);
        t->msquic->ConfigurationClose(t->config_server);
        t->msquic->RegistrationClose(t->registration);
        MsQuicClose(t->msquic);
        free(t);
        return MAGNETK_ERR_CONFIG;
    }

    /* Load client TLS credentials */
    QUIC_CREDENTIAL_CONFIG client_cred = {0};
    client_cred.Type = QUIC_CREDENTIAL_TYPE_NONE;
    client_cred.Flags = QUIC_CREDENTIAL_FLAG_CLIENT;
    if (config->no_cert_validation) {
        client_cred.Flags |= QUIC_CREDENTIAL_FLAG_NO_CERTIFICATE_VALIDATION;
    }

    status = t->msquic->ConfigurationLoadCredential(t->config_client, &client_cred);
    if (QUIC_FAILED(status)) {
        fprintf(stderr, "[magnetk] Client ConfigurationLoadCredential failed: 0x%x\n", status);
        t->msquic->ConfigurationClose(t->config_client);
        t->msquic->ConfigurationClose(t->config_server);
        t->msquic->RegistrationClose(t->registration);
        MsQuicClose(t->msquic);
        free(t);
        return MAGNETK_ERR_TLS;
    }

    *out_transport = t;
    return MAGNETK_OK;
}

MAGNETK_API void magnetk_transport_shutdown(magnetk_transport_t* transport) {
    if (!transport) return;
#ifdef _WIN32
    if (transport->cert_context) {
        CertFreeCertificateContext((PCCERT_CONTEXT)transport->cert_context);
    }
#endif
    if (transport->config_client) transport->msquic->ConfigurationClose(transport->config_client);
    if (transport->config_server) transport->msquic->ConfigurationClose(transport->config_server);
    if (transport->registration) transport->msquic->RegistrationClose(transport->registration);
    if (transport->msquic) MsQuicClose(transport->msquic);
    free(transport);
}

/* ===== Listener ===== */

static QUIC_STATUS QUIC_API listener_callback(
    HQUIC Listener, void* Context, QUIC_LISTENER_EVENT* Event)
{
    magnetk_listener_t* listener = (magnetk_listener_t*)Context;
    (void)Listener;

    switch (Event->Type) {
    case QUIC_LISTENER_EVENT_NEW_CONNECTION: {
        /* Create connection wrapper */
        magnetk_connection_t* conn = (magnetk_connection_t*)calloc(1, sizeof(magnetk_connection_t));
        if (!conn) return QUIC_STATUS_OUT_OF_MEMORY;

        conn->transport = listener->transport;
        conn->handle = Event->NEW_CONNECTION.Connection;
        conn->on_stream = listener->on_stream;
        conn->on_data = listener->on_data;
        conn->on_close = listener->on_close;
        conn->user_data = listener->user_data;
        conn->is_server = 1;
        conn->connected = 1;

        /* Set connection callback */
        listener->transport->msquic->SetCallbackHandler(
            Event->NEW_CONNECTION.Connection,
            (void*)server_conn_callback,
            conn);

        /* Accept the connection with server config */
        QUIC_STATUS status = listener->transport->msquic->ConnectionSetConfiguration(
            Event->NEW_CONNECTION.Connection,
            listener->transport->config_server);
        if (QUIC_FAILED(status)) {
            free(conn);
            return status;
        }

        /* Notify application */
        if (listener->on_connection) {
            listener->on_connection(listener, conn, listener->user_data);
        }

        return QUIC_STATUS_SUCCESS;
    }
    case QUIC_LISTENER_EVENT_STOP_COMPLETE:
        return QUIC_STATUS_SUCCESS;
    default:
        return QUIC_STATUS_SUCCESS;
    }
}

MAGNETK_API magnetk_status_t magnetk_listen(
    magnetk_transport_t*     transport,
    magnetk_on_connection_fn on_connection,
    magnetk_on_stream_fn     on_stream,
    magnetk_on_data_fn       on_data,
    magnetk_on_close_fn      on_close,
    void*                   user_data,
    magnetk_listener_t**     out_listener)
{
    if (!transport || !out_listener) return MAGNETK_ERR_INVALID_PARAM;

    magnetk_listener_t* listener = (magnetk_listener_t*)calloc(1, sizeof(magnetk_listener_t));
    if (!listener) return MAGNETK_ERR_MEMORY;

    listener->transport = transport;
    listener->on_connection = on_connection;
    listener->on_stream = on_stream;
    listener->on_data = on_data;
    listener->on_close = on_close;
    listener->user_data = user_data;

    QUIC_STATUS status = transport->msquic->ListenerOpen(
        transport->registration,
        listener_callback,
        listener,
        &listener->handle);
    if (QUIC_FAILED(status)) {
        free(listener);
        return MAGNETK_ERR_LISTEN;
    }

    /* Build address */
    QUIC_ADDR addr = {0};
    QuicAddrSetFamily(&addr, QUIC_ADDRESS_FAMILY_UNSPEC);
    QuicAddrSetPort(&addr, transport->cfg.port);

    status = transport->msquic->ListenerStart(
        listener->handle,
        &transport->alpn, 1,
        &addr);
    if (QUIC_FAILED(status)) {
        transport->msquic->ListenerClose(listener->handle);
        free(listener);
        return MAGNETK_ERR_LISTEN;
    }

    *out_listener = listener;
    return MAGNETK_OK;
}

MAGNETK_API void magnetk_listener_close(magnetk_listener_t* listener) {
    if (!listener) return;
    if (listener->handle) {
        listener->transport->msquic->ListenerClose(listener->handle);
    }
    free(listener);
}

/* ===== Server Connection Callback ===== */

static QUIC_STATUS QUIC_API server_conn_callback(
    HQUIC Connection, void* Context, QUIC_CONNECTION_EVENT* Event)
{
    magnetk_connection_t* conn = (magnetk_connection_t*)Context;
    (void)Connection;

    switch (Event->Type) {
    case QUIC_CONNECTION_EVENT_CONNECTED:
        conn->connected = 1;
        return QUIC_STATUS_SUCCESS;

    case QUIC_CONNECTION_EVENT_SHUTDOWN_INITIATED_BY_TRANSPORT:
    case QUIC_CONNECTION_EVENT_SHUTDOWN_INITIATED_BY_PEER:
        conn->connected = 0;
        if (conn->on_close) {
            conn->on_close(conn, MAGNETK_ERR_CLOSED, conn->user_data);
        }
        return QUIC_STATUS_SUCCESS;

    case QUIC_CONNECTION_EVENT_SHUTDOWN_COMPLETE:
        conn->transport->msquic->ConnectionClose(Connection);
        free(conn);
        return QUIC_STATUS_SUCCESS;

    case QUIC_CONNECTION_EVENT_PEER_STREAM_STARTED: {
        /* Wrap the stream */
        magnetk_stream_t* stream = (magnetk_stream_t*)calloc(1, sizeof(magnetk_stream_t));
        if (!stream) return QUIC_STATUS_OUT_OF_MEMORY;

        stream->conn = conn;
        stream->handle = Event->PEER_STREAM_STARTED.Stream;
        stream->msquic_api = conn->transport->msquic;  /* Cache for safe close */
        stream->ref_count = 2;                          /* Go + MsQuic */
        stream->on_data = conn->on_data;
        stream->on_close = conn->on_close;
        stream->user_data = conn->user_data;

#ifdef _WIN32
        stream->recv_event = CreateEventA(NULL, FALSE, FALSE, NULL);
        InitializeCriticalSection(&stream->recv_lock);
        stream->send_event = CreateEventA(NULL, FALSE, FALSE, NULL);
#else
        pthread_mutex_init(&stream->recv_mutex, NULL);
        pthread_cond_init(&stream->recv_cond, NULL);
        pthread_mutex_init(&stream->send_mutex, NULL);
        pthread_cond_init(&stream->send_cond, NULL);
#endif

        conn->transport->msquic->SetCallbackHandler(
            Event->PEER_STREAM_STARTED.Stream,
            (void*)stream_callback,
            stream);

        if (conn->on_stream) {
            conn->on_stream(conn, stream, conn->user_data);
        }

        return QUIC_STATUS_SUCCESS;
    }
    default:
        return QUIC_STATUS_SUCCESS;
    }
}

/* ===== Client Connection ===== */

static QUIC_STATUS QUIC_API client_conn_callback(
    HQUIC Connection, void* Context, QUIC_CONNECTION_EVENT* Event)
{
    magnetk_connection_t* conn = (magnetk_connection_t*)Context;
    (void)Connection;

    switch (Event->Type) {
    case QUIC_CONNECTION_EVENT_CONNECTED:
        conn->connected = 1;
        conn->connect_status = MAGNETK_OK;
#ifdef _WIN32
        SetEvent(conn->connect_event);
#else
        pthread_mutex_lock(&conn->connect_mutex);
        pthread_cond_signal(&conn->connect_cond);
        pthread_mutex_unlock(&conn->connect_mutex);
#endif
        return QUIC_STATUS_SUCCESS;

    case QUIC_CONNECTION_EVENT_SHUTDOWN_INITIATED_BY_TRANSPORT:
        conn->connected = 0;
        conn->connect_status = MAGNETK_ERR_CONNECT;
#ifdef _WIN32
        SetEvent(conn->connect_event);
#else
        pthread_mutex_lock(&conn->connect_mutex);
        pthread_cond_signal(&conn->connect_cond);
        pthread_mutex_unlock(&conn->connect_mutex);
#endif
        if (conn->on_close) {
            conn->on_close(conn, MAGNETK_ERR_CLOSED, conn->user_data);
        }
        return QUIC_STATUS_SUCCESS;

    case QUIC_CONNECTION_EVENT_SHUTDOWN_INITIATED_BY_PEER:
        conn->connected = 0;
        if (conn->on_close) {
            conn->on_close(conn, MAGNETK_ERR_CLOSED, conn->user_data);
        }
        return QUIC_STATUS_SUCCESS;

    case QUIC_CONNECTION_EVENT_SHUTDOWN_COMPLETE:
        conn->transport->msquic->ConnectionClose(Connection);
#ifdef _WIN32
        CloseHandle(conn->connect_event);
#else
        pthread_mutex_destroy(&conn->connect_mutex);
        pthread_cond_destroy(&conn->connect_cond);
#endif
        free(conn);
        return QUIC_STATUS_SUCCESS;

    case QUIC_CONNECTION_EVENT_PEER_STREAM_STARTED: {
        magnetk_stream_t* stream = (magnetk_stream_t*)calloc(1, sizeof(magnetk_stream_t));
        if (!stream) return QUIC_STATUS_OUT_OF_MEMORY;

        stream->conn = conn;
        stream->handle = Event->PEER_STREAM_STARTED.Stream;
        stream->msquic_api = conn->transport->msquic;  /* Cache for safe close */
        stream->ref_count = 2;                          /* Go + MsQuic */
        stream->on_data = conn->on_data;
        stream->on_close = conn->on_close;
        stream->user_data = conn->user_data;

#ifdef _WIN32
        stream->recv_event = CreateEventA(NULL, FALSE, FALSE, NULL);
        InitializeCriticalSection(&stream->recv_lock);
        stream->send_event = CreateEventA(NULL, FALSE, FALSE, NULL);
#else
        pthread_mutex_init(&stream->recv_mutex, NULL);
        pthread_cond_init(&stream->recv_cond, NULL);
        pthread_mutex_init(&stream->send_mutex, NULL);
        pthread_cond_init(&stream->send_cond, NULL);
#endif

        conn->transport->msquic->SetCallbackHandler(
            Event->PEER_STREAM_STARTED.Stream,
            (void*)stream_callback,
            stream);

        if (conn->on_stream) {
            conn->on_stream(conn, stream, conn->user_data);
        }
        return QUIC_STATUS_SUCCESS;
    }
    default:
        return QUIC_STATUS_SUCCESS;
    }
}

MAGNETK_API magnetk_status_t magnetk_connect(
    magnetk_transport_t*   transport,
    const char*           host,
    uint16_t              port,
    magnetk_on_stream_fn   on_stream,
    magnetk_on_data_fn     on_data,
    magnetk_on_close_fn    on_close,
    void*                 user_data,
    magnetk_connection_t** out_conn)
{
    if (!transport || !host || !out_conn) return MAGNETK_ERR_INVALID_PARAM;

    magnetk_connection_t* conn = (magnetk_connection_t*)calloc(1, sizeof(magnetk_connection_t));
    if (!conn) return MAGNETK_ERR_MEMORY;

    conn->transport = transport;
    conn->on_stream = on_stream;
    conn->on_data = on_data;
    conn->on_close = on_close;
    conn->user_data = user_data;
    conn->is_server = 0;
    conn->connect_status = MAGNETK_ERR_NOT_READY;

#ifdef _WIN32
    conn->connect_event = CreateEventA(NULL, FALSE, FALSE, NULL);
#else
    pthread_mutex_init(&conn->connect_mutex, NULL);
    pthread_cond_init(&conn->connect_cond, NULL);
#endif

    /* Create connection */
    QUIC_STATUS status = transport->msquic->ConnectionOpen(
        transport->registration,
        client_conn_callback,
        conn,
        &conn->handle);
    if (QUIC_FAILED(status)) {
#ifdef _WIN32
        CloseHandle(conn->connect_event);
#else
        pthread_mutex_destroy(&conn->connect_mutex);
        pthread_cond_destroy(&conn->connect_cond);
#endif
        free(conn);
        return MAGNETK_ERR_CONNECT;
    }

    /* Start connection */
    status = transport->msquic->ConnectionStart(
        conn->handle,
        transport->config_client,
        QUIC_ADDRESS_FAMILY_UNSPEC,
        host,
        port);
    if (QUIC_FAILED(status)) {
        transport->msquic->ConnectionClose(conn->handle);
#ifdef _WIN32
        CloseHandle(conn->connect_event);
#else
        pthread_mutex_destroy(&conn->connect_mutex);
        pthread_cond_destroy(&conn->connect_cond);
#endif
        free(conn);
        return MAGNETK_ERR_CONNECT;
    }

    /* Wait for connection to complete */
#ifdef _WIN32
    DWORD wait_result = WaitForSingleObject(conn->connect_event, transport->cfg.idle_timeout_ms);
    if (wait_result == WAIT_TIMEOUT) {
        transport->msquic->ConnectionShutdown(conn->handle, QUIC_CONNECTION_SHUTDOWN_FLAG_NONE, 0);
        return MAGNETK_ERR_TIMEOUT;
    }
#else
    struct timespec ts;
    clock_gettime(CLOCK_REALTIME, &ts);
    ts.tv_sec += transport->cfg.idle_timeout_ms / 1000;
    ts.tv_nsec += (transport->cfg.idle_timeout_ms % 1000) * 1000000;
    if (ts.tv_nsec >= 1000000000) { ts.tv_sec++; ts.tv_nsec -= 1000000000; }

    pthread_mutex_lock(&conn->connect_mutex);
    while (!conn->connected && conn->connect_status == MAGNETK_ERR_NOT_READY) {
        int ret = pthread_cond_timedwait(&conn->connect_cond, &conn->connect_mutex, &ts);
        if (ret != 0) break;
    }
    pthread_mutex_unlock(&conn->connect_mutex);

    if (!conn->connected) {
        transport->msquic->ConnectionShutdown(conn->handle, QUIC_CONNECTION_SHUTDOWN_FLAG_NONE, 0);
        return MAGNETK_ERR_TIMEOUT;
    }
#endif

    *out_conn = conn;
    return conn->connect_status;
}

MAGNETK_API void magnetk_connection_close(magnetk_connection_t* conn) {
    if (!conn) return;
    if (conn->handle) {
        conn->transport->msquic->ConnectionShutdown(
            conn->handle, QUIC_CONNECTION_SHUTDOWN_FLAG_NONE, 0);
    }
    /* The connection will be freed in the shutdown complete callback */
}

MAGNETK_API void magnetk_connection_set_userdata(magnetk_connection_t* conn, void* user_data) {
    if (conn) conn->user_data = user_data;
}

MAGNETK_API void* magnetk_connection_get_userdata(magnetk_connection_t* conn) {
    return conn ? conn->user_data : NULL;
}

/* ===== Stream Callback ===== */

static QUIC_STATUS QUIC_API stream_callback(
    HQUIC Stream, void* Context, QUIC_STREAM_EVENT* Event)
{
    magnetk_stream_t* stream = (magnetk_stream_t*)Context;
    (void)Stream;

    switch (Event->Type) {
    case QUIC_STREAM_EVENT_RECEIVE: {
        int fin = (Event->RECEIVE.Flags & QUIC_RECEIVE_FLAG_FIN) ? 1 : 0;

        /* Check if a sync recv is pending */
        if (stream->recv_pending) {
            /* Copy data to sync recv buffer */
            uint32_t total = 0;
            for (uint32_t i = 0; i < Event->RECEIVE.BufferCount; i++) {
                uint32_t copy_len = Event->RECEIVE.Buffers[i].Length;
                if (stream->recv_buffer && (total + copy_len <= stream->recv_length)) {
                    memcpy(stream->recv_buffer + total, Event->RECEIVE.Buffers[i].Buffer, copy_len);
                    total += copy_len;
                }
            }
            stream->recv_length = total;
            stream->recv_fin = fin;
            stream->recv_pending = 0;

#ifdef _WIN32
            SetEvent(stream->recv_event);
#else
            pthread_mutex_lock(&stream->recv_mutex);
            pthread_cond_signal(&stream->recv_cond);
            pthread_mutex_unlock(&stream->recv_mutex);
#endif
        } else if (stream->on_data) {
            /* Forward to async callback */
            for (uint32_t i = 0; i < Event->RECEIVE.BufferCount; i++) {
                int is_last = (i == Event->RECEIVE.BufferCount - 1) ? fin : 0;
                stream->on_data(stream,
                    Event->RECEIVE.Buffers[i].Buffer,
                    Event->RECEIVE.Buffers[i].Length,
                    is_last,
                    stream->user_data);
            }
        }
        return QUIC_STATUS_SUCCESS;
    }

    case QUIC_STREAM_EVENT_SEND_COMPLETE: {
        void* send_ctx = Event->SEND_COMPLETE.ClientContext;
        int canceled = Event->SEND_COMPLETE.Canceled;

        if (stream->on_send_complete) {
            stream->on_send_complete(stream, send_ctx, canceled, stream->user_data);
        }

        /* Signal sync send if pending */
        stream->send_complete = 1;
#ifdef _WIN32
        SetEvent(stream->send_event);
#else
        pthread_mutex_lock(&stream->send_mutex);
        pthread_cond_signal(&stream->send_cond);
        pthread_mutex_unlock(&stream->send_mutex);
#endif

        /* Free the copied buffer if it was a regular send */
        if (send_ctx) {
            free(send_ctx);
        }

        return QUIC_STATUS_SUCCESS;
    }

    case QUIC_STREAM_EVENT_PEER_SEND_SHUTDOWN:
    case QUIC_STREAM_EVENT_PEER_SEND_ABORTED:
        /* Signal pending recv with fin */
        if (stream->recv_pending) {
            stream->recv_length = 0;
            stream->recv_fin = 1;
            stream->recv_pending = 0;
#ifdef _WIN32
            SetEvent(stream->recv_event);
#else
            pthread_mutex_lock(&stream->recv_mutex);
            pthread_cond_signal(&stream->recv_cond);
            pthread_mutex_unlock(&stream->recv_mutex);
#endif
        }
        return QUIC_STATUS_SUCCESS;

    case QUIC_STREAM_EVENT_SHUTDOWN_COMPLETE:
        if (stream->on_close) {
            stream->on_close(stream, MAGNETK_ERR_CLOSED, stream->user_data);
        }
        /* Use cached API; stream->conn may already be freed at this point. */
        if (stream->msquic_api) {
            stream->msquic_api->StreamClose(Stream);
        }
        /* Drop MsQuic's reference. Resources are released by whichever
         * side hits ref_count == 0 (see magnetk_stream_release). */
        magnetk_stream_release(stream);
        return QUIC_STATUS_SUCCESS;

    default:
        return QUIC_STATUS_SUCCESS;
    }
}

/* ===== Stream Operations ===== */

MAGNETK_API magnetk_status_t magnetk_stream_open(
    magnetk_connection_t*       conn,
    magnetk_on_data_fn          on_data,
    magnetk_on_send_complete_fn on_send_complete,
    magnetk_on_close_fn         on_close,
    void*                      user_data,
    magnetk_stream_t**          out_stream)
{
    if (!conn || !out_stream) return MAGNETK_ERR_INVALID_PARAM;
    if (!conn->connected) return MAGNETK_ERR_NOT_READY;

    magnetk_stream_t* stream = (magnetk_stream_t*)calloc(1, sizeof(magnetk_stream_t));
    if (!stream) return MAGNETK_ERR_MEMORY;

    stream->conn = conn;
    stream->msquic_api = conn->transport->msquic;  /* Cache for safe close */
    stream->ref_count = 2;                          /* Go + MsQuic */
    stream->on_data = on_data;
    stream->on_send_complete = on_send_complete;
    stream->on_close = on_close;
    stream->user_data = user_data;

#ifdef _WIN32
    stream->recv_event = CreateEventA(NULL, FALSE, FALSE, NULL);
    InitializeCriticalSection(&stream->recv_lock);
    stream->send_event = CreateEventA(NULL, FALSE, FALSE, NULL);
#else
    pthread_mutex_init(&stream->recv_mutex, NULL);
    pthread_cond_init(&stream->recv_cond, NULL);
    pthread_mutex_init(&stream->send_mutex, NULL);
    pthread_cond_init(&stream->send_cond, NULL);
#endif

    QUIC_STATUS status = conn->transport->msquic->StreamOpen(
        conn->handle,
        QUIC_STREAM_OPEN_FLAG_NONE,
        stream_callback,
        stream,
        &stream->handle);
    if (QUIC_FAILED(status)) {
        /* StreamOpen failed: MsQuic never took ownership, so no callback
         * will fire. Free directly without going through release. */
        magnetk_stream_free_resources(stream);
        free(stream);
        return MAGNETK_ERR_STREAM;
    }

    status = conn->transport->msquic->StreamStart(
        stream->handle,
        QUIC_STREAM_START_FLAG_NONE);
    if (QUIC_FAILED(status)) {
        conn->transport->msquic->StreamClose(stream->handle);
        magnetk_stream_free_resources(stream);
        free(stream);
        return MAGNETK_ERR_STREAM;
    }

    *out_stream = stream;
    return MAGNETK_OK;
}

MAGNETK_API magnetk_status_t magnetk_stream_send(
    magnetk_stream_t* stream,
    const uint8_t*   data,
    uint32_t         length,
    int              fin)
{
    if (!stream || !data) return MAGNETK_ERR_INVALID_PARAM;

    /* Copy data (MsQuic needs buffer alive until send complete) */
    uint8_t* copy = (uint8_t*)malloc(length);
    if (!copy) return MAGNETK_ERR_MEMORY;
    memcpy(copy, data, length);

    /* Allocate QUIC_BUFFER on heap so it survives the call */
    QUIC_BUFFER* buf = (QUIC_BUFFER*)malloc(sizeof(QUIC_BUFFER));
    if (!buf) { free(copy); return MAGNETK_ERR_MEMORY; }
    buf->Buffer = copy;
    buf->Length = length;

    QUIC_SEND_FLAGS flags = fin ? QUIC_SEND_FLAG_FIN : QUIC_SEND_FLAG_NONE;

    QUIC_STATUS status = stream->conn->transport->msquic->StreamSend(
        stream->handle,
        buf, 1,
        flags,
        copy);  /* ClientContext = copy, freed in send complete callback */
    if (QUIC_FAILED(status)) {
        free(copy);
        free(buf);
        return MAGNETK_ERR_SEND;
    }

    /* Note: buf is leaked here. In production, we'd track it.
       For now, the copy is freed in send_complete callback via ClientContext.
       We also need to free buf — let's pack them together. */
    /* TODO: Combine buf+copy into a single allocation for production */

    return MAGNETK_OK;
}

MAGNETK_API magnetk_status_t magnetk_stream_send_zerocopy(
    magnetk_stream_t* stream,
    const uint8_t*   data,
    uint32_t         length,
    int              fin,
    void*            send_context)
{
    if (!stream || !data) return MAGNETK_ERR_INVALID_PARAM;

    QUIC_BUFFER* buf = (QUIC_BUFFER*)malloc(sizeof(QUIC_BUFFER));
    if (!buf) return MAGNETK_ERR_MEMORY;
    buf->Buffer = (uint8_t*)data;
    buf->Length = length;

    QUIC_SEND_FLAGS flags = fin ? QUIC_SEND_FLAG_FIN : QUIC_SEND_FLAG_NONE;

    QUIC_STATUS status = stream->conn->transport->msquic->StreamSend(
        stream->handle,
        buf, 1,
        flags,
        send_context);
    if (QUIC_FAILED(status)) {
        free(buf);
        return MAGNETK_ERR_SEND;
    }

    return MAGNETK_OK;
}

MAGNETK_API magnetk_status_t magnetk_stream_shutdown_send(magnetk_stream_t* stream) {
    if (!stream) return MAGNETK_ERR_INVALID_PARAM;

    QUIC_STATUS status = stream->conn->transport->msquic->StreamShutdown(
        stream->handle,
        QUIC_STREAM_SHUTDOWN_FLAG_GRACEFUL,
        0);
    return QUIC_FAILED(status) ? MAGNETK_ERR_STREAM : MAGNETK_OK;
}

/* Tear down stream OS resources but do NOT free the struct itself. */
static void magnetk_stream_free_resources(magnetk_stream_t* stream) {
    if (!stream) return;
#ifdef _WIN32
    if (stream->recv_event) { CloseHandle(stream->recv_event); stream->recv_event = NULL; }
    DeleteCriticalSection(&stream->recv_lock);
    if (stream->send_event) { CloseHandle(stream->send_event); stream->send_event = NULL; }
#else
    pthread_mutex_destroy(&stream->recv_mutex);
    pthread_cond_destroy(&stream->recv_cond);
    pthread_mutex_destroy(&stream->send_mutex);
    pthread_cond_destroy(&stream->send_cond);
#endif
}

/* Drop one reference; free the struct when the last owner releases. */
static void magnetk_stream_release(magnetk_stream_t* stream) {
    if (!stream) return;
    if (MAGNETK_ATOMIC_DEC(&stream->ref_count) == 0) {
        magnetk_stream_free_resources(stream);
        free(stream);
    }
}

MAGNETK_API void magnetk_stream_close(magnetk_stream_t* stream) {
    if (!stream) return;
    /* Atomically take ownership of the handle so a concurrent shutdown
     * callback can't double-shutdown. Use the cached API table so we
     * never deref stream->conn — which may already be freed. */
    HQUIC handle = MAGNETK_ATOMIC_XCHG_PTR(&stream->handle, NULL);
    const QUIC_API_TABLE* api = stream->msquic_api;
    if (handle && api) {
        api->StreamShutdown(handle, QUIC_STREAM_SHUTDOWN_FLAG_ABORT, 0);
    }
    /* Drop Go's reference. If MsQuic callback already finished its
     * release, this frees the struct; otherwise the callback frees later. */
    magnetk_stream_release(stream);
}

MAGNETK_API void magnetk_stream_set_userdata(magnetk_stream_t* stream, void* user_data) {
    if (stream) stream->user_data = user_data;
}

MAGNETK_API void* magnetk_stream_get_userdata(magnetk_stream_t* stream) {
    return stream ? stream->user_data : NULL;
}

/* ===== Synchronous Helpers ===== */

MAGNETK_API magnetk_status_t magnetk_stream_send_sync(
    magnetk_stream_t* stream,
    const uint8_t*   data,
    uint32_t         length,
    int              fin)
{
    if (!stream || !data) return MAGNETK_ERR_INVALID_PARAM;

    stream->send_complete = 0;

    magnetk_status_t result = magnetk_stream_send(stream, data, length, fin);
    if (result != MAGNETK_OK) return result;

    /* Wait for send complete */
#ifdef _WIN32
    WaitForSingleObject(stream->send_event, 30000);
#else
    struct timespec ts;
    clock_gettime(CLOCK_REALTIME, &ts);
    ts.tv_sec += 30;
    pthread_mutex_lock(&stream->send_mutex);
    while (!stream->send_complete) {
        if (pthread_cond_timedwait(&stream->send_cond, &stream->send_mutex, &ts) != 0) break;
    }
    pthread_mutex_unlock(&stream->send_mutex);
#endif

    return stream->send_complete ? MAGNETK_OK : MAGNETK_ERR_TIMEOUT;
}

MAGNETK_API magnetk_status_t magnetk_stream_recv_sync(
    magnetk_stream_t* stream,
    uint8_t*         buffer,
    uint32_t         buffer_size,
    uint32_t*        out_length,
    int*             out_fin,
    uint32_t         timeout_ms)
{
    if (!stream || !buffer || !out_length) return MAGNETK_ERR_INVALID_PARAM;

    stream->recv_buffer = buffer;
    stream->recv_length = buffer_size;
    stream->recv_fin = 0;
    stream->recv_pending = 1;

#ifdef _WIN32
    DWORD wait_result = WaitForSingleObject(stream->recv_event, timeout_ms);
    if (wait_result == WAIT_TIMEOUT) {
        stream->recv_pending = 0;
        return MAGNETK_ERR_TIMEOUT;
    }
#else
    struct timespec ts;
    clock_gettime(CLOCK_REALTIME, &ts);
    ts.tv_sec += timeout_ms / 1000;
    ts.tv_nsec += (timeout_ms % 1000) * 1000000;
    if (ts.tv_nsec >= 1000000000) { ts.tv_sec++; ts.tv_nsec -= 1000000000; }

    pthread_mutex_lock(&stream->recv_mutex);
    while (stream->recv_pending) {
        if (pthread_cond_timedwait(&stream->recv_cond, &stream->recv_mutex, &ts) != 0) {
            stream->recv_pending = 0;
            pthread_mutex_unlock(&stream->recv_mutex);
            return MAGNETK_ERR_TIMEOUT;
        }
    }
    pthread_mutex_unlock(&stream->recv_mutex);
#endif

    *out_length = stream->recv_length;
    if (out_fin) *out_fin = stream->recv_fin;

    stream->recv_buffer = NULL;

    if (stream->recv_length == 0 && stream->recv_fin) {
        return MAGNETK_ERR_CLOSED;
    }

    return MAGNETK_OK;
}
