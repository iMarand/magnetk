#include "magnetk.hpp"
#include <iostream>
#include <string>

int main(int argc, char* argv[]) {
    if (argc < 2) {
        std::cerr << "Usage: ./download <magnetk_link> [output_dir_or_file]" << std::endl;
        return 1;
    }

    std::string magnetLink = argv[1];
    std::string outputPath = argc > 2 ? argv[2] : "./downloads/";

    magnetk::Client::Config cfg;
    cfg.hole_punch = true;
    cfg.relay_assisted = "auto";
    cfg.transport_mode = "auto";
    
    try {
        magnetk::Client client(cfg);

        std::cout << "Downloading " << magnetLink << "..." << std::endl;

        client.download(magnetLink, outputPath, [](const magnetk::Progress& p) {
            std::cout << "\rProgress: " << p.percent
                      << "% (" << p.done_chunks << "/" << p.total_chunks << " chunks)"
                      << " | " << p.speed_mbps << " MB/s " << std::flush;
        });
        
        std::cout << std::endl << "Download complete!" << std::endl;

    } catch (const std::exception& e) {
        std::cerr << "Error: " << e.what() << std::endl;
        return 1;
    }

    return 0;
}
