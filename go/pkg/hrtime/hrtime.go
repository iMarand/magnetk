// Package hrtime is a monotonic clock with sub-microsecond resolution for
// latency measurements.
//
// time.Now on Windows advances in coarse steps (about 0.5 ms, up to
// 15.6 ms, depending on the system timer resolution) because it reads the
// interrupt-time counter, so loopback and LAN round trips often measure as
// zero. On Windows this package reads QueryPerformanceCounter instead; on
// other systems it uses Go's monotonic clock, which is already precise.
package hrtime

import "time"

// Time is a point on the high-resolution monotonic clock, in nanoseconds
// since an arbitrary origin. Only differences are meaningful.
type Time int64

// Now returns the current high-resolution time.
func Now() Time { return Time(now()) }

// Since returns the time elapsed since t.
func Since(t Time) time.Duration { return time.Duration(now() - int64(t)) }

// Sub returns t - u.
func (t Time) Sub(u Time) time.Duration { return time.Duration(t - u) }
