#include "magnet.hpp"
#include <sstream>
#include <map>
#include <algorithm>

namespace magnet {

MagnetLink MagnetLink::parse(const std::string& uri) {
    if (uri.substr(0, 8) != "magnet:?") {
        throw Error("Invalid magnet link: must start with magnet:?");
    }

    std::string query = uri.substr(8);
    std::map<std::string, std::string> params;

    size_t pos = 0;
    while (pos < query.length()) {
        size_t end = query.find('&', pos);
        if (end == std::string::npos) end = query.length();

        std::string pair = query.substr(pos, end - pos);
        size_t eq = pair.find('=');
        if (eq != std::string::npos) {
            std::string key = pair.substr(0, eq);
            std::string val = pair.substr(eq + 1);
            params[key] = val;
        }

        pos = end + 1;
    }

    MagnetLink ml;
    if (params.count("xt")) {
        std::string xt = params["xt"];
        if (xt.substr(0, 9) == "urn:sha256") { // loosely match
            // urn:sha256:<hash>
            size_t last_colon = xt.rfind(':');
            if (last_colon != std::string::npos) {
                ml.file_hash = xt.substr(last_colon + 1);
            }
        }
    }
    if (ml.file_hash.empty()) {
        throw Error("Invalid magnet link: missing xt (urn:sha256:...)");
    }

    if (params.count("dn")) ml.file_name = params["dn"];
    if (params.count("xl")) ml.file_size = std::stoll(params["xl"]);
    if (params.count("xs")) ml.seed_id = params["xs"];
    if (params.count("relay")) ml.relay_addr = params["relay"];

    return ml;
}

std::string MagnetLink::to_string() const {
    std::stringstream ss;
    ss << "magnet:?xt=urn:sha256:" << file_hash;
    if (!file_name.empty()) ss << "&dn=" << file_name;
    if (file_size > 0) ss << "&xl=" << file_size;
    if (!seed_id.empty()) ss << "&xs=" << seed_id;
    if (!relay_addr.empty()) ss << "&relay=" << relay_addr;
    return ss.str();
}

} // namespace magnet
