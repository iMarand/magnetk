package hrtime

import (
	"testing"
	"time"
)

func TestResolution(t *testing.T) {
	// The clock must resolve a goroutine round trip (a few microseconds).
	zero := 0
	ch := make(chan struct{})
	for i := 0; i < 200; i++ {
		s := Now()
		go func() { ch <- struct{}{} }()
		<-ch
		if Since(s) <= 0 {
			zero++
		}
	}
	if zero > 10 {
		t.Fatalf("%d/200 round trips measured as zero", zero)
	}
}

func TestMatchesWallClock(t *testing.T) {
	s, w := Now(), time.Now()
	time.Sleep(50 * time.Millisecond)
	d, wd := Since(s), time.Since(w)
	if diff := d - wd; diff > 20*time.Millisecond || diff < -20*time.Millisecond {
		t.Fatalf("hrtime %v vs time %v", d, wd)
	}
}
