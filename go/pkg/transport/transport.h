/*
 * Magnetk P2P Transport Layer - MsQuic Wrapper
 *
 * Provides a simplified C API over MsQuic for high-performance QUIC transport.
 * Used by Go (via CGo) and C++ SDK directly.
 *
 * Platform: Windows (schannel), Linux (openssl), macOS (openssl)
 */

#ifndef MAGNETK_TRANSPORT_H
#define MAGNETK_TRANSPORT_H

#include <stdint.h>
#include <stddef.h>

#ifdef _WIN32
    #ifdef MAGNETK_STATIC
        #define MAGNETK_API
    #elif defined(MAGNETK_TRANSPORT_EXPORTS)
        #define MAGNETK_API __declspec(dllexport)
    #else
        #define MAGNETK_API __declspec(dllimport)
    #endif
#else
    #define MAGNETK_API __attribute__((visibility("default")))
#endif

#ifdef __cplusplus
extern "C" {
#endif

/* ===== Status Codes ===== */
typedef int32_t magnetk_status_t;

#define MAGNETK_OK                    0
#define MAGNETK_ERR_INIT             -1
#define MAGNETK_ERR_CONFIG           -2
#define MAGNETK_ERR_LISTEN           -3
#define MAGNETK_ERR_CONNECT          -4
#define MAGNETK_ERR_SEND             -5
#define MAGNETK_ERR_RECV             -6
#define MAGNETK_ERR_TIMEOUT          -7
#define MAGNETK_ERR_CLOSED           -8
#define MAGNETK_ERR_TLS              -9
#define MAGNETK_ERR_MEMORY           -10
#define MAGNETK_ERR_INVALID_PARAM    -11
#define MAGNETK_ERR_STREAM           -12
#define MAGNETK_ERR_NOT_READY        -13

/* ===== Opaque Handle Types ===== */
typedef struct magnetk_transport   magnetk_transport_t;
typedef struct magnetk_listener    magnetk_listener_t;
typedef struct magnetk_connection  magnetk_connection_t;
typedef struct magnetk_stream      magnetk_stream_t;

/* ===== Configuration ===== */
typedef struct magnetk_config {
    /* Network */
    const char* bind_address;     /* e.g. "0.0.0.0" or "127.0.0.1" */
    uint16_t    port;             /* e.g. 4002 */

    /* TLS (server only) */
    const char* cert_file;        /* Path to PEM cert, NULL = self-signed */
    const char* key_file;         /* Path to PEM key, NULL = self-signed */
    int         no_cert_validation; /* 1 = skip cert validation (dev mode) */

    /* ALPN */
    const char* alpn;             /* e.g. "magnetk" */

    /* Performance */
    uint32_t    max_connections;        /* Max concurrent connections */
    uint32_t    idle_timeout_ms;        /* Connection idle timeout */
    uint32_t    send_buffer_size;       /* Send buffer in bytes */
    uint32_t    recv_buffer_size;       /* Recv buffer in bytes */
    uint16_t    max_streams_per_conn;   /* Max bidi streams per connection */

    /* Execution profile: 0=low_latency, 1=max_throughput */
    int         execution_profile;
} magnetk_config_t;

/* ===== Callbacks ===== */

/* Called when a new connection is accepted by a listener */
typedef void (*magnetk_on_connection_fn)(
    magnetk_listener_t*    listener,
    magnetk_connection_t*  conn,
    void*                 user_data
);

/* Called when a new stream is opened on a connection */
typedef void (*magnetk_on_stream_fn)(
    magnetk_connection_t*  conn,
    magnetk_stream_t*      stream,
    void*                 user_data
);

/* Called when data is received on a stream */
typedef void (*magnetk_on_data_fn)(
    magnetk_stream_t*      stream,
    const uint8_t*        data,
    uint32_t              length,
    int                   fin,      /* 1 if this is the last data */
    void*                 user_data
);

/* Called when a stream or connection is closed */
typedef void (*magnetk_on_close_fn)(
    void*                 handle,
    magnetk_status_t       status,
    void*                 user_data
);

/* Called when a send completes */
typedef void (*magnetk_on_send_complete_fn)(
    magnetk_stream_t*      stream,
    void*                 send_context,
    int                   canceled,
    void*                 user_data
);

/* ===== Transport Lifecycle ===== */

/* Initialize the transport layer. Must be called once before any other calls. */
MAGNETK_API magnetk_status_t magnetk_transport_init(
    magnetk_config_t*      config,
    magnetk_transport_t**  out_transport
);

/* Shut down the transport layer and free all resources. */
MAGNETK_API void magnetk_transport_shutdown(
    magnetk_transport_t*   transport
);

/* Get default config with sane defaults. */
MAGNETK_API void magnetk_config_default(
    magnetk_config_t*      config
);

/* ===== Listener (Server Side) ===== */

/* Start listening for incoming connections. */
MAGNETK_API magnetk_status_t magnetk_listen(
    magnetk_transport_t*       transport,
    magnetk_on_connection_fn   on_connection,
    magnetk_on_stream_fn       on_stream,
    magnetk_on_data_fn         on_data,
    magnetk_on_close_fn        on_close,
    void*                     user_data,
    magnetk_listener_t**       out_listener
);

/* Stop listening and close the listener. */
MAGNETK_API void magnetk_listener_close(
    magnetk_listener_t*    listener
);

/* ===== Connection (Client Side) ===== */

/* Connect to a remote server. Blocking until connected or timeout. */
MAGNETK_API magnetk_status_t magnetk_connect(
    magnetk_transport_t*       transport,
    const char*               host,
    uint16_t                  port,
    magnetk_on_stream_fn       on_stream,
    magnetk_on_data_fn         on_data,
    magnetk_on_close_fn        on_close,
    void*                     user_data,
    magnetk_connection_t**     out_conn
);

/* Close a connection. */
MAGNETK_API void magnetk_connection_close(
    magnetk_connection_t*  conn
);

/* Set user data on a connection. */
MAGNETK_API void magnetk_connection_set_userdata(
    magnetk_connection_t*  conn,
    void*                 user_data
);

/* Get user data from a connection. */
MAGNETK_API void* magnetk_connection_get_userdata(
    magnetk_connection_t*  conn
);

/* ===== Streams ===== */

/* Open a new bidirectional stream on a connection. */
MAGNETK_API magnetk_status_t magnetk_stream_open(
    magnetk_connection_t*      conn,
    magnetk_on_data_fn         on_data,
    magnetk_on_send_complete_fn on_send_complete,
    magnetk_on_close_fn        on_close,
    void*                     user_data,
    magnetk_stream_t**         out_stream
);

/* Send data on a stream. The data is copied internally. */
MAGNETK_API magnetk_status_t magnetk_stream_send(
    magnetk_stream_t*      stream,
    const uint8_t*        data,
    uint32_t              length,
    int                   fin     /* 1 = last send, close send direction */
);

/* Send data on a stream with zero-copy. Caller must keep buffer alive until
   on_send_complete callback fires. send_context is passed to callback. */
MAGNETK_API magnetk_status_t magnetk_stream_send_zerocopy(
    magnetk_stream_t*      stream,
    const uint8_t*        data,
    uint32_t              length,
    int                   fin,
    void*                 send_context
);

/* Gracefully shut down the send direction of a stream. */
MAGNETK_API magnetk_status_t magnetk_stream_shutdown_send(
    magnetk_stream_t*      stream
);

/* Close a stream (both directions). */
MAGNETK_API void magnetk_stream_close(
    magnetk_stream_t*      stream
);

/* Set user data on a stream. */
MAGNETK_API void magnetk_stream_set_userdata(
    magnetk_stream_t*      stream,
    void*                 user_data
);

/* Get user data from a stream. */
MAGNETK_API void* magnetk_stream_get_userdata(
    magnetk_stream_t*      stream
);

/* ===== Synchronous Helpers (for CGo) ===== */

/* Blocking send: sends data and waits for completion. */
MAGNETK_API magnetk_status_t magnetk_stream_send_sync(
    magnetk_stream_t*      stream,
    const uint8_t*        data,
    uint32_t              length,
    int                   fin
);

/* Blocking recv: waits for data and copies it into buffer.
   Returns actual bytes received in out_length. */
MAGNETK_API magnetk_status_t magnetk_stream_recv_sync(
    magnetk_stream_t*      stream,
    uint8_t*              buffer,
    uint32_t              buffer_size,
    uint32_t*             out_length,
    int*                  out_fin,
    uint32_t              timeout_ms
);

/* ===== Utility ===== */

/* Returns a human-readable error string for a status code. */
MAGNETK_API const char* magnetk_status_string(magnetk_status_t status);

#ifdef __cplusplus
}
#endif

#endif /* MAGNETK_TRANSPORT_H */
