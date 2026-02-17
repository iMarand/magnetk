#include "magnetk.hpp"
#include <array>
#include <cstdio>
#include <stdexcept>
#include <memory>
#include <iostream>
#include <sstream>

#ifdef _WIN32
    #define POPEN _popen
    #define PCLOSE _pclose
#else
    #define POPEN popen
    #define PCLOSE pclose
#endif

namespace magnetk {

Seed::Seed(const Config& config) : config_(config) {
    // Native transport init kept for future use, though wrapper mode ignores it
    magnetk_config_t c_config;
    magnetk_config_default(&c_config);

    c_config.bind_address = config.bind_address.c_str();
    c_config.port = config.port;
    if (!config.cert_file.empty()) c_config.cert_file = config.cert_file.c_str();
    if (!config.key_file.empty()) c_config.key_file = config.key_file.c_str();
    c_config.alpn = "magnetk";
    
    // Check if C transport lib is linked and working
    // magnetk_transport_init(&c_config, &transport_);
}

Seed::~Seed() {
    // if (transport_) magnet_transport_shutdown(transport_);
}

std::string Seed::publish(const std::string& file_path) {
    std::string cmd = "seed "; // Assumes 'seed' is in PATH. TODO: Robust path resolution.
    cmd += "-file \"" + file_path + "\" ";
    cmd += "-port " + std::to_string(config_.port) + " ";
    
    // Redirect stderr to stdout to capture logs? Or let it flow to console?
    // We need to capture "Magnetk Link: ..." from stdout.
    
    std::cout << "[CPP-SDK] Executing: " << cmd << std::endl;

    std::array<char, 128> buffer;
    std::string result;
    std::string magnet_link;

    std::unique_ptr<FILE, decltype(&PCLOSE)> pipe(POPEN(cmd.c_str(), "r"), PCLOSE);
    if (!pipe) {
        throw std::runtime_error("popen() failed!");
    }

    while (fgets(buffer.data(), buffer.size(), pipe.get()) != nullptr) {
        std::string line = buffer.data();
        std::cout << line; // Stream output to console
        
        // Ensure we capture even if line is partial? 
        // fgets reads up to newline.
        if (line.find("Magnetk Link:") != std::string::npos) {
             size_t pos = line.find("Magnetk Link:");
             magnet_link = line.substr(pos + 13);
             // Trim whitespace
             magnet_link.erase(0, magnet_link.find_first_not_of(" \n\r\t"));
             magnet_link.erase(magnet_link.find_last_not_of(" \n\r\t") + 1);
        }
    }
    
    if (magnet_link.empty()) {
        throw std::runtime_error("Failed to extract Magnetk Link from seeder output");
    }

    return magnet_link;
}

} // namespace magnetk

