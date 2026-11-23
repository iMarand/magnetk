//go:build !windows

package hrtime

import "time"

var origin = time.Now()

func now() int64 { return int64(time.Since(origin)) }
