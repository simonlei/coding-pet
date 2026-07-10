package notifier

import (
	"net/url"
	"strings"
)

// RedactURL 返回 kind + URL 的脱敏字符串，用于日志。规则：
//   - 无法解析 → "<kind>://<invalid-url>"。
//   - 有 `key=` query（企微 / 类似 webhook 常见 secret 位置）→ 保留 host+path，
//     key 只保留前 4 位并追加 ****。其他 query 全部丢弃避免误当 secret。
//   - 无 key= query → 保留 host + 前 40 字符 path，超出部分以 "..." 截断。
func RedactURL(kind, rawURL string) string {
	if rawURL == "" {
		return kind + "://<invalid-url>"
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return kind + "://<invalid-url>"
	}
	host := u.Host
	path := u.Path
	q := u.Query()

	if key := q.Get("key"); key != "" {
		masked := "****"
		if len(key) >= 4 {
			masked = key[:4] + "****"
		}
		return kind + "://" + host + path + "?key=" + masked
	}
	// 无 key：截断 path
	const maxPath = 40
	trunc := path
	suffix := ""
	if len(trunc) > maxPath {
		trunc = trunc[:maxPath]
		suffix = "..."
	}
	// 如果原 URL 有 query 但没 key，也一并丢弃（不好判断哪个字段是 secret）。
	out := kind + "://" + host + trunc + suffix
	if strings.HasSuffix(out, "...") {
		return out
	}
	// 若原 URL 有 query 但被丢弃，加个 "..." 提示信息被截断。
	if u.RawQuery != "" {
		out += "..."
	}
	return out
}
