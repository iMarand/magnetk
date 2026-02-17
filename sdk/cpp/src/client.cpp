#include "magnetk.hpp"
#include <iostream>
#include <thread>
#include <chrono>
#include <array>
#include <cstdio>
#include <stdexcept>
#include <memory>
#include <sstream>

#ifdef _WIN32
    #define POPEN _popen
    #define PCLOSE _pclose
#else
    #define POPEN popen
    #define PCLOSE pclose
#endif

namespace magnetk {

Client::Client(const Config& config) : config_(config), transport_(nullptr) {
    // Native transport init kept for future use
    /*
    magnetk_config_t c_config;
    magnetk_config_default(&c_config);

    c_config.bind_address = config.bind_address.c_str();
    c_config.max_connections = config.max_conns;
    c_config.no_cert_validation = config.no_cert_validation;
    c_config.alpn = "magnetk";

    magnetk_transport_init(&c_config, &transport_);
    */
}

Client::~Client() {
    /*
    if (transport_) {
        magnet_transport_shutdown(transport_);
    }
    */
}

void Client::download(const std::string& magnet_uri, const std::string& output_path, ProgressCallback on_progress) {
    std::string cmd = "peer "; // Assumes 'peer' is in PATH
    cmd += "-magnetk \"" + magnet_uri + "\" ";
    cmd += "-output \"" + output_path + "\" ";
    
    // Add config? 
    // cmd += "-config settings.conf ";

    std::cout << "[CPP-SDK] Executing: " << cmd << std::endl;

    std::array<char, 128> buffer;
    std::unique_ptr<FILE, decltype(&PCLOSE)> pipe(POPEN(cmd.c_str(), "r"), PCLOSE);
    if (!pipe) {
        throw std::runtime_error("popen() failed!");
    }

    // Capture stdout to monitor progress?
    // The peer binary prints progress lines?
    // If on_progress is set, we could parse "Progress: 10.5%" from stdout.
    
    while (fgets(buffer.data(), buffer.size(), pipe.get()) != nullptr) {
        std::string line = buffer.data();
        std::cout << line; // Stream to console
        
        // Basic progress parsing (example)
        if (on_progress && line.find("Progress:") != std::string::npos) {
            // TODO: Parse percentage and call callback
            // For now, just let it print to stdout
        }
    }
    
    // Check exit status? PCLOSE returns it.
}

} // namespace magnetk

