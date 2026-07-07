package selfupdate

import (
	"testing"
	"time"
)

func TestJitter_Range(t *testing.T) {
	interval := 10 * time.Minute
	// 抖动区间应为 [interval, interval + interval/2)
	for i := 0; i < 100; i++ {
		got := jitter(interval)
		if got < interval || got >= interval+interval/2 {
			t.Errorf("jitter(%s) = %s, want [%s, %s)", interval, got, interval, interval+interval/2)
		}
	}
}

func TestJitter_TinyInterval(t *testing.T) {
	// interval/2 = 0 时应回退为原 interval，不 panic
	got := jitter(1 * time.Nanosecond)
	if got != 1*time.Nanosecond {
		t.Errorf("got %s, want 1ns", got)
	}
}
