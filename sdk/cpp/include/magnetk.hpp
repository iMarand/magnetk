#ifndef MAGNETK_HPP
#define MAGNETK_HPP

#include <string>
#include <vector>
#include <functional>
#include <memory>
#include <iostream>
#include <cstdint>
#include <stdexcept>

#include "transport.h"

namespace magnetk {

/**
 * High-Level C++ SDK for Magnet P2P.
 * Wraps the C MsQuic transport layer.
 */

/* ===== Errors ===== */
class Error : public std::runtime_error {
public:
    Error(const std::string& msg, magnetk_status_t code = MAGNETK_OK)
        : std::runtime_error(msg), code_(code) {}
    magnetk_status_t code() const { return code_; }
private:
    magnetk_status_t code_;
};

/* ===== Magnet Links ===== */
struct MagnetLink {
    std::string file_hash;
    std::string file_name;
    int64_t     file_size = 0;
    std::string seed_id;
    std::string relay_addr;
    bool        dht_enabled = false;

    static MagnetLink parse(const std::string& uri);
    std::string to_string() const;
};

/* ===== Progress Callback ===== */
struct Progress {
    std::string file_name;
    int64_t     file_size;
    int64_t     downloaded;
    uint32_t    total_chunks = 0;
    uint32_t    done_chunks = 0;
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
        std::string relay_assisted = "auto"; // "auto" | "true" | "false"
        bool        hole_punch = true;
        std::string peer_path = "peer";
        std::string config_path;
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
    struct ProcessHandle;

    struct Config {
        std::string bind_address;
        uint16_t    port = 4002;
        std::string cert_file;
        std::string key_file;
        std::string relay_addr = "69.169.109.243:4003";
        std::string relay_assisted = "auto";
        std::string transport_mode = "auto";
        bool        hole_punch = true;
        bool        dht_enabled = false;
        std::string seed_path = "seed";
        std::string config_path;
    };

    Seed(const Config& config);
    ~Seed();

    // Publish a file (generates manifest, returns magnet link)
    std::string publish(const std::string& file_path);

private:
    Config config_;
    magnetk_transport_t* transport_ = nullptr;
    magnetk_listener_t* listener_ = nullptr;
    std::unique_ptr<ProcessHandle> process_;
};

} // namespace magnetk

#endif // MAGNETK_HPP
