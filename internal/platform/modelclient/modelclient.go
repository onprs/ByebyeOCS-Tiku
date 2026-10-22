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
	"fmt"
	"net"
	"net/http"
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
		MaxIdleConns:          16,
		IdleConnTimeout:       60 * time.Second,
	}
	httpClient := &http.Client{
		Transport: transport,
		Timeout:   opts.Timeout,
		// 重定向可能绕过目标校验；模型端点不需要重定向。
		CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("上游重定向已拒绝") },
	}
	return &Client{engine: engine.NewClientWithHTTP(httpClient)}, nil
}

func guardedDial(dialer *net.Dialer, allowLoopback bool) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, errors.New("上游地址无效")
		}
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil || len(ips) == 0 {
			return nil, errors.New("上游地址无法解析")
		}
		for _, item := range ips {
			ip := item.IP
			if ip.IsLoopback() {
				if allowLoopback {
					return dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
				}
				continue
			}
			if ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
				continue
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		}
		return nil, errors.New("上游地址不可访问")
	}
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
	return TokenUsage{InputTokens: &input, OutputTokens: &output, TotalTokens: &total, Complete: usage.Complete}
}

func mapError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &Error{Code: "upstream_timeout", Message: "模型调用超时，结果未知"}
	}
	if errors.Is(err, context.Canceled) {
		return &Error{Code: "request_cancelled", Message: "请求已取消，模型结果未知"}
	}
	var upstream *engine.Error
	if errors.As(err, &upstream) {
		return &Error{Code: upstream.Code, Message: upstream.Message}
	}
	return &Error{Code: "model_unavailable", Message: fmt.Sprintf("模型服务不可用: %v", err)}
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
