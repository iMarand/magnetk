package cli

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/iMarand/magnetk/go/pkg/diag"
	"github.com/iMarand/magnetk/go/pkg/netx"
)

// DiagMain runs the field-test host or client.
//
//	magnetk diag host   [-listen 0.0.0.0:4002] [-relay vps:4002] [-token T]
//	magnetk diag client -relay host:4002 [-tests all] [-repeat N]
func DiagMain(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: magnetk diag host|client [flags]")
	}
	switch args[0] {
	case "host":
		return diagHost(args[1:])
	case "client":
		return diagClient(args[1:])
	}
	return fmt.Errorf("unknown diag mode %q (host or client)", args[0])
}

func diagHost(args []string) error {
	fs := flag.NewFlagSet("diag host", flag.ContinueOnError)
	listen := fs.String("listen", "0.0.0.0:4002", "embedded relay address (\"\" = no embedded relay, use -relay)")
	relays := fs.String("relay", "", "relay(s) to connect to (without -listen) or to federate with (with -listen)")
	secret := fs.String("secret", "", "cluster secret for federation")
	token := fs.String("token", "", "relay auth token (embedded relay requires it; clients must use it)")
	id := fs.String("id", diag.DefaultHostID, "host node id")
	transport := fs.String("transport", "auto", "host node transport: auto | quic | tcp")
	direct := fs.String("direct", "0.0.0.0:4012", "peer-to-peer listen address (\"-\" = off)")
	advertise := fs.String("advertise", "", "public host:port(s) to advertise for direct pipes")
	fileMB := fs.Int("file-mb", 32, "size of the shared test file in MiB (0 = none)")
	dir := fs.String("dir", "diag-data", "directory for logs and the test file")
	httpAddr := fs.String("http", "127.0.0.1:8090", "HTTP API address (\"-\" = off)")
	verbose := fs.Bool("v", false, "print engine logs")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg := diag.HostConfig{
		ID: *id, RelayListen: *listen, Relays: splitList(*relays), ClusterSecret: *secret, Token: *token,
		Transport: netx.ParseMode(*transport), DirectListen: *direct, Advertise: splitList(*advertise),
		FileSize: int64(*fileMB) << 20, DataDir: *dir, HTTP: *httpAddr,
	}
	if *fileMB == 0 {
		cfg.FileSize = -1
	}
	if *verbose {
		cfg.Logger = log.New(os.Stderr, "", log.Ltime|log.Lmicroseconds)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	h, err := diag.StartHost(ctx, cfg)
	cancel()
	if err != nil {
		return err
	}
	defer h.Close()

	fmt.Printf("Magnetk diagnostics host %q is up.\n", *id)
	if p := h.RelayPort(); p != 0 {
		fmt.Printf("  embedded relay on port %d (QUIC/UDP + TCP)\n", p)
		for _, a := range diag.LocalAddrs() {
			fmt.Printf("  phone relay address:  %s:%d\n", a, p)
		}
		if fp := h.RelayCert(); fp != "" {
			fmt.Printf("  relay TCP certificate sha256: %s\n", fp)
		}
	} else {
		fmt.Printf("  connected via relay %v\n", cfg.Relays)
	}
	if *token != "" {
		fmt.Printf("  token required: %s\n", *token)
	}
	if *httpAddr != "-" {
		fmt.Printf("  HTTP API: http://%s/{clients,logs,results,status,cmd?client=all&op=run&tests=all}\n", *httpAddr)
	}
	fmt.Printf("  logs: %s/logs/<client>.jsonl\n", *dir)
	fmt.Println("Type: run [tests] | monitor on|off | stop | info | reconnect   (sent to all clients)")

	go func() {
		buf := make([]byte, 4096)
		for {
			k, err := os.Stdin.Read(buf)
			if err != nil {
				return
			}
			for _, line := range strings.Split(strings.TrimSpace(string(buf[:k])), "\n") {
				f := strings.Fields(line)
				if len(f) == 0 {
					continue
				}
				cmd := diag.Command{Op: f[0]}
				switch f[0] {
				case "run":
					if len(f) > 1 {
						cmd.Tests = strings.Split(f[1], ",")
					}
				case "monitor":
					cmd.On = len(f) < 2 || f[1] == "on"
				}
				c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				if err := h.SendCommand(c, "all", cmd); err != nil {
					fmt.Println("command failed:", err)
				}
				cancel()
			}
		}
	}()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	<-sig
	return nil
}

func diagClient(args []string) error {
	fs := flag.NewFlagSet("diag client", flag.ContinueOnError)
	relays := fs.String("relay", "127.0.0.1:4002", "relay address(es)")
	host := fs.String("host", diag.DefaultHostID, "diagnostics host node id")
	id := fs.String("id", "", "client node id")
	token := fs.String("token", "", "relay auth token")
	transport := fs.String("transport", "auto", "auto | quic | tcp")
	tests := fs.String("tests", "all", "comma separated tests: "+strings.Join(diag.AllTests, ","))
	repeat := fs.Int("repeat", 1, "rounds")
	pipeMB := fs.Int("pipe-mb", 16, "MiB per pipe test")
	monitor := fs.Duration("monitor", 0, "after the tests, monitor the connection for this long")
	wait := fs.Bool("wait", false, "stay connected and wait for commands from the host")
	dir := fs.String("dir", "", "directory for the client log file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	hostname, _ := os.Hostname()
	c, err := diag.NewClient(diag.ClientConfig{
		ID: *id, Relays: splitList(*relays), HostID: *host, Token: *token, Transport: netx.ParseMode(*transport),
		Device: "magnetk-cli on " + hostname, DataDir: *dir, PipeMB: *pipeMB,
		OnLog: func(l diag.LogLine) {
			if l.Level != "DEBUG" {
				fmt.Printf("%s %-6s %s\n", time.UnixMilli(l.T).Format("15:04:05.000"), l.Level, l.Msg)
			}
		},
	})
	if err != nil {
		return err
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	err = c.Start(ctx)
	cancel()
	if err != nil {
		return err
	}
	c.NetworkChanged("pc")
	failed := 0
	if *tests != "" && *tests != "none" {
		for _, r := range c.Run(splitList(*tests), *repeat) {
			if !r.OK {
				failed++
			}
		}
	}
	if *monitor > 0 {
		c.Monitor(true)
		time.Sleep(*monitor)
		c.Monitor(false)
	}
	if *wait {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt)
		<-sig
	}
	if failed > 0 {
		return fmt.Errorf("%d test(s) failed", failed)
	}
	return nil
}
