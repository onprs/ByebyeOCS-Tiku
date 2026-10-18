package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type ModelClient struct{ http *http.Client }

func newModelClient() *ModelClient {
	return &ModelClient{http: &http.Client{Timeout: 90 * time.Second}}
}

type chatMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type contentPart struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *imageURL `json:"image_url,omitempty"`
}

type imageURL struct {
	URL string `json:"url"`
}

func textPart(s string) contentPart { return contentPart{Type: "text", Text: s} }
func imagePart(data []byte) contentPart {
	mime := http.DetectContentType(data)
	return contentPart{Type: "image_url", ImageURL: &imageURL{URL: "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)}}
}

func (c *ModelClient) chat(ctx context.Context, cfg ModelConfig, messages []chatMessage, temperature float64, tokens int) (string, string, error) {
	if cfg.APIKey == "" || cfg.Model == "" || cfg.BaseURL == "" {
		return "", "", errors.New("请先在设置中填写模型 API Key、地址和名称")
	}
	base, err := url.Parse(cfg.BaseURL)
	if err != nil || (base.Scheme != "https" && base.Scheme != "http") || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" || base.Opaque != "" {
		return "", "", errors.New("模型 API 地址无效")
	}
	body := map[string]any{"model": cfg.Model, "messages": messages, "temperature": temperature}
	if cfg.Provider == "deepseek" {
		delete(body, "temperature")
		body["reasoning_effort"] = cfg.ReasoningEffort
		body["thinking"] = map[string]string{"type": "enabled"}
	}
	if tokens > 0 {
		body["max_tokens"] = tokens
	}
	data, err := json.Marshal(body)
	if err != nil {
		return "", "", err
	}
	endpoint := *base
	endpoint.Path = strings.TrimRight(base.Path, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(data))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	resp, err := c.http.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	limited := io.LimitReader(resp.Body, 4<<20)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var failure struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.NewDecoder(limited).Decode(&failure)
		if failure.Error.Message != "" {
			return "", "", fmt.Errorf("模型请求失败 (%d): %s", resp.StatusCode, failure.Error.Message)
		}
		return "", "", fmt.Errorf("模型请求失败 (%d)", resp.StatusCode)
	}
	var result struct {
		Choices []struct {
			Message struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(limited).Decode(&result); err != nil {
		return "", "", fmt.Errorf("模型响应无效: %w", err)
	}
	if len(result.Choices) == 0 {
		return "", "", errors.New("模型未返回结果")
	}
	return result.Choices[0].Message.Content, result.Choices[0].Message.ReasoningContent, nil
}

func (c *ModelClient) describe(ctx context.Context, cfg ModelConfig, data []byte) (string, error) {
	prompt := "请精确识别图片中的内容。如果是数学公式，用LaTeX格式完整输出每个符号，例如：\\frac{1}{2}、\\sqrt{x}、\\int_{a}^{b}。如果是图表或文字，描述关键信息。如果图片空白或完全无法辨认，回复'空白'。只输出识别结果，不解释。"
	content, _, err := c.chat(ctx, cfg, []chatMessage{{Role: "user", Content: []contentPart{imagePart(data), textPart(prompt)}}}, 0.1, 512)
	return content, err
}

func (c *ModelClient) testText(ctx context.Context, cfg ModelConfig) error {
	_, _, err := c.chat(ctx, cfg, []chatMessage{{Role: "system", Content: "回复OK"}, {Role: "user", Content: "OK"}}, 0.1, 0)
	return err
}

func (c *ModelClient) testVision(ctx context.Context, cfg ModelConfig) error {
	img := image.NewRGBA(image.Rect(0, 0, 100, 30))
	for i := range img.Pix {
		img.Pix[i] = 255
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		return err
	}
	content, _, err := c.chat(ctx, cfg, []chatMessage{{Role: "user", Content: []contentPart{imagePart(b.Bytes()), textPart("What text do you see? Reply 'OK' only.")}}}, 0.1, 512)
	if err == nil && strings.TrimSpace(content) == "" {
		return errors.New("视觉模型未返回结果")
	}
	return err
}

func displayName(cfg ModelConfig) string {
	switch cfg.Provider {
	case "deepseek":
		return "DeepSeek (" + cfg.Model + ")"
	case "openai_compat":
		return "OpenAI Compat (" + cfg.Model + ")"
	case "openai_vl":
		return "OpenAI VL (" + cfg.Model + ")"
	default:
		return "none"
	}
}
