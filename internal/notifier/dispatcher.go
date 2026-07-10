package notifier

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// Dispatcher 是"把一条已格式化好的消息发到某个 target"的抽象。每个 kind 一个
// 实现。工厂 NewDispatcher 按 Target.Kind 分派。
type Dispatcher interface {
	// Kind 返回本 dispatcher 对应的 target kind，用于日志与断言。
	Kind() string
	// Send 发送 msg。ctx 由 Notifier 注入超时（10s）。非 2xx 或网络错误返回 error。
	Send(ctx context.Context, msg string) error
}

// NewDispatcher 按 Target.Kind 返回对应实现。未知 Kind 返回 error，
// Notifier 层收到 error 记一行日志并跳过该 target（R10）。
func NewDispatcher(t Target) (Dispatcher, error) {
	switch t.Kind {
	case "wechat_work":
		return newWechatWorkDispatcher(t.URL), nil
	default:
		return nil, fmt.Errorf("notifier: unknown target kind %q", t.Kind)
	}
}

// wechatWorkDispatcher 向企业微信群机器人 webhook POST 标准文本消息。
type wechatWorkDispatcher struct {
	url    string
	client *http.Client
}

func newWechatWorkDispatcher(url string) *wechatWorkDispatcher {
	return &wechatWorkDispatcher{
		url:    url,
		client: &http.Client{},
	}
}

func (d *wechatWorkDispatcher) Kind() string { return "wechat_work" }

// wechatTextBody 是企微机器人文本消息的标准 payload 结构。
type wechatTextBody struct {
	Msgtype string `json:"msgtype"`
	Text    struct {
		Content string `json:"content"`
	} `json:"text"`
}

func (d *wechatWorkDispatcher) Send(ctx context.Context, msg string) error {
	body := wechatTextBody{Msgtype: "text"}
	body.Text.Content = msg
	buf, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("wechat_work: marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.url, bytes.NewReader(buf))
	if err != nil {
		return fmt.Errorf("wechat_work: new request: %w", scrubURLErr(err))
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.client.Do(req)
	if err != nil {
		return fmt.Errorf("wechat_work: %w", scrubURLErr(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// 消费一点 body 便于日志（限量避免服务器塞脏数据）
		snippet := make([]byte, 256)
		n, _ := io.ReadFull(resp.Body, snippet)
		return fmt.Errorf("wechat_work: HTTP %d: %s", resp.StatusCode, string(snippet[:n]))
	}
	return nil
}

// scrubURLErr 剥掉 *url.Error 里的完整 URL 字段，避免上层用 %v/%s 打日志时
// 把包含 secret 的 webhook URL 泄漏。返回一个不带 URL 字段的错误。
// 非 *url.Error 原样返回。
func scrubURLErr(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("%s: %w", ue.Op, ue.Err)
	}
	return err
}
