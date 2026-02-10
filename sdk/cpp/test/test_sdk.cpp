#include <iostream>
#include <cassert>
#include "magnet.hpp"

void test_magnet_link() {
    std::string uri = "magnet:?xt=urn:sha256:abc123hash&dn=test.txt&xl=1024";
    auto ml = magnet::MagnetLink::parse(uri);
    
    assert(ml.file_hash == "abc123hash");
    assert(ml.file_name == "test.txt");
    assert(ml.file_size == 1024);
    
    std::cout << "MagnetLink test passed" << std::endl;
}

int main() {
    test_magnet_link();
    
    // Test Client initialization (mocks transport)
    magnet::Client::Config cfg;
    cfg.bind_address = "127.0.0.1";
    
    try {
        magnet::Client client(cfg);
        std::cout << "Client initialized successfully" << std::endl;
        
        client.download(
            "magnet:?xt=urn:sha256:dummy&dn=dummy.bin&xl=1000",
            "./",
            [](const magnet::Progress& p) {
                std::cout << "Progress: " << p.percent << "%" << std::endl;
            }
        );
    } catch (const std::exception& e) {
        std::cerr << "Client error: " << e.what() << std::endl;
    }

    return 0;
}
