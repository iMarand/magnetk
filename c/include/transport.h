/*
 * Magnet P2P Transport Layer - MsQuic Wrapper
 *
 * Provides a simplified C API over MsQuic for high-performance QUIC transport.
 * Used by Go (via CGo) and C++ SDK directly.
 *
 * Platform: Windows (schannel), Linux (openssl), macOS (openssl)
 */

#ifndef MAGNET_TRANSPORT_H
#define MAGNET_TRANSPORT_H

#include <stdint.h>
#include <stddef.h>

#ifdef _WIN32
    #ifdef MAGNET_TRANSPORT_EXPORTS
        #define MAGNET_API __declspec(dllexport)
    #else
        #define MAGNET_API __declspec(dllimport)
    #endif
#else
    #define MAGNET_API __attribute__((visibility("default")))
#endif

#ifdef __cplusplus
extern "C" {
#endif

/* ===== Status Codes ===== */
typedef int32_t magnet_status_t;

#define MAGNET_OK                    0
#define MAGNET_ERR_INIT             -1
#define MAGNET_ERR_CONFIG           -2
#define MAGNET_ERR_LISTEN           -3
#define MAGNET_ERR_CONNECT          -4
#define MAGNET_ERR_SEND             -5
#define MAGNET_ERR_RECV             -6
#define MAGNET_ERR_TIMEOUT          -7
#define MAGNET_ERR_CLOSED           -8
#define MAGNET_ERR_TLS              -9
#define MAGNET_ERR_MEMORY           -10
#define MAGNET_ERR_INVALID_PARAM    -11
#define MAGNET_ERR_STREAM           -12
#define MAGNET_ERR_NOT_READY        -13

/* ===== Opaque Handle Types ===== */
typedef struct magnet_transport   magnet_transport_t;
typedef struct magnet_listener    magnet_listener_t;
typedef struct magnet_connection  magnet_connection_t;
typedef struct magnet_stream      magnet_stream_t;

/* ===== Configuration ===== */
typedef struct magnet_config {
    /* Network */
    const char* bind_address;     /* e.g. "0.0.0.0" or "127.0.0.1" */
    uint16_t    port;             /* e.g. 4002 */

    /* TLS (server only) */
    const char* cert_file;        /* Path to PEM cert, NULL = self-signed */
    const char* key_file;         /* Path to PEM key, NULL = self-signed */
    int         no_cert_validation; /* 1 = skip cert validation (dev mode) */

    /* ALPN */
    const char* alpn;             /* e.g. "magnet" */

    /* Performance */
    uint32_t    max_connections;        /* Max concurrent connections */
    uint32_t    idle_timeout_ms;        /* Connection idle timeout */
    uint32_t    send_buffer_size;       /* Send buffer in bytes */
    uint32_t    recv_buffer_size;       /* Recv buffer in bytes */
    uint16_t    max_streams_per_conn;   /* Max bidi streams per connection */

    /* Execution profile: 0=low_latency, 1=max_throughput */
    int         execution_profile;
} magnet_config_t;

/* ===== Callbacks ===== */

/* Called when a new connection is accepted by a listener */
typedef void (*magnet_on_connection_fn)(
    magnet_listener_t*    listener,
    magnet_connection_t*  conn,
    void*                 user_data
);

/* Called when a new stream is opened on a connection */
typedef void (*magnet_on_stream_fn)(
    magnet_connection_t*  conn,
    magnet_stream_t*      stream,
    void*                 user_data
);

/* Called when data is received on a stream */
typedef void (*magnet_on_data_fn)(
    magnet_stream_t*      stream,
    const uint8_t*        data,
    uint32_t              length,
    int                   fin,      /* 1 if this is the last data */
    void*                 user_data
);

/* Called when a stream or connection is closed */
typedef void (*magnet_on_close_fn)(
    void*                 handle,
    magnet_status_t       status,
    void*                 user_data
);

/* Called when a send completes */
typedef void (*magnet_on_send_complete_fn)(
    magnet_stream_t*      stream,
    void*                 send_context,
    int                   canceled,
    void*                 user_data
);

/* ===== Transport Lifecycle ===== */

/* Initialize the transport layer. Must be called once before any other calls. */
MAGNET_API magnet_status_t magnet_transport_init(
    magnet_config_t*      config,
    magnet_transport_t**  out_transport
);

/* Shut down the transport layer and free all resources. */
MAGNET_API void magnet_transport_shutdown(
    magnet_transport_t*   transport
);

/* Get default config with sane defaults. */
MAGNET_API void magnet_config_default(
    magnet_config_t*      config
);

/* ===== Listener (Server Side) ===== */

/* Start listening for incoming connections. */
MAGNET_API magnet_status_t magnet_listen(
    magnet_transport_t*       transport,
    magnet_on_connection_fn   on_connection,
    magnet_on_stream_fn       on_stream,
    magnet_on_data_fn         on_data,
    magnet_on_close_fn        on_close,
    void*                     user_data,
    magnet_listener_t**       out_listener
);

/* Stop listening and close the listener. */
MAGNET_API void magnet_listener_close(
    magnet_listener_t*    listener
);

/* ===== Connection (Client Side) ===== */

/* Connect to a remote server. Blocking until connected or timeout. */
MAGNET_API magnet_status_t magnet_connect(
    magnet_transport_t*       transport,
    const char*               host,
    uint16_t                  port,
    magnet_on_stream_fn       on_stream,
    magnet_on_data_fn         on_data,
    magnet_on_close_fn        on_close,
    void*                     user_data,
    magnet_connection_t**     out_conn
);

/* Close a connection. */
MAGNET_API void magnet_connection_close(
    magnet_connection_t*  conn
);

/* Set user data on a connection. */
MAGNET_API void magnet_connection_set_userdata(
    magnet_connection_t*  conn,
    void*                 user_data
);

/* Get user data from a connection. */
MAGNET_API void* magnet_connection_get_userdata(
    magnet_connection_t*  conn
);

/* ===== Streams ===== */

/* Open a new bidirectional stream on a connection. */
MAGNET_API magnet_status_t magnet_stream_open(
    magnet_connection_t*      conn,
    magnet_on_data_fn         on_data,
    magnet_on_send_complete_fn on_send_complete,
    magnet_on_close_fn        on_close,
    void*                     user_data,
    magnet_stream_t**         out_stream
);

/* Send data on a stream. The data is copied internally. */
MAGNET_API magnet_status_t magnet_stream_send(
    magnet_stream_t*      stream,
    const uint8_t*        data,
    uint32_t              length,
    int                   fin     /* 1 = last send, close send direction */
);

/* Send data on a stream with zero-copy. Caller must keep buffer alive until
   on_send_complete callback fires. send_context is passed to callback. */
MAGNET_API magnet_status_t magnet_stream_send_zerocopy(
    magnet_stream_t*      stream,
    const uint8_t*        data,
    uint32_t              length,
    int                   fin,
    void*                 send_context
);

/* Gracefully shut down the send direction of a stream. */
MAGNET_API magnet_status_t magnet_stream_shutdown_send(
    magnet_stream_t*      stream
);

/* Close a stream (both directions). */
MAGNET_API void magnet_stream_close(
    magnet_stream_t*      stream
);

/* Set user data on a stream. */
MAGNET_API void magnet_stream_set_userdata(
    magnet_stream_t*      stream,
    void*                 user_data
);

/* Get user data from a stream. */
MAGNET_API void* magnet_stream_get_userdata(
    magnet_stream_t*      stream
);

/* ===== Synchronous Helpers (for CGo) ===== */

/* Blocking send: sends data and waits for completion. */
MAGNET_API magnet_status_t magnet_stream_send_sync(
    magnet_stream_t*      stream,
    const uint8_t*        data,
    uint32_t              length,
    int                   fin
);

/* Blocking recv: waits for data and copies it into buffer.
   Returns actual bytes received in out_length. */
MAGNET_API magnet_status_t magnet_stream_recv_sync(
    magnet_stream_t*      stream,
    uint8_t*              buffer,
    uint32_t              buffer_size,
    uint32_t*             out_length,
    int*                  out_fin,
    uint32_t              timeout_ms
);

/* ===== Utility ===== */

/* Returns a human-readable error string for a status code. */
MAGNET_API const char* magnet_status_string(magnet_status_t status);

#ifdef __cplusplus
}
#endif

#endif /* MAGNET_TRANSPORT_H */
