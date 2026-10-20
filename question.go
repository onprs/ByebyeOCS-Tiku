package main

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"regexp"
	"strconv"
	"strings"
)

const maxSearchRequestBytes = 32 << 20

var imagePlaceholderPattern = regexp.MustCompile(`\[图片([1-9][0-9]*)\]`)

type searchRequest struct {
	Title             string   `json:"title"`
	Options           string   `json:"options"`
	Type              string   `json:"type"`
	Images            []string `json:"images"`
	SuggestionTitle   string   `json:"suggestion_title"`
	SuggestionOptions string   `json:"suggestion_options"`
}

// prepare 保留原题输入，使用优化文本中的编号匹配浏览器上传的图片。
func (q searchRequest) prepare() (string, string, map[string][]byte, error) {
	title, options := q.Title, q.Options
	if q.SuggestionTitle != "" {
		title = q.SuggestionTitle
	}
	if q.SuggestionOptions != "" {
		options = q.SuggestionOptions
	}
	references := extractImageReferences(title + "\n" + options)
	used := make(map[int]bool)
	for _, reference := range references {
		match := imagePlaceholderPattern.FindStringSubmatch(reference)
		if !strings.HasPrefix(reference, "[图片") || match == nil {
			continue
		}
		index, err := strconv.Atoi(match[1])
		if err != nil || index < 1 || index > len(q.Images) {
			return "", "", nil, fmt.Errorf("%s 缺少对应的图片数据", match[0])
		}
		used[index] = true
	}
	images := make(map[string][]byte, len(q.Images))
	for i, raw := range q.Images {
		if !used[i+1] {
			return "", "", nil, fmt.Errorf("第 %d 张图片缺少对应的 [图片%d] 标记", i+1, i+1)
		}
		data, err := decodeInlineImage(raw)
		if err != nil {
			return "", "", nil, fmt.Errorf("第 %d 张图片无效: %w", i+1, err)
		}
		images[fmt.Sprintf("[图片%d]", i+1)] = data
	}
	return title, options, images, nil
}

func decodeInlineImage(raw string) ([]byte, error) {
	header, encoded, ok := strings.Cut(raw, ",")
	header = strings.ToLower(header)
	if !ok || !strings.HasPrefix(header, "data:image/") || !strings.HasSuffix(header, ";base64") {
		return nil, errors.New("需要 Base64 图片 data URL")
	}
	if len(encoded) > base64.StdEncoding.EncodedLen(20<<20) {
		return nil, errors.New("图片超过 20 MB")
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, errors.New("Base64 编码无效")
	}
	if len(data) > 20<<20 {
		return nil, errors.New("图片超过 20 MB")
	}
	// OCS 会统一 data URL 的 MIME 前缀，格式以实际图片内容为准。
	if _, err := imageExtension(data); err != nil {
		return nil, err
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, errors.New("图片内容无效")
	}
	if cfg.Width < 1 || cfg.Height < 1 || int64(cfg.Width)*int64(cfg.Height) > 16000000 {
		return nil, errors.New("图片尺寸无效")
	}
	if len(data) > 5<<20 {
		return shrinkImage(data)
	}
	return data, nil
}
