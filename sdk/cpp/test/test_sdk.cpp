// End-to-end test of the C++ SDK against an embedded relay.
#include <magnetk.hpp>

#include <atomic>
#include <cstdio>
#include <fstream>
#include <iostream>
#include <random>
#include <sstream>
#include <thread>

static int failures = 0;
#define CHECK(cond, what)                                                          \
    do {                                                                           \
        if (cond) std::cout << "ok   " << what << "\n";                            \
        else { std::cout << "FAIL " << what << " (line " << __LINE__ << ")\n"; failures++; } \
    } while (0)

template <class E, class F>
static bool throws(F&& f) {
    try { f(); } catch (const E&) { return true; } catch (...) { return false; }
    return false;
}

int main() {
    try {
        std::cout << "libmagnetk " << magnetk::version() << ", QUIC " << (magnetk::quic_available() ? "yes" : "no") << "\n";
        magnetk::Relay relay;

        magnetk::NodeOptions oa;
        oa.relays = {relay.address()};
        oa.id = "cpp-alice";
        oa.cache_dir = "-";
        magnetk::Node alice(oa);

        magnetk::NodeOptions ob = oa;
        ob.id = "cpp-bob";
        ob.listen = true;
        ob.listen_addr = "127.0.0.1:0";
        magnetk::Node bob(ob);
        CHECK(bob.id() == "cpp-bob", "node id");

        // direct message with receipt (binary-safe payload)
        std::string bin("a\0b\xff", 4);
        alice.send("cpp-bob", bin, magnetk::Receipt);
        auto m = bob.receive(5000);
        CHECK(m && m->kind == magnetk::Kind::Direct && m->from == "cpp-alice" && m->data == bin, "binary direct message");
        CHECK(throws<magnetk::NoRouteError>([&] { alice.send("nobody", "x", magnetk::Receipt); }), "NoRouteError");

        // pub/sub
        bob.subscribe("orders.>");
        alice.publish("orders.eu.new", "order-1");
        m = bob.receive(5000);
        CHECK(m && m->kind == magnetk::Kind::Publish && m->subject == "orders.eu.new" && m->data == "order-1", "publication");
        bob.unsubscribe("orders.>");

        // request/reply with a dispatcher thread on bob
        bob.serve("svc.reverse");
        bob.on_message([](const magnetk::Message& msg) -> std::optional<std::string> {
            if (!msg.is_request()) return std::nullopt;
            if (msg.data == "fail") throw std::runtime_error("asked to fail");
            return std::string(msg.data.rbegin(), msg.data.rend());
        });
        CHECK(alice.request_topic("svc.reverse", "abc") == "cba", "request to topic");
        CHECK(alice.request("cpp-bob", "xyz") == "zyx", "request to node");
        bool remote = false;
        try { alice.request("cpp-bob", "fail"); } catch (const magnetk::RemoteError& e) { remote = std::string(e.what()).find("asked to fail") != std::string::npos; }
        CHECK(remote, "RemoteError carries the handler message");
        bob.on_message(nullptr);  // replaced below: stop dispatcher
        bob.close();

        // pipes (bob restarted without dispatcher so accept_pipe can be used)
        magnetk::Node bob2(ob);
        bob2.accept_pipe(0);  // enable accepting
        for (const char* route : {"relay", "direct"}) {
            std::string payload(2 * 1024 * 1024 + 7, '\0');
            std::mt19937 rng(42);
            for (auto& c : payload) c = static_cast<char>(rng());
            magnetk::Pipe p = alice.open_pipe("cpp-bob", "echo", "m", route);
            auto q = bob2.accept_pipe(5000);
            CHECK(q && q->peer == "cpp-alice" && q->service == "echo" && q->meta == "m", std::string("accept pipe via ") + route);
            CHECK(p.direct == (std::string(route) == "direct"), std::string("pipe path is ") + route);
            std::thread echo([&] { std::string all = q->read_all(); q->write(all); q->close_write(); });
            std::thread writer([&] { p.write(payload); p.close_write(); });
            std::string back = p.read_all(10000);
            writer.join();
            echo.join();
            CHECK(back == payload, std::string("pipe echo 2 MB via ") + route);
        }

        // files
        const char* src = "cpp_sdk_test.bin";
        {
            std::ofstream f(src, std::ios::binary);
            std::mt19937 rng(7);
            for (int i = 0; i < 3 * 1024 * 1024; i++) f.put(static_cast<char>(rng()));
        }
        auto shared = bob2.share(src);
        CHECK(shared.size == 3 * 1024 * 1024 && shared.link.rfind("magnetk:?", 0) == 0, "share");
        int updates = 0;
        auto res = alice.download(shared.link, "cpp_sdk_out.bin", [&](const magnetk::Progress&) { updates++; });
        std::ifstream a(src, std::ios::binary), b(res.path, std::ios::binary);
        std::stringstream sa, sb;
        sa << a.rdbuf();
        sb << b.rdbuf();
        CHECK(res.hash == shared.hash && sa.str() == sb.str() && updates > 0, "download verified");
        a.close();
        b.close();
        CHECK(alice.providers_json(shared.hash).find(bob2.id()) != std::string::npos, "find providers");
        bob2.unshare(shared.hash);
        std::remove(src);
        std::remove("cpp_sdk_out.bin");

        CHECK(alice.ping_us() > 0, "ping");
        CHECK(relay.stats_json().find("\"nodes\":2") != std::string::npos, "relay stats");

        // auth
        magnetk::RelayOptions ro;
        ro.tokens = {"s3cret"};
        magnetk::Relay locked(ro);
        magnetk::NodeOptions bad;
        bad.relays = {locked.address()};
        bad.token = "wrong";
        CHECK(throws<magnetk::UnauthorizedError>([&] { magnetk::Node n(bad); }), "UnauthorizedError");
    } catch (const magnetk::Error& e) {
        std::cout << "UNEXPECTED magnetk::Error " << e.code() << ": " << e.what() << "\n";
        return 2;
    }
    if (failures) {
        std::cout << failures << " FAILURE(S)\n";
        return 1;
    }
    std::cout << "ALL C++ SDK TESTS PASSED\n";
    return 0;
}
