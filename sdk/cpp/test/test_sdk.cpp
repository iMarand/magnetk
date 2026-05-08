#include <iostream>
#include <cassert>
#include "magnetk.hpp"

void test_magnet_link() {
    std::string uri = "magnetk:?xt=urn:sha256:abc123hash&dn=test.txt&xl=1024&relay=%2Fip4%2F127.0.0.1%2Ftcp%2F4001%2Fp2p%2Frelay";
    auto ml = magnetk::MagnetLink::parse(uri);
    
    assert(ml.file_hash == "abc123hash");
    assert(ml.file_name == "test.txt");
    assert(ml.file_size == 1024);
    assert(ml.relay_addr == "/ip4/127.0.0.1/tcp/4001/p2p/relay");
    assert(ml.to_string() == uri);
    
    std::cout << "MagnetLink test passed" << std::endl;
}

int main() {
    test_magnet_link();
    
    // Test Client initialization (mocks transport)
    magnetk::Client::Config cfg;
    cfg.bind_address = "127.0.0.1";
    
    try {
        magnetk::Client client(cfg);
        std::cout << "Client initialized successfully" << std::endl;
    } catch (const std::exception& e) {
        std::cerr << "Client error: " << e.what() << std::endl;
    }

    return 0;
}
