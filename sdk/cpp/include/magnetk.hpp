// Magnetk C++ SDK (header-only, C++17).
//
// Fast messaging, pipes and file distribution over QUIC, on top of the
// Magnetk C API (libmagnetk). The library is loaded at runtime, so any
// compiler works (MSVC, MinGW, GCC, Clang) and no import library is needed.
// It is searched in: $MAGNETK_LIB (file or directory), the executable's
// directory, the current directory, then the system loader path.
//
//   #include <magnetk.hpp>
//   magnetk::Node node({.relays = {"relay.example.com:4002"}, .id = "alice"});
//   node.send("bob", "hello");
//   node.subscribe("chat.*");
//   while (auto m = node.receive(1000)) std::cout << m->from << ": " << m->data << "\n";
//
// Errors are reported as exceptions derived from magnetk::Error.
#ifndef MAGNETK_HPP
#define MAGNETK_HPP

#include <atomic>
#include <chrono>
#include <cstdint>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <functional>
#include <memory>
#include <mutex>
#include <optional>
#include <stdexcept>
#include <string>
#include <string_view>
#include <thread>
#include <utility>
#include <vector>

#ifdef _WIN32
#ifndef WIN32_LEAN_AND_MEAN
#define WIN32_LEAN_AND_MEAN
#endif
#ifndef NOMINMAX
#define NOMINMAX
#endif
#include <windows.h>
#else
#include <dlfcn.h>
#include <limits.h>
#include <unistd.h>
#endif

namespace magnetk {

// ======================================================================
// Errors
// ======================================================================

enum Code : int {
    OK = 0, ERR = -1, ERR_TIMEOUT = -2, ERR_CLOSED = -3, ERR_NO_ROUTE = -4,
    ERR_UNAUTHORIZED = -5, ERR_NOT_CONNECTED = -6, ERR_REMOTE = -7, ERR_REJECTED = -8,
    ERR_NO_PROVIDERS = -9, ERR_INVALID = -10, ERR_DROPPED = -11,
};

class Error : public std::runtime_error {
public:
    Error(int code, const std::string& msg) : std::runtime_error(msg), code_(code) {}
    int code() const noexcept { return code_; }
private:
    int code_;
};
#define MAGNETK_ERROR_CLASS(Name, C) \
    class Name : public Error { public: explicit Name(const std::string& m) : Error(C, m) {} };
MAGNETK_ERROR_CLASS(TimeoutError, ERR_TIMEOUT)
MAGNETK_ERROR_CLASS(ClosedError, ERR_CLOSED)
MAGNETK_ERROR_CLASS(NoRouteError, ERR_NO_ROUTE)
MAGNETK_ERROR_CLASS(UnauthorizedError, ERR_UNAUTHORIZED)
MAGNETK_ERROR_CLASS(NotConnectedError, ERR_NOT_CONNECTED)
MAGNETK_ERROR_CLASS(RemoteError, ERR_REMOTE)
MAGNETK_ERROR_CLASS(RejectedError, ERR_REJECTED)
MAGNETK_ERROR_CLASS(NoProvidersError, ERR_NO_PROVIDERS)
MAGNETK_ERROR_CLASS(InvalidError, ERR_INVALID)
MAGNETK_ERROR_CLASS(DroppedError, ERR_DROPPED)
#undef MAGNETK_ERROR_CLASS

// ======================================================================
// Runtime binding of libmagnetk
// ======================================================================

namespace detail {

struct RawMessage {
    int32_t kind; uint64_t id; char* from; char* subject; void* data;
    int32_t len; int32_t datagram; uint64_t request;
};

struct Api {
    const char* (*version)();
    int (*quic_available)();
    void (*free_)(void*);
    int (*node_open)(const char*, uint64_t*, char**);
    int (*node_close)(uint64_t);
    char* (*node_id)(uint64_t);
    char* (*node_info)(uint64_t);
    int (*node_wait_connected)(uint64_t, int, char**);
    int (*send)(uint64_t, const char*, const void*, int, int, int, char**);
    int (*publish)(uint64_t, const char*, const void*, int, int, char**);
    int (*subscribe)(uint64_t, const char*, const char*, char**);
    int (*unsubscribe)(uint64_t, const char*, char**);
    int (*serve)(uint64_t, const char*, char**);
    int (*request)(uint64_t, const char*, const char*, const void*, int, int, void**, int*, char**);
    int (*recv)(uint64_t, int, RawMessage**, char**);
    void (*message_free)(RawMessage*);
    int (*respond)(uint64_t, uint64_t, const void*, int, int, char**);
    int (*watch_presence)(uint64_t, int, char**);
    int (*ping)(uint64_t, int, int64_t*, char**);
    char* (*list_nodes)(uint64_t, const char*, char**);
    char* (*relay_stats)(uint64_t, char**);
    int (*pipe_open)(uint64_t, const char*, const char*, const char*, const char*, int, uint64_t*, char**);
    int (*pipe_accept)(uint64_t, int, uint64_t*, char**);
    char* (*pipe_info)(uint64_t);
    int (*pipe_read)(uint64_t, void*, int, int, char**);
    int (*pipe_write)(uint64_t, const void*, int, int, char**);
    int (*pipe_close_write)(uint64_t);
    int (*pipe_close)(uint64_t);
    int (*share)(uint64_t, const char*, int64_t, char**, char**);
    int (*unshare)(uint64_t, const char*, char**);
    char* (*find_providers)(uint64_t, const char*, char**);
    int (*download_start)(uint64_t, const char*, const char*, const char*, uint64_t*, char**);
    int (*download_poll)(uint64_t, int, char**, char**);
    int (*download_cancel)(uint64_t);
    void (*download_free)(uint64_t);
    int (*relay_start)(const char*, uint64_t*, char**);
    int (*relay_port)(uint64_t);
    char* (*relay_stats_json)(uint64_t);
    int (*relay_close)(uint64_t);

    static Api& get() {
        static Api api = load();
        return api;
    }

private:
#ifdef _WIN32
    using Handle = HMODULE;
    static Handle open_lib(const std::string& p) { return LoadLibraryExA(p.c_str(), nullptr, LOAD_WITH_ALTERED_SEARCH_PATH); }
    static void* sym(Handle h, const char* n) { return reinterpret_cast<void*>(GetProcAddress(h, n)); }
    static std::string exe_dir() {
        char buf[MAX_PATH]; DWORD n = GetModuleFileNameA(nullptr, buf, MAX_PATH);
        std::string s(buf, n); auto p = s.find_last_of("\\/"); return p == std::string::npos ? "." : s.substr(0, p);
    }
    static constexpr const char* kName = "libmagnetk.dll";
    static constexpr char kSep = '\\';
#else
    using Handle = void*;
    static Handle open_lib(const std::string& p) { return dlopen(p.c_str(), RTLD_NOW | RTLD_LOCAL); }
    static void* sym(Handle h, const char* n) { return dlsym(h, n); }
    static std::string exe_dir() {
        char buf[PATH_MAX]; ssize_t n = readlink("/proc/self/exe", buf, sizeof(buf) - 1);
        if (n <= 0) return ".";
        std::string s(buf, static_cast<size_t>(n)); auto p = s.find_last_of('/'); return p == std::string::npos ? "." : s.substr(0, p);
    }
#ifdef __APPLE__
    static constexpr const char* kName = "libmagnetk.dylib";
#else
    static constexpr const char* kName = "libmagnetk.so";
#endif
    static constexpr char kSep = '/';
#endif

    static Api load() {
        std::vector<std::string> tries;
        if (const char* env = std::getenv("MAGNETK_LIB")) {
            std::string e(env);
            tries.push_back(e);
            tries.push_back(e + kSep + kName);
        }
        tries.push_back(exe_dir() + kSep + kName);
        tries.push_back(std::string(".") + kSep + kName);
        tries.push_back(kName);
        Handle h = nullptr;
        for (const auto& t : tries) {
            if ((h = open_lib(t))) break;
        }
        if (!h) {
            std::string msg = "libmagnetk not found (set MAGNETK_LIB). Tried:";
            for (const auto& t : tries) msg += " " + t;
            throw Error(ERR, msg);
        }
        Api a{};
        auto bind = [&](auto& fp, const char* name) {
            void* p = sym(h, name);
            if (!p) throw Error(ERR, std::string("libmagnetk is missing symbol ") + name);
            fp = reinterpret_cast<std::remove_reference_t<decltype(fp)>>(p);
        };
        bind(a.version, "mk_version"); bind(a.quic_available, "mk_quic_available"); bind(a.free_, "mk_free");
        bind(a.node_open, "mk_node_open"); bind(a.node_close, "mk_node_close"); bind(a.node_id, "mk_node_id");
        bind(a.node_info, "mk_node_info"); bind(a.node_wait_connected, "mk_node_wait_connected");
        bind(a.send, "mk_send"); bind(a.publish, "mk_publish"); bind(a.subscribe, "mk_subscribe");
        bind(a.unsubscribe, "mk_unsubscribe"); bind(a.serve, "mk_serve"); bind(a.request, "mk_request");
        bind(a.recv, "mk_recv"); bind(a.message_free, "mk_message_free"); bind(a.respond, "mk_respond");
        bind(a.watch_presence, "mk_watch_presence"); bind(a.ping, "mk_ping"); bind(a.list_nodes, "mk_list_nodes");
        bind(a.relay_stats, "mk_relay_stats"); bind(a.pipe_open, "mk_pipe_open"); bind(a.pipe_accept, "mk_pipe_accept");
        bind(a.pipe_info, "mk_pipe_info"); bind(a.pipe_read, "mk_pipe_read"); bind(a.pipe_write, "mk_pipe_write");
        bind(a.pipe_close_write, "mk_pipe_close_write"); bind(a.pipe_close, "mk_pipe_close");
        bind(a.share, "mk_share"); bind(a.unshare, "mk_unshare"); bind(a.find_providers, "mk_find_providers");
        bind(a.download_start, "mk_download_start"); bind(a.download_poll, "mk_download_poll");
        bind(a.download_cancel, "mk_download_cancel"); bind(a.download_free, "mk_download_free");
        bind(a.relay_start, "mk_relay_start"); bind(a.relay_port, "mk_relay_port");
        bind(a.relay_stats_json, "mk_relay_stats_json"); bind(a.relay_close, "mk_relay_close");
        return a;
    }
};

inline std::string take(char* s) {
    if (!s) return {};
    std::string out(s);
    Api::get().free_(s);
    return out;
}

[[noreturn]] inline void throw_code(int rc, const std::string& msg) {
    switch (rc) {
        case ERR_TIMEOUT: throw TimeoutError(msg);
        case ERR_CLOSED: throw ClosedError(msg);
        case ERR_NO_ROUTE: throw NoRouteError(msg);
        case ERR_UNAUTHORIZED: throw UnauthorizedError(msg);
        case ERR_NOT_CONNECTED: throw NotConnectedError(msg);
        case ERR_REMOTE: throw RemoteError(msg);
        case ERR_REJECTED: throw RejectedError(msg);
        case ERR_NO_PROVIDERS: throw NoProvidersError(msg);
        case ERR_INVALID: throw InvalidError(msg);
        case ERR_DROPPED: throw DroppedError(msg);
        default: throw Error(rc, msg);
    }
}

// Takes char** (not char*): C++ evaluates arguments in unspecified order, so
// check(api().fn(..., &err), err) could read err before fn filled it in.
inline int check(int rc, char** errp) {
    char* err = errp ? *errp : nullptr;
    if (errp) *errp = nullptr;
    if (rc < 0) throw_code(rc, err ? take(err) : "magnetk error " + std::to_string(rc));
    if (err) Api::get().free_(err);
    return rc;
}

// ---- minimal JSON helpers (flat objects produced by libmagnetk) ----
inline std::string json_escape(std::string_view s) {
    std::string o;
    o.reserve(s.size() + 2);
    o += '"';
    for (char c : s) {
        switch (c) {
            case '"': o += "\\\""; break;
            case '\\': o += "\\\\"; break;
            case '\n': o += "\\n"; break;
            case '\r': o += "\\r"; break;
            case '\t': o += "\\t"; break;
            default:
                if (static_cast<unsigned char>(c) < 0x20) {
                    char b[8]; std::snprintf(b, sizeof b, "\\u%04x", c); o += b;
                } else o += c;
        }
    }
    o += '"';
    return o;
}

inline std::string json_array(const std::vector<std::string>& v) {
    std::string o = "[";
    for (size_t i = 0; i < v.size(); i++) { if (i) o += ','; o += json_escape(v[i]); }
    return o + "]";
}

inline size_t json_find(const std::string& js, std::string_view key) {
    std::string pat = "\"" + std::string(key) + "\":";
    auto p = js.find(pat);
    return p == std::string::npos ? p : p + pat.size();
}

inline std::string json_str(const std::string& js, std::string_view key) {
    auto p = json_find(js, key);
    if (p == std::string::npos || p >= js.size() || js[p] != '"') return {};
    std::string o;
    for (size_t i = p + 1; i < js.size(); i++) {
        char c = js[i];
        if (c == '"') break;
        if (c == '\\' && i + 1 < js.size()) {
            char n = js[++i];
            switch (n) {
                case 'n': o += '\n'; break;
                case 't': o += '\t'; break;
                case 'r': o += '\r'; break;
                case 'u': if (i + 4 < js.size()) { o += static_cast<char>(std::strtol(js.substr(i + 1, 4).c_str(), nullptr, 16)); i += 4; } break;
                default: o += n;
            }
        } else o += c;
    }
    return o;
}

inline double json_num(const std::string& js, std::string_view key) {
    auto p = json_find(js, key);
    if (p == std::string::npos) return 0;
    return std::strtod(js.c_str() + p, nullptr);
}

inline bool json_bool(const std::string& js, std::string_view key) {
    auto p = json_find(js, key);
    return p != std::string::npos && js.compare(p, 4, "true") == 0;
}

} // namespace detail

/// Library version ("2.0.0").
inline std::string version() { return detail::Api::get().version(); }
/// True when QUIC (MsQuic) is available; otherwise TCP+TLS is used.
inline bool quic_available() { return detail::Api::get().quic_available() != 0; }

// ======================================================================
// Types
// ======================================================================

enum class Kind { Direct = 1, Publish = 2, Request = 3, Presence = 4 };

/// Send flags (combine with |).
enum SendFlags : int { Receipt = 1, Store = 2, Unreliable = 4 };

struct Message {
    Kind kind = Kind::Direct;
    uint64_t id = 0;
    std::string from;      ///< sender node id
    std::string subject;   ///< topic, or "join"/"leave" for presence
    std::string data;      ///< payload (binary-safe)
    bool datagram = false;
    uint64_t request = 0;  ///< non-zero for requests (answer with Node::respond)

    bool is_request() const { return kind == Kind::Request; }
};

struct SharedFile {
    std::string hash, name, root, path, link;
    int64_t size = 0, chunk_size = 0;
};

struct Progress {
    std::string name;
    int64_t size = 0, done = 0;
    double percent = 0, bytes_per_sec = 0, eta_seconds = 0;
    int sources = 0, streams = 0;
};

struct DownloadResult {
    std::string path, hash;
    int64_t size = 0, bytes = 0, resumed = 0;
    double seconds = 0;
};

/// Node options (see magnetk.h for every key).
struct NodeOptions {
    std::vector<std::string> relays;
    std::string id;
    std::string token;
    std::string meta;
    std::string transport = "auto";   ///< auto | quic | tcp
    std::string route = "auto";       ///< auto | direct | relay
    bool listen = false;               ///< accept peer-to-peer connections
    std::string listen_addr;           ///< e.g. "0.0.0.0:4012"
    std::vector<std::string> advertise;
    bool upnp = false;
    bool watch_presence = false;
    bool verify_peer = false;
    int request_timeout_ms = 0;
    int connect_timeout_ms = 0;
    int inbox_size = 0;                ///< buffered direct messages (default 8192)
    int sub_buffer = 0;                ///< per-subscription buffer (default 1024)
    int slow_consumer_wait_ms = 0;     ///< wait before dropping when full (default 250)
    int64_t chunk_size = 0;
    int streams_per_source = 0;
    std::string cache_dir;             ///< "-" disables the manifest cache
    std::vector<std::string> link_relays;
    std::string congestion;            ///< cubic (default) | bbr
    bool log = false;

    std::string to_json() const {
        using detail::json_escape;
        std::string j = "{\"relays\":" + detail::json_array(relays);
        auto s = [&](const char* k, const std::string& v) { if (!v.empty()) j += std::string(",\"") + k + "\":" + json_escape(v); };
        auto i = [&](const char* k, long long v) { if (v) j += std::string(",\"") + k + "\":" + std::to_string(v); };
        auto b = [&](const char* k, bool v) { if (v) j += std::string(",\"") + k + "\":true"; };
        s("id", id); s("token", token); s("meta", meta); s("transport", transport); s("route", route);
        b("listen", listen); s("listen_addr", listen_addr); b("upnp", upnp); b("watch_presence", watch_presence);
        b("verify_peer", verify_peer); i("request_timeout_ms", request_timeout_ms); i("connect_timeout_ms", connect_timeout_ms);
        i("chunk_size", chunk_size); i("streams_per_source", streams_per_source); s("cache_dir", cache_dir);
        i("inbox_size", inbox_size); i("sub_buffer", sub_buffer); i("slow_consumer_wait_ms", slow_consumer_wait_ms);
        s("congestion", congestion); b("log", log);
        if (!advertise.empty()) j += ",\"advertise\":" + detail::json_array(advertise);
        if (!link_relays.empty()) j += ",\"link_relays\":" + detail::json_array(link_relays);
        return j + "}";
    }
};

// ======================================================================
// Pipe
// ======================================================================

/// A reliable byte stream to another node. Move-only; closes on destruction.
class Pipe {
public:
    Pipe() = default;
    explicit Pipe(uint64_t h) : h_(h) {
        std::string info = detail::take(detail::Api::get().pipe_info(h));
        peer = detail::json_str(info, "peer");
        service = detail::json_str(info, "service");
        meta = detail::json_str(info, "meta");
        direct = detail::json_bool(info, "direct");
    }
    Pipe(Pipe&& o) noexcept { *this = std::move(o); }
    Pipe& operator=(Pipe&& o) noexcept {
        if (this != &o) {
            close();
            h_ = std::exchange(o.h_, 0);
            peer = std::move(o.peer); service = std::move(o.service); meta = std::move(o.meta); direct = o.direct;
        }
        return *this;
    }
    Pipe(const Pipe&) = delete;
    Pipe& operator=(const Pipe&) = delete;
    ~Pipe() { close(); }

    std::string peer, service, meta;
    bool direct = false;   ///< true = peer-to-peer, false = through the relay

    explicit operator bool() const { return h_ != 0; }

    /// Read up to `len` bytes; returns 0 at end of stream. Throws TimeoutError.
    size_t read(void* buf, size_t len, int timeout_ms = -1) {
        char* err = nullptr;
        int n = detail::Api::get().pipe_read(h_, buf, static_cast<int>(len > 0x7fffffff ? 0x7fffffff : len), timeout_ms, &err);
        detail::check(n, &err);
        return static_cast<size_t>(n);
    }

    /// Read everything until the peer closes its side.
    std::string read_all(int timeout_ms = -1) {
        std::string out;
        char buf[64 * 1024];
        for (size_t n; (n = read(buf, sizeof buf, timeout_ms)) > 0;) out.append(buf, n);
        return out;
    }

    /// Write all bytes (blocks while the receiver is slow).
    void write(const void* data, size_t len, int timeout_ms = -1) {
        const char* p = static_cast<const char*>(data);
        while (len > 0) {
            int chunk = static_cast<int>(len > (1u << 20) ? (1u << 20) : len);
            char* err = nullptr;
            int n = detail::Api::get().pipe_write(h_, p, chunk, timeout_ms, &err);
            detail::check(n, &err);
            p += n;
            len -= static_cast<size_t>(n);
        }
    }
    void write(std::string_view s, int timeout_ms = -1) { write(s.data(), s.size(), timeout_ms); }

    /// Send EOF to the peer; reading continues.
    void close_write() { if (h_) detail::Api::get().pipe_close_write(h_); }
    void close() { if (h_) { detail::Api::get().pipe_close(h_); h_ = 0; } }

private:
    uint64_t h_ = 0;
};

// ======================================================================
// Node
// ======================================================================

class Node {
public:
    explicit Node(const NodeOptions& opts) {
        char* err = nullptr;
        detail::check(api().node_open(opts.to_json().c_str(), &h_, &err), &err);
    }
    Node(const Node&) = delete;
    Node& operator=(const Node&) = delete;
    ~Node() { close(); }

    /// Node id (assigned by the relay if none was given).
    std::string id() const { return detail::take(api().node_id(h_)); }
    /// Connection details as JSON (relay, protocol, rtt_us, counters...).
    std::string info_json() const { return detail::take(api().node_info(h_)); }

    // ---- messaging ----

    /// Direct message. flags: Receipt | Store | Unreliable.
    void send(const std::string& to, std::string_view data, int flags = 0, int timeout_ms = 10000) {
        char* err = nullptr;
        detail::check(api().send(h_, to.c_str(), data.data(), static_cast<int>(data.size()), flags, timeout_ms, &err), &err);
    }
    void publish(const std::string& topic, std::string_view data, bool unreliable = false) {
        char* err = nullptr;
        detail::check(api().publish(h_, topic.c_str(), data.data(), static_cast<int>(data.size()), unreliable ? Unreliable : 0, &err), &err);
    }
    /// Subscribe to a pattern ("a.b", "a.*", "a.>"); messages arrive via receive()/on_message.
    void subscribe(const std::string& pattern, const std::string& queue = "") {
        char* err = nullptr;
        detail::check(api().subscribe(h_, pattern.c_str(), queue.empty() ? nullptr : queue.c_str(), &err), &err);
    }
    void unsubscribe(const std::string& pattern) {
        char* err = nullptr;
        detail::check(api().unsubscribe(h_, pattern.c_str(), &err), &err);
    }
    /// Serve requests sent to `topic` (load-balanced between servers).
    void serve(const std::string& topic) {
        char* err = nullptr;
        detail::check(api().serve(h_, topic.c_str(), &err), &err);
    }
    /// Request to a node; returns the response bytes.
    std::string request(const std::string& to, std::string_view data, int timeout_ms = 10000) {
        return do_request(to.c_str(), nullptr, data, timeout_ms);
    }
    /// Request to one server of a topic.
    std::string request_topic(const std::string& topic, std::string_view data, int timeout_ms = 10000) {
        return do_request(nullptr, topic.c_str(), data, timeout_ms);
    }
    /// Next message (std::nullopt on timeout). Do not mix with on_message().
    std::optional<Message> receive(int timeout_ms = -1) {
        detail::RawMessage* raw = nullptr;
        char* err = nullptr;
        int rc = api().recv(h_, timeout_ms, &raw, &err);
        detail::check(rc, &err);
        if (rc == 0 || !raw) return std::nullopt;
        Message m;
        m.kind = static_cast<Kind>(raw->kind);
        m.id = raw->id;
        m.from = raw->from ? raw->from : "";
        m.subject = raw->subject ? raw->subject : "";
        if (raw->data && raw->len > 0) m.data.assign(static_cast<const char*>(raw->data), static_cast<size_t>(raw->len));
        m.datagram = raw->datagram != 0;
        m.request = raw->request;
        api().message_free(raw);
        return m;
    }
    /// Answer a request message.
    void respond(const Message& req, std::string_view data, bool is_error = false) {
        char* err = nullptr;
        detail::check(api().respond(h_, req.request, data.data(), static_cast<int>(data.size()), is_error ? 1 : 0, &err), &err);
    }
    /// Run `handler` for every incoming message on a background thread
    /// (requests are answered with the returned string when the handler
    /// returns one; throw to send an error).
    /// Pass nullptr to stop the dispatcher.
    void on_message(std::function<std::optional<std::string>(const Message&)> handler) {
        stop_dispatch();
        if (!handler) return;
        running_ = true;
        dispatcher_ = std::thread([this, handler = std::move(handler)] {
            while (running_) {
                std::optional<Message> m;
                try { m = receive(200); } catch (const Error&) { if (!running_) return; std::this_thread::sleep_for(std::chrono::milliseconds(50)); continue; }
                if (!m) continue;
                try {
                    auto reply = handler(*m);
                    if (m->is_request() && reply) respond(*m, *reply);
                } catch (const std::exception& e) {
                    if (m->is_request()) { try { respond(*m, e.what(), true); } catch (...) {} }
                }
            }
        });
    }
    void watch_presence(bool on = true) {
        char* err = nullptr;
        detail::check(api().watch_presence(h_, on ? 1 : 0, &err), &err);
    }
    /// Round trip to the relay in microseconds.
    int64_t ping_us(int timeout_ms = 5000) {
        int64_t us = 0; char* err = nullptr;
        detail::check(api().ping(h_, timeout_ms, &us, &err), &err);
        return us;
    }
    std::string nodes_json(const std::string& prefix = "") {
        char* err = nullptr;
        char* r = api().list_nodes(h_, prefix.c_str(), &err);
        if (!r) detail::check(ERR, &err);
        return detail::take(r);
    }
    std::string relay_stats_json() {
        char* err = nullptr;
        char* r = api().relay_stats(h_, &err);
        if (!r) detail::check(ERR, &err);
        return detail::take(r);
    }

    // ---- pipes ----

    /// Open a byte stream to `to` for `service`. route: "" (default), auto, direct, relay.
    Pipe open_pipe(const std::string& to, const std::string& service, const std::string& meta = "",
                   const std::string& route = "", int timeout_ms = 15000) {
        uint64_t p = 0; char* err = nullptr;
        detail::check(api().pipe_open(h_, to.c_str(), service.c_str(), meta.c_str(), route.c_str(), timeout_ms, &p, &err), &err);
        return Pipe(p);
    }
    /// Next incoming pipe (std::nullopt on timeout).
    std::optional<Pipe> accept_pipe(int timeout_ms = -1) {
        uint64_t p = 0; char* err = nullptr;
        int rc = api().pipe_accept(h_, timeout_ms, &p, &err);
        detail::check(rc, &err);
        if (rc == 0) return std::nullopt;
        return Pipe(p);
    }

    // ---- files ----

    SharedFile share(const std::string& path, int64_t chunk_size = 0) {
        char* out = nullptr; char* err = nullptr;
        detail::check(api().share(h_, path.c_str(), chunk_size, &out, &err), &err);
        std::string js = detail::take(out);
        SharedFile f;
        f.hash = detail::json_str(js, "hash"); f.name = detail::json_str(js, "name");
        f.root = detail::json_str(js, "root"); f.path = detail::json_str(js, "path");
        f.link = detail::json_str(js, "link");
        f.size = static_cast<int64_t>(detail::json_num(js, "size"));
        f.chunk_size = static_cast<int64_t>(detail::json_num(js, "chunk_size"));
        return f;
    }
    void unshare(const std::string& hash) {
        char* err = nullptr;
        detail::check(api().unshare(h_, hash.c_str(), &err), &err);
    }
    /// Nodes serving a file, as JSON ({"hash","name","size","nodes":[...]}).
    std::string providers_json(const std::string& hash) {
        char* err = nullptr;
        char* r = api().find_providers(h_, hash.c_str(), &err);
        if (!r) detail::check(ERR, &err);
        return detail::take(r);
    }
    /// Download a magnetk link (or hash) to `out` (file, directory or "").
    DownloadResult download(const std::string& link, const std::string& out = "",
                            const std::function<void(const Progress&)>& progress = nullptr,
                            const std::string& route = "", bool seed = false) {
        std::string opts = "{\"seed\":" + std::string(seed ? "true" : "false");
        if (!route.empty()) opts += ",\"route\":" + detail::json_escape(route);
        opts += "}";
        uint64_t dl = 0; char* err = nullptr;
        detail::check(api().download_start(h_, link.c_str(), out.empty() ? nullptr : out.c_str(), opts.c_str(), &dl, &err), &err);
        struct Guard { uint64_t d; ~Guard() { detail::Api::get().download_free(d); } } guard{dl};
        for (;;) {
            char* pj = nullptr; err = nullptr;
            int rc = api().download_poll(dl, 250, &pj, &err);
            std::string js = detail::take(pj);
            if (progress) {
                Progress p;
                p.name = detail::json_str(js, "name");
                p.size = static_cast<int64_t>(detail::json_num(js, "size"));
                p.done = static_cast<int64_t>(detail::json_num(js, "done"));
                p.percent = detail::json_num(js, "percent");
                p.bytes_per_sec = detail::json_num(js, "bytes_per_sec");
                p.eta_seconds = detail::json_num(js, "eta_ms") / 1000.0;
                p.sources = static_cast<int>(detail::json_num(js, "sources"));
                p.streams = static_cast<int>(detail::json_num(js, "streams"));
                progress(p);
            }
            detail::check(rc, &err);
            if (rc == 1) {
                DownloadResult r;
                r.path = detail::json_str(js, "path"); r.hash = detail::json_str(js, "hash");
                r.size = static_cast<int64_t>(detail::json_num(js, "size"));
                r.bytes = static_cast<int64_t>(detail::json_num(js, "bytes"));
                r.resumed = static_cast<int64_t>(detail::json_num(js, "resumed"));
                r.seconds = detail::json_num(js, "elapsed_ms") / 1000.0;
                return r;
            }
        }
    }

    void close() {
        stop_dispatch();
        if (h_) { api().node_close(h_); h_ = 0; }
    }

private:
    static detail::Api& api() { return detail::Api::get(); }
    std::string do_request(const char* to, const char* topic, std::string_view data, int timeout_ms) {
        void* out = nullptr; int out_len = 0; char* err = nullptr;
        detail::check(api().request(h_, to, topic, data.data(), static_cast<int>(data.size()), timeout_ms, &out, &out_len, &err), &err);
        std::string r;
        if (out) { r.assign(static_cast<const char*>(out), static_cast<size_t>(out_len)); api().free_(out); }
        return r;
    }
    void stop_dispatch() {
        running_ = false;
        if (dispatcher_.joinable()) dispatcher_.join();
    }

    uint64_t h_ = 0;
    std::atomic<bool> running_{false};
    std::thread dispatcher_;
};

// ======================================================================
// Embedded relay
// ======================================================================

struct RelayOptions {
    std::string listen = "127.0.0.1:0";
    std::string id;
    std::vector<std::string> tokens;
    std::vector<std::string> peers;
    std::string cluster_secret;
    std::string transport = "auto";
    bool log = false;
};

/// Runs a relay inside this process (tests, small deployments).
class Relay {
public:
    explicit Relay(const RelayOptions& o = {}) {
        using detail::json_escape;
        std::string j = "{\"listen\":" + json_escape(o.listen) + ",\"transport\":" + json_escape(o.transport);
        if (!o.id.empty()) j += ",\"id\":" + json_escape(o.id);
        if (!o.tokens.empty()) j += ",\"tokens\":" + detail::json_array(o.tokens);
        if (!o.peers.empty()) j += ",\"peers\":" + detail::json_array(o.peers);
        if (!o.cluster_secret.empty()) j += ",\"cluster_secret\":" + json_escape(o.cluster_secret);
        if (o.log) j += ",\"log\":true";
        j += "}";
        char* err = nullptr;
        detail::check(detail::Api::get().relay_start(j.c_str(), &h_, &err), &err);
        host_ = o.listen.substr(0, o.listen.rfind(':'));
        if (host_.empty() || host_ == "0.0.0.0") host_ = "127.0.0.1";
    }
    Relay(const Relay&) = delete;
    Relay& operator=(const Relay&) = delete;
    ~Relay() { close(); }

    int port() const { return detail::Api::get().relay_port(h_); }
    std::string address() const { return host_ + ":" + std::to_string(port()); }
    std::string stats_json() const { return detail::take(detail::Api::get().relay_stats_json(h_)); }
    void close() { if (h_) { detail::Api::get().relay_close(h_); h_ = 0; } }

private:
    uint64_t h_ = 0;
    std::string host_;
};

} // namespace magnetk

#endif // MAGNETK_HPP
