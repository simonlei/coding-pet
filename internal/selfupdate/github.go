package selfupdate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	// defaultAPIBase 是 GitHub REST API 根地址；测试时可覆盖。
	defaultAPIBase = "https://api.github.com"
	// repoSlug 是目标仓库 owner/repo。
	repoSlug = "simonlei/coding-pet"
	// userAgent GitHub API 要求必须携带 User-Agent，否则返回 403。
	userAgent = "coding-pet-selfupdate"
	// shaSumsAsset 是 release 中校验和清单的资产名。
	shaSumsAsset = "SHA256SUMS.txt"
	// httpTimeout 统一控制所有 HTTP 请求超时（release JSON、SHA256SUMS、归档下载）。
	// 跨境网络慢时连几百字节的 SHA256SUMS 都会撞 30s Client.Timeout；统一放宽到 30 分钟，
	// 让所有请求走同一条慢路径都能完成，靠 auto.go 的定时器控制重试节奏而非硬超时。
	httpTimeout = 30 * time.Minute
)

// Asset 对应 release 的一个可下载资产。
type Asset struct {
	Name        string `json:"name"`
	DownloadURL string `json:"browser_download_url"`
}

// Release 对应 GitHub /releases/latest 的响应（仅取用到的字段）。
type Release struct {
	TagName string  `json:"tag_name"`
	Assets  []Asset `json:"assets"`
}

// Client 封装对 GitHub release 的查询与下载，APIBase/HTTP 可注入以便测试。
type Client struct {
	APIBase string
	HTTP    *http.Client
	// Token 为可选的 GitHub 个人访问令牌。设置后限流从 60/hr 提升到 5000/hr。
	// 由 NewClient 从 GITHUB_TOKEN 环境变量读取；测试可显式覆盖或置空。
	Token string
}

// NewClient 返回使用默认 GitHub 地址与合理超时的 Client。
// 若环境变量 GITHUB_TOKEN 存在（非空），后续请求会带上 Authorization 头以规避匿名限流。
func NewClient() *Client {
	return &Client{
		APIBase: defaultAPIBase,
		HTTP:    &http.Client{Timeout: httpTimeout},
		Token:   strings.TrimSpace(os.Getenv("GITHUB_TOKEN")),
	}
}

// applyAuth 若配置了 Token 就给请求加上 Bearer 认证头。
func (c *Client) applyAuth(req *http.Request) {
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
}

// FetchLatest 拉取最新正式版 release（GitHub 自动跳过 prerelease/draft）。
func (c *Client) FetchLatest() (*Release, error) {
	url := fmt.Sprintf("%s/repos/%s/releases/latest", strings.TrimRight(c.APIBase, "/"), repoSlug)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent) // 必填，否则 GitHub 返回 403
	req.Header.Set("Accept", "application/vnd.github+json")
	c.applyAuth(req)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch latest release: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch latest release: %s", describeGitHubError(resp))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read release body: %w", err)
	}
	var rel Release
	if err := json.Unmarshal(body, &rel); err != nil {
		return nil, fmt.Errorf("parse release json: %w", err)
	}
	if rel.TagName == "" {
		return nil, fmt.Errorf("release has empty tag_name")
	}
	return &rel, nil
}

// archiveExt 返回当前平台的归档扩展名。
func archiveExt() string {
	if runtime.GOOS == "windows" {
		return "zip"
	}
	return "tar.gz"
}

// AssetName 计算当前平台/架构对应的归档资产名。
// 形如 coding-pet-<tag>-<goos>-<goarch>.<ext>（约定见 build.yml）。
func AssetName(tag string) string {
	return fmt.Sprintf("coding-pet-%s-%s-%s.%s", tag, runtime.GOOS, runtime.GOARCH, archiveExt())
}

// SelectAsset 从 release 中选出当前平台的归档资产与 SHA256SUMS.txt 资产。
func (r *Release) SelectAsset() (archive Asset, shaSums Asset, err error) {
	want := AssetName(r.TagName)
	var foundArchive, foundSha bool
	for _, a := range r.Assets {
		switch a.Name {
		case want:
			archive = a
			foundArchive = true
		case shaSumsAsset:
			shaSums = a
			foundSha = true
		}
	}
	if !foundArchive {
		return archive, shaSums, fmt.Errorf("no asset matching %q in release %s", want, r.TagName)
	}
	if !foundSha {
		return archive, shaSums, fmt.Errorf("no %s asset in release %s", shaSumsAsset, r.TagName)
	}
	return archive, shaSums, nil
}

// parseShaSums 解析 "sha256  filename" 逐行清单，返回 filename -> 期望 sha256。
func parseShaSums(data []byte) map[string]string {
	out := make(map[string]string)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		// 文件名可能带 "*" 前缀（二进制模式），去掉之。
		name := strings.TrimPrefix(fields[len(fields)-1], "*")
		out[name] = strings.ToLower(fields[0])
	}
	return out
}

// fetchBytes 下载 url 全部内容。
func (c *Client) fetchBytes(url string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	c.applyAuth(req)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// describeGitHubError 把 GitHub 非 200 响应格式化成信息丰富的字符串：
// 带上限流剩余/重置时间与响应体首行 message，便于运维定位（否则只看到 "HTTP 403"）。
func describeGitHubError(resp *http.Response) string {
	limit := resp.Header.Get("X-RateLimit-Limit")
	remaining := resp.Header.Get("X-RateLimit-Remaining")
	// 只读一小段 body，避免异常响应把日志撑爆。
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	var msg struct {
		Message          string `json:"message"`
		DocumentationURL string `json:"documentation_url"`
	}
	_ = json.Unmarshal(body, &msg)

	parts := []string{fmt.Sprintf("HTTP %d", resp.StatusCode)}
	if msg.Message != "" {
		parts = append(parts, msg.Message)
	}
	// 限流耗尽时给出明确提示与重置时间。
	if remaining == "0" && limit != "" {
		if reset := resp.Header.Get("X-RateLimit-Reset"); reset != "" {
			if ts, err := strconv.ParseInt(reset, 10, 64); err == nil {
				parts = append(parts, fmt.Sprintf("rate limit exhausted (%s/hr, resets at %s)",
					limit, time.Unix(ts, 0).Format(time.RFC3339)))
			}
		}
	}
	return strings.Join(parts, "; ")
}

// humanBytes 把字节数格式化成人可读字符串（1.2MB / 500KB）。
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%cB", float64(n)/float64(div), "KMGTPE"[exp])
}

// DownloadAndVerify 下载归档到 destDir 下的临时文件，并对照 SHA256SUMS.txt 校验。
// 校验失败或下载失败时删除临时文件并返回 error（安全回退）。
// 返回校验通过的临时归档文件路径。
func (c *Client) DownloadAndVerify(archive, shaSums Asset, destDir string) (string, error) {
	sumsData, err := c.fetchBytes(shaSums.DownloadURL)
	if err != nil {
		return "", fmt.Errorf("download %s: %w", shaSumsAsset, err)
	}
	sums := parseShaSums(sumsData)
	want, ok := sums[archive.Name]
	if !ok {
		return "", fmt.Errorf("%s missing entry for %q", shaSumsAsset, archive.Name)
	}

	// 临时文件建在目标目录（与最终二进制同分区，保证后续 rename 原子）。
	tmp, err := os.CreateTemp(destDir, "cp-update-*."+archiveExt())
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()

	req, err := http.NewRequest(http.MethodGet, archive.DownloadURL, nil)
	if err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return "", err
	}
	req.Header.Set("User-Agent", userAgent)
	c.applyAuth(req)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return "", fmt.Errorf("download archive: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		tmp.Close()
		os.Remove(tmpPath)
		return "", fmt.Errorf("download archive: %s", describeGitHubError(resp))
	}
	if resp.ContentLength > 0 {
		log.Printf("selfupdate: downloading %s (%s)", archive.Name, humanBytes(resp.ContentLength))
	} else {
		log.Printf("selfupdate: downloading %s (size unknown)", archive.Name)
	}

	// 边写边算 SHA256。
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), resp.Body)
	if err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return "", fmt.Errorf("write archive: %w", err)
	}
	log.Printf("selfupdate: downloaded %s (%s), verifying sha256", archive.Name, humanBytes(n))
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("close archive: %w", err)
	}

	got := hex.EncodeToString(h.Sum(nil))
	if got != want {
		os.Remove(tmpPath)
		return "", fmt.Errorf("sha256 mismatch for %s: got %s want %s", archive.Name, got, want)
	}

	// 规整命名（保留正确扩展名，便于后续按扩展名判定解压方式）。
	final := filepath.Join(destDir, archive.Name)
	if err := os.Rename(tmpPath, final); err != nil {
		// rename 失败不致命，直接用临时路径即可（同分区一般不会失败）。
		return tmpPath, nil
	}
	return final, nil
}
