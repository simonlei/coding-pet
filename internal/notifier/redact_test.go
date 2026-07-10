package notifier

import (
	"net/url"
	"strings"
	"testing"
)

func TestRedactURL_WithKey(t *testing.T) {
	got := RedactURL("wechat_work", "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=abcdef1234-verylong-secret")
	if !strings.HasPrefix(got, "wechat_work://") {
		t.Errorf("missing kind prefix: %q", got)
	}
	if !strings.Contains(got, "qyapi.weixin.qq.com") {
		t.Errorf("missing host: %q", got)
	}
	if !strings.Contains(got, "key=abcd****") {
		t.Errorf("expected key=abcd****, got %q", got)
	}
	if strings.Contains(got, "verylong-secret") {
		t.Errorf("secret leaked into output: %q", got)
	}
}

func TestRedactURL_NoKey(t *testing.T) {
	got := RedactURL("wechat_work", "https://example.com/hooks/abcdefghijklmnopqrstuvwxyz1234567890extra-more-here")
	if !strings.HasPrefix(got, "wechat_work://example.com") {
		t.Errorf("host missing: %q", got)
	}
	if !strings.HasSuffix(got, "...") {
		t.Errorf("expected trailing ...: %q", got)
	}
	if strings.Contains(got, "extra-more-here") {
		t.Errorf("tail leaked: %q", got)
	}
}

func TestRedactURL_InvalidURL(t *testing.T) {
	got := RedactURL("wechat_work", ":::not a url:::")
	if got != "wechat_work://<invalid-url>" {
		t.Errorf("got %q", got)
	}
}

func TestRedactURL_ShortKey(t *testing.T) {
	// key 少于 4 位：全部替换成 ****
	got := RedactURL("wechat_work", "https://q.example.com/w/send?key=ab")
	// URL query 会保留 key= 但对 <4 位 key 直接全打码
	if !strings.Contains(got, "key=****") {
		t.Errorf("short key should be fully masked: %q", got)
	}
}

func TestRedactURL_MultipleParams(t *testing.T) {
	got := RedactURL("wechat_work", "https://q.example.com/w?key=abcdef1234&extra=hello")
	if !strings.Contains(got, "key=abcd****") {
		t.Errorf("key not truncated: %q", got)
	}
	// 其他 query 也一并被截断到 key 后不再出现（避免误当 secret）
	if strings.Contains(got, "extra=hello") {
		t.Errorf("non-key query leaked: %q", got)
	}
}

// TestRedactURL_ParseableSanityCheck 保证 URL 是可解析的（供其他测试参考）。
func TestRedactURL_ParseableSanityCheck(t *testing.T) {
	u, err := url.Parse("https://a.com/x?key=1234abcd")
	if err != nil {
		t.Fatal(err)
	}
	if u.Query().Get("key") != "1234abcd" {
		t.Fatal("query mismatch")
	}
}
