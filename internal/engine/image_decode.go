package engine

import (
	"bytes"
	"encoding/base64"
	"errors"
	"image"
	"strings"
)

// InlineImageLimits 约束 OCS 请求中的 Base64 图片。
type InlineImageLimits struct {
	// MaxEncodedBytes 是 Base64 文本长度上限。
	MaxEncodedBytes int64
	// MaxBytes 是处理后允许的最大解码体积。
	MaxBytes int64
	// MaxPixels 是解码后的像素总数上限。
	MaxPixels int64
	// ShrinkBytes 以上的图片按上游规则缩小；为 0 时不缩小。
	ShrinkBytes int64
}

// DecodeInlineImage 校验并解码 Base64 图片 data URL，必要时复用上游缩图流程。
func DecodeInlineImage(raw string, limits InlineImageLimits) ([]byte, error) {
	header, encoded, ok := strings.Cut(raw, ",")
	header = strings.ToLower(header)
	if !ok || !strings.HasPrefix(header, "data:image/") || !strings.HasSuffix(header, ";base64") {
		return nil, errors.New("需要 Base64 图片 data URL")
	}
	if limits.MaxEncodedBytes <= 0 {
		limits.MaxEncodedBytes = int64(base64.StdEncoding.EncodedLen(20 << 20))
	}
	if int64(len(encoded)) > limits.MaxEncodedBytes {
		return nil, errors.New("图片超过体积上限")
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, errors.New("Base64 编码无效")
	}
	if limits.MaxBytes > 0 && int64(len(data)) > limits.MaxBytes && limits.ShrinkBytes <= 0 {
		return nil, errors.New("图片超过体积上限")
	}
	// OCS 会统一 data URL 的 MIME 前缀，格式以实际图片内容为准。
	if _, err := ImageExtension(data); err != nil {
		return nil, err
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, errors.New("图片内容无效")
	}
	if cfg.Width < 1 || cfg.Height < 1 || (limits.MaxPixels > 0 && int64(cfg.Width)*int64(cfg.Height) > limits.MaxPixels) {
		return nil, errors.New("图片尺寸无效")
	}
	if limits.ShrinkBytes > 0 && int64(len(data)) > limits.ShrinkBytes {
		data, err = ShrinkImage(data)
		if err != nil {
			return nil, err
		}
	}
	if limits.MaxBytes > 0 && int64(len(data)) > limits.MaxBytes {
		return nil, errors.New("图片超过体积上限")
	}
	return data, nil
}
