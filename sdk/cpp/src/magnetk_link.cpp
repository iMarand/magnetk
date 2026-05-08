#include "magnetk.hpp"
#include <sstream>
#include <map>
#include <algorithm>
#include <cctype>
#include <cstdlib>
#include <iomanip>

namespace magnetk {

static std::string url_decode(const std::string& input) {
    std::string out;
    out.reserve(input.size());
    for (size_t i = 0; i < input.size(); ++i) {
        if (input[i] == '%' && i + 2 < input.size()) {
            const std::string hex = input.substr(i + 1, 2);
            char* end = nullptr;
            long value = std::strtol(hex.c_str(), &end, 16);
            if (end && *end == '\0') {
                out.push_back(static_cast<char>(value));
                i += 2;
                continue;
            }
        }
        if (input[i] == '+') {
            out.push_back(' ');
        } else {
            out.push_back(input[i]);
        }
    }
    return out;
}

static std::string url_encode(const std::string& input) {
    std::ostringstream out;
    out << std::uppercase << std::hex;
    for (unsigned char c : input) {
        if (std::isalnum(c) || c == '-' || c == '_' || c == '.' || c == '~') {
            out << c;
        } else {
            out << '%' << std::setw(2) << std::setfill('0') << static_cast<int>(c);
        }
    }
    return out.str();
}

MagnetLink MagnetLink::parse(const std::string& uri) {
    const std::string prefix = "magnetk:?";
    if (uri.rfind(prefix, 0) != 0) {
        throw Error("Invalid magnet link: must start with magnetk:?");
    }

    std::string query = uri.substr(prefix.size());
    std::map<std::string, std::string> params;

    size_t pos = 0;
    while (pos < query.length()) {
        size_t end = query.find('&', pos);
        if (end == std::string::npos) end = query.length();

        std::string pair = query.substr(pos, end - pos);
        size_t eq = pair.find('=');
        if (eq != std::string::npos) {
            std::string key = pair.substr(0, eq);
            std::string val = url_decode(pair.substr(eq + 1));
            params[key] = val;
        }

        pos = end + 1;
    }

    MagnetLink ml;
    if (params.count("xt")) {
        std::string xt = params["xt"];
        if (xt.rfind("urn:sha256:", 0) == 0) {
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
    if (params.count("dht")) {
        std::string dht = params["dht"];
        std::transform(dht.begin(), dht.end(), dht.begin(), [](unsigned char c) { return static_cast<char>(std::tolower(c)); });
        ml.dht_enabled = dht == "true" || dht == "1";
    }

    if (ml.relay_addr.empty() && !ml.dht_enabled) {
        throw Error("Magnet link needs either relay address or dht=true for discovery");
    }

    return ml;
}

std::string MagnetLink::to_string() const {
    std::stringstream ss;
    ss << "magnetk:?xt=urn:sha256:" << file_hash;
    if (!file_name.empty()) ss << "&dn=" << url_encode(file_name);
    if (file_size > 0) ss << "&xl=" << file_size;
    if (!seed_id.empty()) ss << "&xs=" << url_encode(seed_id);
    if (!relay_addr.empty()) ss << "&relay=" << url_encode(relay_addr);
    if (dht_enabled) ss << "&dht=true";
    return ss.str();
}

} // namespace magnetk
