#include "magnetk.hpp"
#include <array>
#include <atomic>
#include <chrono>
#include <cstdio>
#include <memory>
#include <sstream>
#include <stdexcept>
#include <thread>

#ifdef _WIN32
#define NOMINMAX
#include <windows.h>
#else
#include <csignal>
#include <fcntl.h>
#include <sys/types.h>
#include <sys/wait.h>
#include <unistd.h>
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

struct Seed::ProcessHandle {
#ifdef _WIN32
    PROCESS_INFORMATION pi{};
    HANDLE stdout_read = nullptr;
#else
    pid_t pid = -1;
    int stdout_fd = -1;
#endif
    std::thread drain_thread;
    std::atomic<bool> stopping{false};
};

static std::string trim(std::string s) {
    const char* ws = " \n\r\t";
    const auto first = s.find_first_not_of(ws);
    if (first == std::string::npos) return "";
    const auto last = s.find_last_not_of(ws);
    return s.substr(first, last - first + 1);
}

static std::string build_seed_command(const Seed::Config& cfg, const std::string& file_path) {
    std::stringstream cmd;
    cmd << quote_arg(cfg.seed_path)
        << " -file " << quote_arg(file_path)
        << " -port " << cfg.port;

    append_flag(cmd, "-relay", cfg.relay_addr);
    append_flag(cmd, "-config", cfg.config_path);

    if (!cfg.relay_assisted.empty()) {
        append_flag(cmd, "-relay-assisted", cfg.relay_assisted);
    }
    if (!cfg.transport_mode.empty() && cfg.transport_mode != "auto") {
        append_flag(cmd, "-transport", cfg.transport_mode);
    }
    if (cfg.hole_punch) {
        cmd << " -holepunch";
    }
    if (cfg.dht_enabled) {
        cmd << " -dht";
    }
    return cmd.str();
}

static bool read_line(Seed::ProcessHandle* handle, std::string& line) {
    line.clear();
    char ch = 0;
    for (;;) {
#ifdef _WIN32
        DWORD read = 0;
        BOOL ok = ReadFile(handle->stdout_read, &ch, 1, &read, nullptr);
        if (!ok || read == 0) return !line.empty();
#else
        ssize_t read = ::read(handle->stdout_fd, &ch, 1);
        if (read <= 0) return !line.empty();
#endif
        line.push_back(ch);
        if (ch == '\n') return true;
    }
}

static std::unique_ptr<Seed::ProcessHandle> start_seed_process(const std::string& command) {
    auto handle = std::make_unique<Seed::ProcessHandle>();

#ifdef _WIN32
    SECURITY_ATTRIBUTES sa{};
    sa.nLength = sizeof(sa);
    sa.bInheritHandle = TRUE;

    HANDLE read_pipe = nullptr;
    HANDLE write_pipe = nullptr;
    if (!CreatePipe(&read_pipe, &write_pipe, &sa, 0)) {
        throw std::runtime_error("CreatePipe failed");
    }
    SetHandleInformation(read_pipe, HANDLE_FLAG_INHERIT, 0);

    STARTUPINFOA si{};
    si.cb = sizeof(si);
    si.dwFlags = STARTF_USESTDHANDLES;
    si.hStdOutput = write_pipe;
    si.hStdError = write_pipe;
    si.hStdInput = GetStdHandle(STD_INPUT_HANDLE);

    std::string mutable_cmd = command;
    BOOL ok = CreateProcessA(
        nullptr,
        mutable_cmd.data(),
        nullptr,
        nullptr,
        TRUE,
        CREATE_NO_WINDOW,
        nullptr,
        nullptr,
        &si,
        &handle->pi
    );
    CloseHandle(write_pipe);
    if (!ok) {
        CloseHandle(read_pipe);
        throw std::runtime_error("CreateProcess failed");
    }
    handle->stdout_read = read_pipe;
#else
    int pipefd[2];
    if (pipe(pipefd) != 0) {
        throw std::runtime_error("pipe failed");
    }

    pid_t pid = fork();
    if (pid < 0) {
        close(pipefd[0]);
        close(pipefd[1]);
        throw std::runtime_error("fork failed");
    }
    if (pid == 0) {
        dup2(pipefd[1], STDOUT_FILENO);
        dup2(pipefd[1], STDERR_FILENO);
        close(pipefd[0]);
        close(pipefd[1]);
        execl("/bin/sh", "sh", "-c", command.c_str(), static_cast<char*>(nullptr));
        _exit(127);
    }
    close(pipefd[1]);
    handle->pid = pid;
    handle->stdout_fd = pipefd[0];
#endif
    return handle;
}

static void stop_seed_process(Seed::ProcessHandle* handle) {
    if (!handle) return;
    handle->stopping = true;

#ifdef _WIN32
    if (handle->pi.hProcess) {
        TerminateProcess(handle->pi.hProcess, 0);
        WaitForSingleObject(handle->pi.hProcess, 3000);
    }
    if (handle->stdout_read) CloseHandle(handle->stdout_read);
    if (handle->pi.hThread) CloseHandle(handle->pi.hThread);
    if (handle->pi.hProcess) CloseHandle(handle->pi.hProcess);
    handle->stdout_read = nullptr;
    handle->pi = {};
#else
    if (handle->pid > 0) {
        kill(handle->pid, SIGTERM);
        int status = 0;
        waitpid(handle->pid, &status, 0);
    }
    if (handle->stdout_fd >= 0) close(handle->stdout_fd);
    handle->stdout_fd = -1;
    handle->pid = -1;
#endif
    if (handle->drain_thread.joinable()) {
        handle->drain_thread.join();
    }
}

Seed::Seed(const Config& config) : config_(config) {
}

Seed::~Seed() {
    stop_seed_process(process_.get());
}

std::string Seed::publish(const std::string& file_path) {
    if (process_) {
        throw std::runtime_error("this Seed instance is already publishing a file");
    }

    const std::string command = build_seed_command(config_, file_path);
    std::cout << "[CPP-SDK] Starting seed: " << command << std::endl;

    process_ = start_seed_process(command);

    std::string magnet_link;
    const auto deadline = std::chrono::steady_clock::now() + std::chrono::seconds(60);
    while (std::chrono::steady_clock::now() < deadline) {
        std::string line;
        if (!read_line(process_.get(), line)) {
            break;
        }
        std::cout << line;
        const auto pos = line.find("Magnetk Link:");
        if (pos != std::string::npos) {
            magnet_link = trim(line.substr(pos + 13));
            break;
        }
    }

    if (magnet_link.empty()) {
        stop_seed_process(process_.get());
        process_.reset();
        throw std::runtime_error("failed to extract Magnetk Link from seeder output");
    }

    process_->drain_thread = std::thread([handle = process_.get()]() {
        std::string line;
        while (!handle->stopping && read_line(handle, line)) {
            std::cout << line;
        }
    });

    return magnet_link;
}

} // namespace magnetk
