#include "magnet.hpp"

namespace magnet {

Seed::Seed(const Config& config) : config_(config) {
    magnet_config_t c_config;
    magnet_config_default(&c_config);

    c_config.bind_address = config.bind_address.c_str();
    c_config.port = config.port;
    if (!config.cert_file.empty()) c_config.cert_file = config.cert_file.c_str();
    if (!config.key_file.empty()) c_config.key_file = config.key_file.c_str();
    c_config.alpn = "magnet";

    magnet_transport_init(&c_config, &transport_);

    // Start listener
    // Note: C listener callbacks need C-style function pointers
    // For full C++ integration, we'd use a static trampoline similar to CGo
    // magnet_listen(transport_, ..., &listener_);
}

Seed::~Seed() {
    if (listener_) {
        magnet_listener_close(listener_);
    }
    if (transport_) {
        magnet_transport_shutdown(transport_);
    }
}

std::string Seed::publish(const std::string& file_path) {
    // Generate manifest logic would go here
    // Return magnet link
    return "magnet:?xt=urn:sha256:TODO&dn=" + file_path;
}

} // namespace magnet
