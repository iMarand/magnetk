// Request/reply service and client.
//   rpc server [relay]     (run several: they share the load)
//   rpc client [relay]
#include <magnetk.hpp>

#include <chrono>
#include <iostream>
#include <string>
#include <thread>

int main(int argc, char** argv) {
    if (argc < 2) {
        std::cerr << "usage: rpc server|client [relay host:port]\n";
        return 2;
    }
    try {
        magnetk::NodeOptions opts;
        opts.relays = {argc > 2 ? argv[2] : "127.0.0.1:4002"};
        magnetk::Node node(opts);
        if (std::string(argv[1]) == "server") {
            node.serve("svc.upper");
            const std::string id = node.id();
            node.on_message([id](const magnetk::Message& m) -> std::optional<std::string> {
                if (!m.is_request()) return std::nullopt;
                std::string out = m.data;
                for (auto& c : out) c = static_cast<char>(std::toupper(static_cast<unsigned char>(c)));
                return out + " (by " + id + ")";
            });
            std::cout << "serving svc.upper as " << id << "\n";
            for (;;) std::this_thread::sleep_for(std::chrono::seconds(60));
        }
        for (int i = 0; i < 5; i++) {
            auto t0 = std::chrono::steady_clock::now();
            std::string r = node.request_topic("svc.upper", "hello " + std::to_string(i));
            auto us = std::chrono::duration_cast<std::chrono::microseconds>(std::chrono::steady_clock::now() - t0).count();
            std::cout << r << "  (" << us << " us)\n";
        }
    } catch (const magnetk::Error& e) {
        std::cerr << "error: " << e.what() << "\n";
        return 1;
    }
    return 0;
}
