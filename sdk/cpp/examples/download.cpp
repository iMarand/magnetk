// Download a magnetk link (resumes automatically).
//   download "<magnetk link>" [output] [relay host:port]
#include <magnetk.hpp>

#include <cstdio>
#include <iostream>

int main(int argc, char** argv) {
    if (argc < 2) {
        std::cerr << "usage: download <link> [output] [relay host:port]\n";
        return 2;
    }
    try {
        std::string link = argv[1];
        // Relays default to the first relay= parameter of the link.
        std::string relay = argc > 3 ? argv[3] : "";
        if (relay.empty()) {
            auto p = link.find("relay=");
            if (p != std::string::npos) {
                relay = link.substr(p + 6, link.find('&', p) - p - 6);
                for (auto q = relay.find("%3A"); q != std::string::npos; q = relay.find("%3A")) relay.replace(q, 3, ":");
            }
        }
        magnetk::NodeOptions opts;
        opts.relays = {relay.empty() ? "127.0.0.1:4002" : relay};
        magnetk::Node node(opts);
        auto res = node.download(link, argc > 2 ? argv[2] : "", [](const magnetk::Progress& p) {
            std::printf("\r%5.1f%%  %.1f MB/s  eta %.0fs  [%d source(s)]   ", p.percent, p.bytes_per_sec / 1e6, p.eta_seconds, p.sources);
            std::fflush(stdout);
        });
        std::printf("\nsaved %s (%lld bytes in %.2fs), sha256 %s\n", res.path.c_str(), static_cast<long long>(res.size), res.seconds, res.hash.c_str());
    } catch (const magnetk::Error& e) {
        std::cerr << "\nerror: " << e.what() << "\n";
        return 1;
    }
    return 0;
}
