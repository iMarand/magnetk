// Room chat over publish/subscribe plus private messages.
//   chat <name> [relay host:port]
// Type a line for the room, "@name text" for a private message.
#include <magnetk.hpp>

#include <iostream>
#include <string>

int main(int argc, char** argv) {
    if (argc < 2) {
        std::cerr << "usage: chat <name> [relay host:port]\n";
        return 2;
    }
    try {
        magnetk::NodeOptions opts;
        opts.id = argv[1];
        opts.relays = {argc > 2 ? argv[2] : "127.0.0.1:4002"};
        opts.watch_presence = true;
        magnetk::Node node(opts);
        node.subscribe("chat.room");

        const std::string me = argv[1];
        node.on_message([&](const magnetk::Message& m) -> std::optional<std::string> {
            if (m.kind == magnetk::Kind::Presence)
                std::cout << "* " << m.from << (m.subject == "join" ? " joined" : " left") << "\n";
            else if (m.kind == magnetk::Kind::Publish && m.from != me)
                std::cout << "[room] " << m.from << ": " << m.data << "\n";
            else if (m.kind == magnetk::Kind::Direct)
                std::cout << "[private] " << m.from << ": " << m.data << "\n";
            return std::nullopt;
        });

        std::cout << "connected as " << node.id() << "\n";
        for (std::string line; std::getline(std::cin, line);) {
            if (line.empty()) continue;
            if (line[0] == '@' && line.find(' ') != std::string::npos) {
                auto sp = line.find(' ');
                try {
                    node.send(line.substr(1, sp - 1), line.substr(sp + 1), magnetk::Receipt | magnetk::Store);
                } catch (const magnetk::NoRouteError&) {
                    std::cout << "(offline)\n";
                }
            } else {
                node.publish("chat.room", line);
            }
        }
    } catch (const magnetk::Error& e) {
        std::cerr << "error: " << e.what() << "\n";
        return 1;
    }
    return 0;
}
