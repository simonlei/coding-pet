package selfupdate

import (
	"log"
	"math/rand"
	"os"
	"time"
)

// DefaultInterval 是自动更新检查的默认基础间隔。
const DefaultInterval = 30 * time.Minute

// StartAuto 启动一个后台 goroutine，按 interval + 随机抖动定时调用 CheckAndUpdate。
// 抖动为 [0, interval/2)，避免多设备同刻打 GitHub API（KTD8）。
// 若 opts.Kind 为空，退化为 no-op（记录一条告警）。返回停止函数。
func StartAuto(opts Options, interval time.Duration) func() {
	if opts.Kind == "" {
		log.Printf("selfupdate: auto-update disabled (no kind resolved)")
		return func() {}
	}
	if interval <= 0 {
		interval = DefaultInterval
	}
	// 首次触发前也加一次抖动，避免所有实例启动即打 API
	delay := jitter(interval)
	auth := "anonymous (60/hr limit)"
	if os.Getenv("GITHUB_TOKEN") != "" {
		auth = "GITHUB_TOKEN present (5000/hr limit)"
	}
	log.Printf("selfupdate: auto-update enabled (kind=%s, current=%s, interval=%s, auth=%s), first check in %s",
		opts.Kind, opts.CurrentVersion, interval, auth, delay)
	stop := make(chan struct{})
	go func() {
		select {
		case <-time.After(delay):
		case <-stop:
			return
		}
		for {
			log.Printf("selfupdate: periodic check starting (kind=%s, local=%s)", opts.Kind, opts.CurrentVersion)
			if _, err := CheckAndUpdate(opts); err != nil {
				log.Printf("selfupdate: check failed (will retry next interval): %v", err)
			}
			next := jitter(interval)
			log.Printf("selfupdate: next check in %s", next)
			select {
			case <-time.After(next):
			case <-stop:
				return
			}
		}
	}()
	return func() { close(stop) }
}

// jitter 返回 interval + rand(0..interval/2)。
func jitter(interval time.Duration) time.Duration {
	half := int64(interval / 2)
	if half <= 0 {
		return interval
	}
	//nolint:gosec // 非安全用途；仅用于打散调度
	return interval + time.Duration(rand.Int63n(half))
}
