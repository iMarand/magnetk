# Magnetk for Python

Fast messaging, pub/sub, request/reply, byte pipes and verified
multi-source file transfer over QUIC (TCP fallback), through one or more
relays and peer-to-peer whenever peers can reach each other.

```python
import magnetk

node = magnetk.Node("relay.example.com:4002", id="alice")

node.subscribe("chat.>", lambda m: print(m.sender, m.text))
node.publish("chat.general", "hello")

node.serve("svc.echo", lambda m: m.data)          # request handler
print(node.request_topic("svc.echo", "ping"))

shared = node.share("big.iso")                     # serve a file
print(shared.link)                                 # give this to anyone
# elsewhere: magnetk.Node(...).download(link, "downloads/")
```

* Native engine (`libmagnetk`) via ctypes — no subprocesses, no compiler.
* Failover between relays, automatic reconnect, presence, offline storage,
  delivery receipts, unreliable datagrams, queue groups.
* Downloads from many providers in parallel; every chunk verified;
  interrupted downloads resume.

Install from the repository after building the native library:

```bash
scripts/build.sh            # Windows: scripts\build.bat
pip install ./sdk/python
```

Full guide: [Documentation.md](Documentation.md). Examples: `examples/`.
Run a relay: `magnetk relay` ([deployment guide](../../docs/RELAY_DEPLOYMENT.md)).
