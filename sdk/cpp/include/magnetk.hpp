#ifndef MAGNETK_HPP
#define MAGNETK_HPP

#include <string>
#include <vector>
#include <functional>
#include <memory>
#include <iostream>

#include "transport.h"

namespace magnetk {

/**
 * High-Level C++ SDK for Magnet P2P.
 * Wraps the C MsQuic transport layer.
 */

/* ===== Errors ===== */
class Error : public std::runtime_error {
public:
    Error(const std::string& msg, magnet_status_t code = MAGNET_OK)
        : std::runtime_error(msg), code_(code) {}
    magnet_status_t code() const { return code_; }
private:
    magnet_status_t code_;
};

/* ===== Magnet Links ===== */
struct MagnetLink {
    std::string file_hash;
    std::string file_name;
    int64_t     file_size = 0;
    std::string seed_id;
    std::string relay_addr;

    static MagnetLink parse(const std::string& uri);
    std::string to_string() const;
};

/* ===== Progress Callback ===== */
struct Progress {
    std::string file_name;
    int64_t     file_size;
    int64_t     downloaded;
    double      percent;
    double      speed_mbps;
};

using ProgressCallback = std::function<void(const Progress&)>;

/* ===== Client ===== */
class Client {
public:
    struct Config {
        std::string bind_address;
        uint16_t    max_conns = 10;
        bool        no_cert_validation = true;
        std::string transport_mode = "auto"; // "auto" | "quic" | "tcp"
    };

    Client(const Config& config = Config());
    ~Client();

    // Prevent copy
    Client(const Client&) = delete;
    Client& operator=(const Client&) = delete;

    // Download a file
    void download(const std::string& magnet_uri, const std::string& output_path, ProgressCallback on_progress = nullptr);

private:
    Config config_;
    magnetk_transport_t* transport_ = nullptr;
};

/* ===== Seed ===== */
class Seed {
public:
    struct Config {
        std::string bind_address;
        uint16_t    port = 4002;
        std::string cert_file;
        std::string key_file;
    };

    Seed(const Config& config);
    ~Seed();

    // Publish a file (generates manifest, returns magnet link)
    std::string publish(const std::string& file_path);

private:
    Config config_;
    magnetk_transport_t* transport_ = nullptr;
    magnet_listener_t* listener_ = nullptr;
};

} // namespace magnetk

#endif // MAGNETK_HPP
