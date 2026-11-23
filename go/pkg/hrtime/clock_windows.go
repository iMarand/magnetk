//go:build windows

package hrtime

import (
	"syscall"
	"time"
	"unsafe"
)

var (
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	procQPC  = kernel32.NewProc("QueryPerformanceCounter")
	procQPF  = kernel32.NewProc("QueryPerformanceFrequency")
	qpcFreq  int64
	origin   = time.Now()
)

func init() {
	if procQPF.Find() != nil || procQPC.Find() != nil {
		return
	}
	procQPF.Call(uintptr(unsafe.Pointer(&qpcFreq)))
}

func now() int64 {
	if qpcFreq <= 0 {
		return int64(time.Since(origin))
	}
	var c int64
	procQPC.Call(uintptr(unsafe.Pointer(&c)))
	// split to avoid overflowing c*1e9 after a few minutes of uptime
	return c/qpcFreq*1e9 + c%qpcFreq*1e9/qpcFreq
}
