#include "magnetk.hpp"
#include <array>
#include <cstdio>
#include <memory>
#include <regex>
#include <sstream>
#include <stdexcept>

#ifdef _WIN32
#define POPEN _popen
#define PCLOSE _pclose
#else
#define POPEN popen
#define PCLOSE pclose
#endif

namespace magnetk {

static std::string quote_arg(const std::string& value) {
    std::string out = "\"";
    for (char c : value) {
        if (c == '"') out += "\\\"";
        else out += c;
    }
    out += "\"";
    return out;
}

static void append_flag(std::stringstream& cmd, const std::string& name, const std::string& value) {
    if (!value.empty()) {
        cmd << " " << name << " " << quote_arg(value);
    }
}

Client::Client(const Config& config) : config_(config), transport_(nullptr) {
}

Client::~Client() {
}

void Client::download(const std::string& magnet_uri, const std::string& output_path, ProgressCallback on_progress) {
    std::stringstream cmd;
    cmd << quote_arg(config_.peer_path)
        << " -magnetk " << quote_arg(magnet_uri)
        << " -output " << quote_arg(output_path);

    append_flag(cmd, "-config", config_.config_path);

    if (!config_.transport_mode.empty() && config_.transport_mode != "auto") {
        append_flag(cmd, "-transport", config_.transport_mode);
    }
    if (!config_.relay_assisted.empty()) {
        append_flag(cmd, "-relay-assisted", config_.relay_assisted);
    }
    if (config_.hole_punch) {
        cmd << " -holepunch";
    }

    // Capture stderr too; Go's log package writes there.
    cmd << " 2>&1";

    std::array<char, 512> buffer{};
    std::unique_ptr<FILE, decltype(&PCLOSE)> pipe(POPEN(cmd.str().c_str(), "r"), PCLOSE);
    if (!pipe) {
        throw std::runtime_error("failed to start peer process");
    }

    std::regex progress_re(R"(Progress:\s+([0-9.]+)%.*\(([0-9]+)\/([0-9]+)\s+chunks.*?([0-9.]+)\/([0-9.]+)\s+MB.*?\|\s+([0-9.]+)\s+MB/s)");
    std::regex receiving_re(R"(Receiving chunk\s+([0-9]+)\/([0-9]+):.*total\s+([0-9.]+)\/([0-9.]+)\s+MB.*?\|\s+([0-9.]+)\s+MB/s)");

    while (fgets(buffer.data(), static_cast<int>(buffer.size()), pipe.get()) != nullptr) {
        std::string line = buffer.data();
        std::cout << line;

        if (!on_progress) {
            continue;
        }

        std::smatch m;
        Progress p{};
        if (std::regex_search(line, m, progress_re)) {
            p.percent = std::stod(m[1]);
            p.done_chunks = static_cast<uint32_t>(std::stoul(m[2]));
            p.total_chunks = static_cast<uint32_t>(std::stoul(m[3]));
            p.downloaded = static_cast<int64_t>(std::stod(m[4]) * 1024 * 1024);
            p.file_size = static_cast<int64_t>(std::stod(m[5]) * 1024 * 1024);
            p.speed_mbps = std::stod(m[6]);
            on_progress(p);
        } else if (std::regex_search(line, m, receiving_re)) {
            p.done_chunks = static_cast<uint32_t>(std::stoul(m[1]) - 1);
            p.total_chunks = static_cast<uint32_t>(std::stoul(m[2]));
            p.downloaded = static_cast<int64_t>(std::stod(m[3]) * 1024 * 1024);
            p.file_size = static_cast<int64_t>(std::stod(m[4]) * 1024 * 1024);
            p.percent = p.file_size > 0 ? (static_cast<double>(p.downloaded) / static_cast<double>(p.file_size)) * 100.0 : 0.0;
            p.speed_mbps = std::stod(m[5]);
            on_progress(p);
        }
    }

    int status = PCLOSE(pipe.release());
    if (status != 0) {
        throw std::runtime_error("peer process failed with status " + std::to_string(status));
    }
}

} // namespace magnetk
