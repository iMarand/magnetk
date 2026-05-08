#include "magnetk.hpp"
#include <iostream>
#include <string>

int main(int argc, char* argv[]) {
    magnetk::Seed::Config cfg;
    cfg.port = 4002;
    cfg.relay_addr = "69.169.109.243:4003";
    cfg.hole_punch = true;
    cfg.relay_assisted = "auto";

    std::string filePath = argc > 1 ? argv[1] : "./test_file.txt";
    
    try {
        magnetk::Seed seed(cfg);

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
