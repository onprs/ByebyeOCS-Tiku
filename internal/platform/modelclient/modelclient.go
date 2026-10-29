// Package modelclient 把 platform v2 的调用请求适配到共享 internal/engine。
//
// 协议选择、消息编码、上游请求和错误分类都由 engine 提供；本包只负责
// platform 的网络边界（私网拦截、可选私有 CA、超时）和稳定错误码映射。
package modelclient

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"time"

	"github.com/onprs/OCS-Tiku/internal/engine"
	"github.com/onprs/OCS-Tiku/internal/platform/normalize"
)

type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Message }

type TokenUsage struct {
	InputTokens  *int64
	OutputTokens *int64
	TotalTokens  *int64
	Complete     bool
}

type Result struct {
	Content   string
	Reasoning string
	Usage     TokenUsage
	Latency   time.Duration
}

type Call struct {
	Protocol        string
	Vendor          string
	BaseURL         string
	APIKey          string
	Model           string
	System          string
	Prompt          string
	Images          []normalize.Image
	MaxOutputTokens int
	Temperature     float64
}

type Options struct {
	AllowLoopback bool
	CAFile        string
	Timeout       time.Duration
}

type Client struct {
	engine *engine.Client
}

// maxUpstreamResponseBytes 限制模型响应体，避免忽略预算后无限读取 JSON。
const maxUpstreamResponseBytes int64 = 8 << 20

// NewWithOptions 创建带私网拦截和可选私有 CA 的模型客户端。
func NewWithOptions(opts Options) (*Client, error) {
	if opts.Timeout <= 0 {
		opts.Timeout = 90 * time.Second
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if opts.CAFile != "" {
		data, err := os.ReadFile(opts.CAFile)
		if err != nil {
			return nil, errors.New("无法读取模型上游 CA 文件")
		}
		pool, err := x509.SystemCertPool()
		if err != nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(data) {
			return nil, errors.New("模型上游 CA 文件没有有效证书")
		}
		tlsConfig.RootCAs = pool
	}
	dialer := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		// 不使用环境代理，避免代理绕过目标 IP 校验。
		DialContext:           guardedDial(dialer, opts.AllowLoopback),
		TLSClientConfig:       tlsConfig,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 85 * time.Second,
		MaxIdleConns:          32,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       60 * time.Second,
	}
	httpClient := &http.Client{
		Transport: boundedTransport{base: transport, limit: maxUpstreamResponseBytes},
		Timeout:   opts.Timeout,
		// 重定向可能绕过目标校验；模型端点不需要重定向。
		CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("上游重定向已拒绝") },
	}
	return &Client{engine: engine.NewClientWithHTTP(httpClient)}, nil
}

// boundedTransport 在传输层限制响应体大小，覆盖所有协议客户端读取路径。
type boundedTransport struct {
	base  http.RoundTripper
	limit int64
}

func (t boundedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := t.base.RoundTrip(request)
	if err != nil {
		return nil, err
	}
	if response.ContentLength > t.limit {
		_ = response.Body.Close()
		return nil, engine.ErrResponseTooLarge
	}
	response.Body = &boundedBody{body: response.Body, remaining: t.limit}
	return response, nil
}

// boundedBody 允许读取到 limit 字节；再多读一个字节即返回固定错误。
type boundedBody struct {
	body      io.ReadCloser
	remaining int64
	exceeded  bool
}

func (b *boundedBody) Read(buffer []byte) (int, error) {
	if b.exceeded {
		return 0, engine.ErrResponseTooLarge
	}
	if len(buffer) == 0 {
		return 0, nil
	}
	if b.remaining <= 0 {
		var probe [1]byte
		n, err := b.body.Read(probe[:])
		if n > 0 {
			b.exceeded = true
			return 0, engine.ErrResponseTooLarge
		}
		return 0, err
	}
	if int64(len(buffer)) > b.remaining {
		buffer = buffer[:b.remaining]
	}
	n, err := b.body.Read(buffer)
	b.remaining -= int64(n)
	return n, err
}

func (b *boundedBody) Close() error { return b.body.Close() }

var blockedUpstreamRanges = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("240.0.0.0/4"),
}

func guardedDial(dialer *net.Dialer, allowLoopback bool) func(context.Context, string, string) (net.Conn, error) {
	return guardedDialWith(net.DefaultResolver.LookupIPAddr, dialer.DialContext, allowLoopback)
}

func guardedDialWith(resolve func(context.Context, string) ([]net.IPAddr, error), dial func(context.Context, string, string) (net.Conn, error), allowLoopback bool) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, errors.New("上游地址无效")
		}
		ips, err := resolve(ctx, host)
		if err != nil || len(ips) == 0 {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, errors.New("上游地址无法解析")
		}
		attempted := false
		for _, item := range ips {
			if !allowedUpstreamIP(item.IP, allowLoopback) {
				continue
			}
			attempted = true
			conn, err := dial(ctx, network, net.JoinHostPort(item.IP.String(), port))
			if err == nil {
				return conn, nil
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
		}
		if attempted {
			return nil, errors.New("无法连接上游地址")
		}
		return nil, errors.New("上游地址不可访问")
	}
}

func allowedUpstreamIP(ip net.IP, allowLoopback bool) bool {
	if ip.IsLoopback() {
		return allowLoopback
	}
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return false
	}
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	for _, prefix := range blockedUpstreamRanges {
		if prefix.Contains(addr.Unmap()) {
			return false
		}
	}
	return true
}

// Chat 执行一次非流式模型调用。调用方负责超时；本函数不自动重试。
func (c *Client) Chat(ctx context.Context, call Call) (Result, error) {
	protocol := call.Protocol
	if protocol == "" {
		protocol = engine.ProtocolOpenAIChat
	}
	service := engine.ServiceConfig{
		Protocol: protocol, BaseURL: call.BaseURL, APIKey: call.APIKey, Vendor: call.Vendor,
	}
	model := engine.ModelConfig{Model: call.Model}
	text := call.Prompt
	var content any = text
	if len(call.Images) > 0 {
		parts := make([]engine.ContentPart, 0, len(call.Images)+1)
		parts = append(parts, engine.TextPart(text))
		for _, img := range call.Images {
			parts = append(parts, engine.ImagePart(img.Data))
		}
		content = parts
	}
	messages := make([]engine.ChatMessage, 0, 2)
	if call.System != "" {
		messages = append(messages, engine.ChatMessage{Role: "system", Content: call.System})
	}
	messages = append(messages, engine.ChatMessage{Role: "user", Content: content})
	result, err := c.engine.Chat(ctx, service, model, messages, call.Temperature, call.MaxOutputTokens)
	if err != nil {
		return Result{}, mapError(err)
	}
	return Result{
		Content: result.Content, Reasoning: result.Reasoning,
		Usage: usageFromEngine(result.Usage), Latency: result.Latency,
	}, nil
}

func usageFromEngine(usage engine.TokenUsage) TokenUsage {
	if !usage.Present {
		return TokenUsage{}
	}
	input, output, total := usage.InputTokens, usage.OutputTokens, usage.TotalTokens
	result := TokenUsage{Complete: usage.Complete && input >= 0 && output >= 0 && total >= 0}
	if input >= 0 {
		result.InputTokens = &input
	}
	if output >= 0 {
		result.OutputTokens = &output
	}
	if total >= 0 {
		result.TotalTokens = &total
	}
	return result
}

func mapError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &Error{Code: "upstream_timeout", Message: safeMessage("upstream_timeout")}
	}
	if errors.Is(err, context.Canceled) {
		return &Error{Code: "request_cancelled", Message: safeMessage("request_cancelled")}
	}
	var upstream *engine.Error
	if errors.As(err, &upstream) {
		return &Error{Code: upstream.Code, Message: safeMessage(upstream.Code)}
	}
	return &Error{Code: "model_unavailable", Message: safeMessage("model_unavailable")}
}

// safeMessage 只返回 platform 固定文案，不回显原始错误、URL、密钥或题干。
func safeMessage(code string) string {
	switch code {
	case "model_auth_failed":
		return "模型服务认证失败"
	case "upstream_rate_limited":
		return "模型服务限流，请稍后人工重试"
	case "upstream_timeout":
		return "模型调用超时，结果未知"
	case "request_cancelled":
		return "请求已取消，模型结果未知"
	case "response_too_large":
		return "模型响应超过大小上限"
	case "invalid_request":
		return "模型请求参数无效"
	case "unsupported_protocol":
		return "该协议不支持无用量连通测试"
	default:
		return "模型服务不可用"
	}
}

// Reachability 只检查网络/认证可达性，不产生回答 token。
func (c *Client) Reachability(ctx context.Context, protocol, baseURL, apiKey string) error {
	if protocol == "" {
		protocol = engine.ProtocolOpenAIChat
	}
	err := c.engine.Reachability(ctx, engine.ServiceConfig{Protocol: protocol, BaseURL: baseURL, APIKey: apiKey})
	if err == nil {
		return nil
	}
	return mapError(err)
}
