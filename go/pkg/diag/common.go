// Package diag is Magnetk's field-test kit: a Host that runs on a PC (an
// optional embedded relay, echo/sink/source/file test services and a log
// collector) and a Client test suite that runs on another device, e.g. the
// Android test app, and reports every log line and result back to the host.
//
// All traffic goes through the normal Magnetk APIs (messages, requests,
// pub/sub, datagrams, pipes, file transfer), so a test run exercises the
// real protocol end to end: same Wi-Fi, mobile data through a VPS relay,
// network switches, and so on.
package diag

import (
	"encoding/json"
	"math"
	"sort"
	"time"
)

// Defaults and topic names shared by host and client.
const (
	DefaultHostID = "diag-host"

	TopicLog    = "diag.log"    // diag.log.<client>: JSON array of LogLine
	TopicResult = "diag.result" // diag.result.<client>: Result
	TopicCmd    = "diag.cmd"    // diag.cmd.<client> or diag.cmd.all: Command

	ServiceSink   = "diag.sink"   // client -> host bytes; host answers sha256
	ServiceSource = "diag.source" // host -> client bytes + sha256 trailer
	ServiceEcho   = "diag.echo"   // full-duplex echo
)

func ctlTopic(host string) string  { return "diag." + host + ".ctl" }
func echoTopic(host string) string { return "diag." + host + ".echo" }

// LogLine is one client log entry.
type LogLine struct {
	T     int64  `json:"t"` // unix ms
	Level string `json:"lvl"`
	Msg   string `json:"msg"`
	Seq   uint64 `json:"seq"`
}

// Result is the outcome of one test.
type Result struct {
	Test     string         `json:"test"`
	OK       bool           `json:"ok"`
	Error    string         `json:"error,omitempty"`
	Ms       float64        `json:"ms"`
	Metrics  map[string]any `json:"metrics,omitempty"`
	Client   string         `json:"client"`
	Network  string         `json:"network,omitempty"`
	Protocol string         `json:"protocol,omitempty"`
	Time     int64          `json:"time"` // unix ms
}

// Command is sent by the host (or its HTTP API) to clients.
type Command struct {
	Op     string   `json:"op"` // run, stop, monitor, info, reconnect
	Tests  []string `json:"tests,omitempty"`
	Repeat int      `json:"repeat,omitempty"`
	On     bool     `json:"on,omitempty"`
	PipeMB int      `json:"pipe_mb,omitempty"`
}

// ctlReq / ctlResp are the control requests a client sends to the host.
type ctlReq struct {
	Op    string `json:"op"` // hello, bulk_start, bulk_result, fanout, file
	N     int    `json:"n,omitempty"`
	Size  int    `json:"size,omitempty"`
	Topic string `json:"topic,omitempty"`
	Info  string `json:"info,omitempty"`
}

type hostHello struct {
	Host       string   `json:"host"`
	Version    string   `json:"version"`
	TimeNs     int64    `json:"time_ns"`
	FileLink   string   `json:"file_link,omitempty"`
	FileSize   int64    `json:"file_size,omitempty"`
	RelayCert  string   `json:"relay_cert,omitempty"` // TCP certificate fingerprint of the embedded relay
	RelayToken bool     `json:"relay_token"`          // embedded relay requires a token
	Direct     []string `json:"direct,omitempty"`
	QUIC       bool     `json:"quic"`
	Key        string   `json:"key,omitempty"`   // host identity fingerprint
	HTTPS      string   `json:"https,omitempty"` // ":port" of the HTTPS comparison server
	HTTPSCert  string   `json:"https_cert,omitempty"`
}

type bulkResult struct {
	Count      int     `json:"count"`
	Bytes      int64   `json:"bytes"`
	OutOfOrder int     `json:"out_of_order"`
	Ms         float64 `json:"ms"` // first to last message, host clock
}

type sinkResult struct {
	Bytes  int64   `json:"bytes"`
	SHA256 string  `json:"sha256"`
	Ms     float64 `json:"ms"`
}

type sourceReq struct {
	Bytes int64 `json:"bytes"`
}

// latency summarizes durations.
type latency struct {
	N      int     `json:"n"`
	Min    float64 `json:"min_ms"`
	P50    float64 `json:"p50_ms"`
	P90    float64 `json:"p90_ms"`
	P99    float64 `json:"p99_ms"`
	Max    float64 `json:"max_ms"`
	Mean   float64 `json:"mean_ms"`
	Jitter float64 `json:"jitter_ms"` // mean absolute difference between consecutive samples
}

func summarize(samples []time.Duration) latency {
	if len(samples) == 0 {
		return latency{}
	}
	ms := make([]float64, len(samples))
	var sum, jit float64
	for i, d := range samples {
		ms[i] = float64(d.Microseconds()) / 1000
		sum += ms[i]
		if i > 0 {
			jit += math.Abs(ms[i] - ms[i-1])
		}
	}
	sorted := append([]float64(nil), ms...)
	sort.Float64s(sorted)
	pct := func(p float64) float64 {
		i := int(math.Ceil(p*float64(len(sorted)))) - 1
		if i < 0 {
			i = 0
		}
		return round2(sorted[i])
	}
	l := latency{N: len(ms), Min: round2(sorted[0]), P50: pct(0.5), P90: pct(0.9), P99: pct(0.99), Max: round2(sorted[len(sorted)-1]), Mean: round2(sum / float64(len(ms)))}
	if len(ms) > 1 {
		l.Jitter = round2(jit / float64(len(ms)-1))
	}
	return l
}

func round2(f float64) float64 { return math.Round(f*100) / 100 }

func mbps(bytes int64, d time.Duration) float64 {
	if d <= 0 {
		return 0
	}
	return round2(float64(bytes) / d.Seconds() / 1e6)
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
