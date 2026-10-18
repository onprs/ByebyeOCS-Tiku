package main

import (
	"context"
	"errors"
	"fmt"
	"html"
	"regexp"
	"strings"
	"time"
)

var (
	answerPattern = regexp.MustCompile(`(?is)<answer>\s*(.*?)\s*</answer>`)
	tagPattern    = regexp.MustCompile(`<[^>]+>`)
	spacePattern  = regexp.MustCompile(`\s+`)
	optionLabel   = regexp.MustCompile(`^[A-Za-z][.、)）:：\s]+`)
)

type Solver struct {
	config *Config
	models *ModelClient
	images *ImageProcessor
}

func questionLabel(kind string) string {
	switch kind {
	case "single":
		return "单选题"
	case "multiple":
		return "多选题"
	case "judgement":
		return "判断题"
	case "completion":
		return "填空题"
	default:
		return kind
	}
}

func cleanTitle(text string) string {
	return strings.TrimSpace(spacePattern.ReplaceAllString(html.UnescapeString(tagPattern.ReplaceAllString(text, "")), " "))
}

func labelOptions(options string) string {
	if strings.TrimSpace(options) == "" {
		return ""
	}
	lines := strings.Split(options, "\n")
	groups := []string{}
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if optionLabel.MatchString(line) || len(groups) == 0 {
			groups = append(groups, line)
		} else {
			groups[len(groups)-1] += " " + line
		}
	}
	// 未带 A./B. 前缀时，每行都是一个选项。
	hasLabel := false
	for _, line := range lines {
		if optionLabel.MatchString(strings.TrimSpace(line)) {
			hasLabel = true
			break
		}
	}
	if !hasLabel {
		groups = groups[:0]
		for _, line := range lines {
			if line = strings.TrimSpace(line); line != "" {
				groups = append(groups, line)
			}
		}
	}
	result := make([]string, 0, len(groups))
	for i, group := range groups {
		label := fmt.Sprintf("%d", i)
		if i < 26 {
			label = string(rune('A' + i))
		}
		result = append(result, label+". "+strings.TrimSpace(optionLabel.ReplaceAllString(group, "")))
	}
	return strings.Join(result, "\n")
}

func buildPrompt(title, options, kind string) string {
	parts := []string{"题型：" + questionLabel(kind), "题目：" + cleanTitle(title)}
	if kind != "completion" && strings.TrimSpace(options) != "" {
		parts = append(parts, "选项：\n"+labelOptions(options))
	}
	return strings.Join(parts, "\n")
}

func extractAnswer(content, reasoning, kind string) string {
	raw := ""
	for _, value := range []string{content, reasoning} {
		if match := answerPattern.FindStringSubmatch(value); len(match) == 2 && strings.TrimSpace(match[1]) != "" {
			raw = strings.TrimSpace(match[1])
			break
		}
	}
	if raw == "" {
		return strings.TrimSpace(content)
	}
	if kind == "completion" {
		return raw
	}
	if raw == "对" || raw == "错" {
		return raw
	}
	letters := regexp.MustCompile(`[A-Za-z]`).FindAllString(raw, -1)
	if len(letters) > 0 {
		if kind == "multiple" {
			for i := range letters {
				letters[i] = strings.ToUpper(letters[i])
			}
			return strings.Join(letters, "#")
		}
		return strings.ToUpper(letters[0])
	}
	return raw
}

func directContent(title, options, kind string, images map[string][]byte) []contentPart {
	parts := []contentPart{{Type: "text", Text: "题型：" + questionLabel(kind) + "\n题目：" + replaceURLs(cleanTitle(title))}}
	for _, raw := range extractImageURLs(title) {
		if data := images[raw]; data != nil {
			parts = append(parts, imagePart(data))
		}
	}
	if kind != "completion" && strings.TrimSpace(options) != "" {
		parts = append(parts, textPart("选项：\n"+labelOptions(replaceURLs(options))))
		optionIndex := -1
		for _, line := range strings.Split(options, "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			optionIndex++
			for _, raw := range extractImageURLs(line) {
				if data := images[raw]; data != nil {
					parts = append(parts, textPart(fmt.Sprintf("[选项 %c 的图片]", 'A'+optionIndex)), imagePart(data))
				}
			}
		}
	}
	return append(parts, textPart("请输出<answer>X</answer>"))
}

func replaceURLs(text string) string {
	for _, raw := range extractImageURLs(text) {
		text = strings.ReplaceAll(text, raw, "[图]")
	}
	return text
}

func (s *Solver) solve(ctx context.Context, title, options, kind string) (r Record, solveErr error) {
	cfg := s.config.snapshot()
	start := time.Now()
	r = Record{Timestamp: start.Format("2006-01-02T15:04:05.000000"), Type: kind, Status: "complete",
		RawTitle: title, RawOptions: options, ProcessedTitle: title, ProcessedOptions: options,
		Images: []ImageInfo{}, TextModel: displayName(cfg.Models.Text), VisionModel: displayName(cfg.Models.Vision)}
	direct := cfg.Answer.Mode == "direct" && cfg.Models.Vision.Provider != ""
	defer func() { r.TotalTimeMS = time.Since(start).Milliseconds() }()
	processedTitle, processedOptions, infos, images := s.images.process(ctx, title, options, cfg.Models.Vision, s.models, direct)
	r.ProcessedTitle, r.ProcessedOptions, r.Images = processedTitle, processedOptions, infos
	var content, reasoning string
	var err error
	if direct {
		r.TextModel = displayName(cfg.Models.Vision) + " (direct)"
		r.FinalPrompt = fmt.Sprintf("题型：%s\n题目：%s\n选项：\n%s\n[Direct 模式: 含 %d 张原图，直接发给视觉模型作答]", questionLabel(kind), title, options, len(images))
		content, reasoning, err = s.models.chat(ctx, cfg.Models.Vision,
			[]chatMessage{{Role: "system", Content: cfg.Answer.SystemPrompt}, {Role: "user", Content: directContent(title, options, kind, images)}}, 0.1, 0)
	} else {
		if cfg.Models.Text.Provider == "" {
			err = errors.New("文本模型未配置")
		} else {
			r.FinalPrompt = buildPrompt(processedTitle, processedOptions, kind)
			content, reasoning, err = s.models.chat(ctx, cfg.Models.Text,
				[]chatMessage{{Role: "system", Content: cfg.Answer.SystemPrompt}, {Role: "user", Content: r.FinalPrompt}}, 0.1, 0)
		}
	}
	r.RawContent, r.Reasoning = content, reasoning
	if err != nil {
		r.Answer = "ERROR: " + err.Error()
		return r, err
	}
	r.Answer = extractAnswer(content, reasoning, kind)
	return r, nil
}
