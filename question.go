package main

import (
	"encoding/base64"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/onprs/OCS-Tiku/internal/engine"
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
	return engine.DecodeInlineImage(raw, engine.InlineImageLimits{
		MaxEncodedBytes: int64(base64.StdEncoding.EncodedLen(20 << 20)),
		MaxBytes:        20 << 20,
		MaxPixels:       16000000,
		ShrinkBytes:     5 << 20,
	})
}
