// Package normalize 负责题目规范化、图片校验和精确缓存键。
//
// 规范化只做确定性文本处理；不做模糊匹配，也不把相似历史题当作缓存命中。
package normalize

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"regexp"
	"strconv"
	"strings"

	"github.com/onprs/OCS-Tiku/internal/engine"
)

var (
	tagPattern   = regexp.MustCompile(`<[^>]+>`)
	spacePattern = regexp.MustCompile(`\s+`)
	imagePattern = regexp.MustCompile(`\[图片([1-9][0-9]*)\]`)
)

type Limits struct {
	MaxImages      int
	MaxImageBytes  int64
	MaxImagePixels int64
}

type Question struct {
	Title      string
	Options    string
	Type       string
	Images     []Image
	TitleText  string
	OptionsRaw string
}

type Image struct {
	Index        int
	Data         []byte
	MIME         string
	Width        int64
	Height       int64
	Digest       string
	OriginalSize int
}

var validTypes = map[string]bool{
	"single": true, "multiple": true, "judgement": true, "completion": true, "shortanswer": true,
}

// Prepare 校验题型、图片 data URL、数量和占位符，并保留原题顺序。
func Prepare(title, options, questionType string, rawImages []string, limits Limits) (Question, error) {
	questionType = strings.ToLower(strings.TrimSpace(questionType))
	if questionType == "" {
		questionType = "single"
	}
	if !validTypes[questionType] {
		return Question{}, errors.New("题目类型无效")
	}
	if strings.TrimSpace(title) == "" {
		return Question{}, errors.New("题目标题不能为空")
	}
	if len(title)+len(options) > 1<<20 {
		return Question{}, errors.New("题目文本过大")
	}
	if len(rawImages) > limits.MaxImages {
		return Question{}, fmt.Errorf("图片数量超过上限 %d", limits.MaxImages)
	}
	used := make(map[int]bool)
	for _, match := range imagePattern.FindAllStringSubmatch(title+"\n"+options, -1) {
		index, _ := strconv.Atoi(match[1])
		if index < 1 || index > len(rawImages) {
			return Question{}, fmt.Errorf("%s 缺少对应的图片数据", match[0])
		}
		used[index] = true
	}
	images := make([]Image, 0, len(rawImages))
	for i, raw := range rawImages {
		if !used[i+1] {
			return Question{}, fmt.Errorf("第 %d 张图片缺少对应的 [图片%d] 标记", i+1, i+1)
		}
		item, err := decodeImage(raw, limits)
		if err != nil {
			return Question{}, fmt.Errorf("第 %d 张图片无效: %w", i+1, err)
		}
		item.Index = i + 1
		images = append(images, item)
	}
	q := Question{
		Title:      title,
		Options:    options,
		Type:       questionType,
		Images:     images,
		TitleText:  CanonicalText(title),
		OptionsRaw: options,
	}
	return q, nil
}

func decodeImage(raw string, limits Limits) (Image, error) {
	data, err := engine.DecodeInlineImage(raw, engine.InlineImageLimits{
		MaxEncodedBytes: int64(base64.StdEncoding.EncodedLen(20 << 20)),
		MaxBytes:        limits.MaxImageBytes,
		MaxPixels:       limits.MaxImagePixels,
		ShrinkBytes:     limits.MaxImageBytes,
	})
	if err != nil {
		return Image{}, err
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return Image{}, errors.New("图片内容无效或格式不受支持")
	}
	if cfg.Width < 1 || cfg.Height < 1 || int64(cfg.Width)*int64(cfg.Height) > limits.MaxImagePixels {
		return Image{}, errors.New("图片像素超过上限")
	}
	sum := sha256.Sum256(data)
	return Image{
		Data: data, MIME: "image/" + format,
		Width: int64(cfg.Width), Height: int64(cfg.Height), Digest: hex.EncodeToString(sum[:]), OriginalSize: len(data),
	}, nil
}

// CanonicalText 去除 HTML 标签、反转义并折叠空白；不改变文字内容顺序。
func CanonicalText(value string) string {
	value = html.UnescapeString(tagPattern.ReplaceAllString(value, " "))
	value = strings.ReplaceAll(value, "\u00a0", " ")
	return strings.TrimSpace(spacePattern.ReplaceAllString(value, " "))
}

// CanonicalOptions 统一换行、去除行尾空白并保留选项顺序。
func CanonicalOptions(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	lines := strings.Split(value, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(strings.TrimSpace(lines[i]), " ")
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// ImagesDigest 对图片字节按顺序做长度前缀摘要；顺序不同则是不同题。
func ImagesDigest(images []Image) string {
	h := sha256.New()
	for _, img := range images {
		fmt.Fprintf(h, "image:%d:%d:", img.Index, len(img.Data))
		h.Write(img.Data)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// CacheKey 生成精确缓存键：规范化题、选项顺序、题型、图片摘要、模型、提示词版本、规范化版本和范围键。
func CacheKey(offeringID, promptVersion, normalizationVersion, scope, scopeKeyHash, title, options, questionType string, images []Image) string {
	h := sha256.New()
	fields := []string{
		"tiku-exact-cache-v1", offeringID, promptVersion, normalizationVersion,
		scope, scopeKeyHash, questionType, CanonicalText(title), CanonicalOptions(options), ImagesDigest(images),
	}
	for _, field := range fields {
		fmt.Fprintf(h, "%d:%s\n", len(field), field)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ScopeKeyHash 只保存范围键摘要，不在数据库中存放主平台传入的不可逆键原文。
func ScopeKeyHash(scope, key string) string {
	if scope == "shared" {
		return "shared"
	}
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}
