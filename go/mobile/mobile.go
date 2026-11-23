// Command mobile builds libmagnetkdiag.so, the native half of the Android
// field-test app (android/). It wraps the diag.Client test suite behind a
// handful of C functions that jni.c exposes to Java.
//
//	GOOS=android GOARCH=arm64 CGO_ENABLED=1 CC="<ndk clang> --target=aarch64-linux-android26" \
//	  go build -buildmode=c-shared -tags nomsquic -o libmagnetkdiag.so ./mobile
//
// (android/build.py does all of this.)
package main

/*
#include <stdlib.h>
*/
import "C"

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
	"unsafe"

	"github.com/iMarand/magnetk/go/pkg/diag"
	"github.com/iMarand/magnetk/go/pkg/netx"
)

func main() {}

type startConfig struct {
	Relays    []string `json:"relays"`
	Host      string   `json:"host"`
	Token     string   `json:"token"`
	ID        string   `json:"id"`
	Transport string   `json:"transport"`
	Device    string   `json:"device"`
	DataDir   string   `json:"data_dir"`
	PipeMB    int      `json:"pipe_mb"`
	Network   string   `json:"network"`
	TZName    string   `json:"tz_name"`
	TZOffset  int      `json:"tz_offset_sec"`
	CacheDir  string   `json:"cache_dir"`
}

var (
	mu      sync.Mutex
	client  *diag.Client
	lines   = make(chan string, 20000)
	network = "unknown"
)

func pushLine(s string) {
	select {
	case lines <- s:
	default: // UI not polling: drop the oldest
		select {
		case <-lines:
		default:
		}
		select {
		case lines <- s:
		default:
		}
	}
}

func onLog(l diag.LogLine) {
	if l.Level == "DEBUG" && !strings.Contains(l.Msg, "[node]") {
		return
	}
	pushLine(fmt.Sprintf("%s %-6s %s", time.UnixMilli(l.T).Format("15:04:05.000"), l.Level, l.Msg))
}

// jniSafe makes s acceptable to JNI NewStringUTF (modified UTF-8): valid
// UTF-8, no NUL, no characters outside the BMP.
func jniSafe(s string) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "?")
	}
	return strings.Map(func(r rune) rune {
		if r == 0 || r > 0xFFFF {
			return '?'
		}
		return r
	}, s)
}

func cstr(s string) *C.char { return C.CString(jniSafe(s)) }

//export mkdStart
func mkdStart(cfgJSON *C.char) *C.char {
	var cfg startConfig
	if err := json.Unmarshal([]byte(C.GoString(cfgJSON)), &cfg); err != nil {
		return cstr("bad config: " + err.Error())
	}
	if cfg.CacheDir != "" {
		// os.TempDir() is /data/local/tmp on Android, which apps cannot write.
		os.Setenv("TMPDIR", cfg.CacheDir)
		os.Setenv("MAGNETK_CERT_DIR", cfg.CacheDir)
	}
	if cfg.TZName != "" || cfg.TZOffset != 0 {
		// Go cannot read Android's time zone database location; use the
		// offset the app reports so log times match the phone's clock.
		time.Local = time.FixedZone(cfg.TZName, cfg.TZOffset)
	}
	mu.Lock()
	old := client
	client = nil
	mu.Unlock()
	if old != nil {
		old.Close()
	}
	c, err := diag.NewClient(diag.ClientConfig{
		ID: cfg.ID, Relays: cfg.Relays, HostID: cfg.Host, Token: cfg.Token,
		Transport: netx.ParseMode(cfg.Transport), Device: cfg.Device, DataDir: cfg.DataDir,
		PipeMB: cfg.PipeMB, OnLog: onLog,
	})
	if err != nil {
		return cstr(err.Error())
	}
	mu.Lock()
	client = c
	if cfg.Network != "" {
		network = cfg.Network
	}
	nw := network
	mu.Unlock()
	c.NetworkChanged(nw)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		c.Start(ctx) // keeps retrying in the background on failure
	}()
	return nil
}

func current() *diag.Client {
	mu.Lock()
	defer mu.Unlock()
	return client
}

//export mkdPoll
func mkdPoll(timeoutMs C.int) *C.char {
	var batch []string
	select {
	case l := <-lines:
		batch = append(batch, l)
	case <-time.After(time.Duration(timeoutMs) * time.Millisecond):
		return nil
	}
	for len(batch) < 500 {
		select {
		case l := <-lines:
			batch = append(batch, l)
			continue
		default:
		}
		break
	}
	return cstr(strings.Join(batch, "\n"))
}

//export mkdRun
func mkdRun(tests *C.char, repeat C.int) {
	if c := current(); c != nil {
		var list []string
		for _, t := range strings.Split(C.GoString(tests), ",") {
			if t = strings.TrimSpace(t); t != "" {
				list = append(list, t)
			}
		}
		c.RunAsync(list, int(repeat))
	} else {
		pushLine("not connected: press Connect first")
	}
}

//export mkdStopTests
func mkdStopTests() {
	if c := current(); c != nil {
		c.StopTests()
	}
}

//export mkdMonitor
func mkdMonitor(on C.int) {
	if c := current(); c != nil {
		c.Monitor(on != 0)
	}
}

//export mkdNetwork
func mkdNetwork(desc *C.char) {
	d := C.GoString(desc)
	mu.Lock()
	network = d
	c := client
	mu.Unlock()
	if c != nil {
		c.NetworkChanged(d)
	}
}

//export mkdLog
func mkdLog(level, msg *C.char) {
	l := diag.LogLine{T: time.Now().UnixMilli(), Level: C.GoString(level), Msg: C.GoString(msg)}
	if c := current(); c != nil {
		c.Log(l.Level, l.Msg)
	} else {
		onLog(l)
	}
}

//export mkdStatus
func mkdStatus() *C.char {
	c := current()
	if c == nil {
		return cstr("not started")
	}
	return cstr(c.ID() + " · " + c.Status())
}

//export mkdClose
func mkdClose() {
	mu.Lock()
	c := client
	client = nil
	mu.Unlock()
	if c != nil {
		c.Close()
	}
}

//export mkdFree
func mkdFree(p *C.char) { C.free(unsafe.Pointer(p)) }
