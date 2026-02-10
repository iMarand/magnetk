#include "magnetk.hpp"
#include <iostream>
#include <thread>
#include <chrono>

namespace magnetk {

Client::Client(const Config& config) : config_(config), transport_(nullptr) {
    magnetk_config_t c_config;
    magnetk_config_default(&c_config);

    c_config.bind_address = config.bind_address.c_str();
    c_config.max_connections = config.max_conns;
    c_config.no_cert_validation = config.no_cert_validation;
    c_config.alpn = "magnetk";

    magnet_status_t status = magnetk_transport_init(&c_config, &transport_);
    if (status != MAGNET_OK) {
        throw Error("Failed to initialize transport: " + std::to_string(status));
    }
}

Client::~Client() {
    if (transport_) {
        magnet_transport_shutdown(transport_);
    }
}

void Client::download(const std::string& magnet_uri, const std::string& output_path, ProgressCallback on_progress) {
    auto ml = MagnetLink::parse(magnet_uri);

    // TODO: Connect to relay -> get seeds
    // For now, assume seed address is somehow known or resolved?
    // The C transport Connect takes host/port.
    // If relay_addr is in magnet link, we'd use that.

    // This is a placeholder for the actual download logic which would involve:
    // 1. Connect to seed (using transport->connect)
    // 2. Open stream
    // 3. Send manifest request
    // 4. Receive manifest
    // 5. Request chunks
    // 6. Write chunks to file

    std::cout << "[CPP-SDK] downloading " << ml.file_name << " (" << ml.file_size << " bytes)" << std::endl;

    // Simulate progress for now as we don't have a running C++ seed to test against yet
    if (on_progress) {
        Progress p;
        p.file_name = ml.file_name;
        p.file_size = ml.file_size;
        p.downloaded = 0;
        p.percent = 0.0;
        
        for (int i = 0; i <= 10; ++i) {
            std::this_thread::sleep_for(std::chrono::milliseconds(100));
            p.downloaded = ml.file_size * i / 10;
            p.percent = i * 10.0;
            on_progress(p);
        }
    }
}

} // namespace magnetk
