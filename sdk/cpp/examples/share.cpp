// Share a file and keep serving it.
//   share <file> [relay host:port]
#include <magnetk.hpp>

#include <chrono>
#include <iostream>
#include <thread>

int main(int argc, char** argv) {
    if (argc < 2) {
        std::cerr << "usage: share <file> [relay host:port]\n";
        return 2;
    }
    try {
        magnetk::NodeOptions opts;
        opts.relays = {argc > 2 ? argv[2] : "127.0.0.1:4002"};
        opts.listen = true;  // also accept peer-to-peer downloads
        magnetk::Node node(opts);
        auto f = node.share(argv[1]);
        std::cout << "Sharing " << f.name << " (" << f.size << " bytes)\n";
        std::cout << "Magnetk Link: " << f.link << "\n";
        std::cout << "Serving; Ctrl+C to stop.\n";
        for (;;) std::this_thread::sleep_for(std::chrono::seconds(60));
    } catch (const magnetk::Error& e) {
        std::cerr << "error: " << e.what() << "\n";
        return 1;
    }
}
