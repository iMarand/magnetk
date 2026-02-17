#include "magnetk.hpp"
#include <iostream>
#include <string>

int main() {
    magnetk::Seed::Config cfg;
    // Configure Seed
    cfg.port = 4002;
    cfg.dht_enabled = true; // Enable DHT
    
    try {
        magnetk::Seed seed(cfg);

        std::string filePath = "./test_file.txt";
        std::cout << "Sharing " << filePath << "..." << std::endl;

        std::string link = seed.publish(filePath);

        std::cout << "--------------------------------------------------" << std::endl;
        std::cout << "FILE SHARED SUCCESSFULLY!" << std::endl;
        std::cout << "Magnetk Link: " << link << std::endl;
        std::cout << "--------------------------------------------------" << std::endl;

        std::cout << "Press Enter to stop seeding..." << std::endl;
        std::cin.get();
        
    } catch (const std::exception& e) {
        std::cerr << "Error: " << e.what() << std::endl;
        return 1;
    }

    return 0;
}
