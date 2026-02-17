#include "magnetk.hpp"
#include <iostream>
#include <string>

int main(int argc, char* argv[]) {
    if (argc < 2) {
        std::cerr << "Usage: ./download <magnetk_link>" << std::endl;
        return 1;
    }

    std::string magnetLink = argv[1];

    magnetk::Client::Config cfg;
    cfg.dht_enabled = true; // Enable DHT
    
    try {
        magnetk::Client client(cfg);

        std::cout << "Downloading " << magnetLink << "..." << std::endl;

        client.download(magnetLink, "./downloads/", [](const magnetk::Progress& p) {
            std::cout << "\rProgress: " << p.percent << "% " << std::flush;
        });
        
        std::cout << std::endl << "Download complete!" << std::endl;

    } catch (const std::exception& e) {
        std::cerr << "Error: " << e.what() << std::endl;
        return 1;
    }

    return 0;
}
