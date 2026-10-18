package main

import (
	"context"
	"errors"
	"fmt"
	"html"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/onprs/OCS-Tiku/internal/catalog"
)

var (
	answerPattern = regexp.MustCompile(`(?is)<answer>\s*(.*?)\s*</answer>`)
	tagPattern    = regexp.MustCompile(`<[^>]+>`)
	spacePattern  = regexp.MustCompile(`\s+`)
	optionLabel   = regexp.MustCompile(`^[A-Za-z][.、)）:：\s]+`)
)

// Solver 完成一次答题：图片处理、模型调用与答案解析。
type Solver struct {
	config  *Config
	models  *ModelClient
	images  *ImageProcessor
	catalog *catalog.Store
	store   *Store
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

// truncateText 按字节上限截断文本，不切断多字节字符。
func truncateText(text string, maxBytes int) string {
	if len(text) <= maxBytes {
		return text
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut]
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

// directContent 构造直传图片的消息内容：图片交给回答模型自行理解。
// 没有可用图片时返回纯文本，与文本请求保持一致的消息格式。
func directContent(title, options, kind string, images map[string][]byte) any {
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
	parts = append(parts, textPart("请输出<answer>X</answer>"))
	for _, part := range parts {
		if part.Type == "image_url" {
			return parts
		}
	}
	text := "题型：" + questionLabel(kind) + "\n题目：" + replaceURLs(cleanTitle(title))
	if kind != "completion" && strings.TrimSpace(options) != "" {
		text += "\n选项：\n" + labelOptions(replaceURLs(options))
	}
	return text + "\n请输出<answer>X</answer>"
}

func replaceURLs(text string) string {
	for _, raw := range extractImageURLs(text) {
		text = strings.ReplaceAll(text, raw, "[图]")
	}
	return text
}

// solve 完成一次答题。图片路径由回答模型的视觉能力决定：
// 具备视觉能力时图片直接交给回答模型，否则交给图片转述模型转为文字。
// 开启 Agent 模式且模型支持工具调用时，由 agent 分步推理，失败时回退到普通作答。
func (s *Solver) solve(ctx context.Context, title, options, kind string) (result Record, solveErr error) {
	cfg := s.config.snapshot()
	start := time.Now()
	answerService := cfg.Services[cfg.Answer.Answer.Service]
	captionService := cfg.Services[cfg.Answer.Caption.Service]
	capability := s.capability(answerService, cfg.Answer.Answer)
	direct := capability.Vision

	result = Record{
		Timestamp: start.Format("2006-01-02T15:04:05.000000"), Type: kind, Status: "complete",
		RawTitle: title, RawOptions: options, ProcessedTitle: title, ProcessedOptions: options,
		Images: []ImageInfo{}, Vision: capability.Vision,
		AnswerModel: modelLabel(answerService, cfg.Answer.Answer), CaptionModel: captionLabel(captionService, cfg.Answer.Caption),
	}
	defer func() { result.TotalTimeMS = time.Since(start).Milliseconds() }()

	var caption *captionTask
	if cfg.Answer.Caption.Model != "" {
		captionModel := cfg.Answer.Caption
		caption = &captionTask{run: func(callCtx context.Context, data []byte) (string, error) {
			describeCtx := s.modelContext(callCtx, "caption", captionService, captionModel)
			return s.models.describe(describeCtx, captionService, captionModel, cfg.Answer.CaptionPrompt, data)
		}}
	}
	processedTitle, processedOptions, infos, images, err := s.images.process(ctx, title, options, caption, direct)
	result.ProcessedTitle, result.ProcessedOptions, result.Images = processedTitle, processedOptions, infos
	if err != nil {
		result.Answer = "ERROR: " + err.Error()
		return result, err
	}

	var userContent any
	if direct {
		result.FinalPrompt = buildPrompt(title, options, kind)
		if len(images) > 0 {
			result.FinalPrompt += fmt.Sprintf("\n[图片直传: 含 %d 张原图]", len(images))
		}
		userContent = directContent(title, options, kind, images)
	} else {
		result.FinalPrompt = buildPrompt(processedTitle, processedOptions, kind)
		userContent = result.FinalPrompt
	}
	prompt := []chatMessage{
		{Role: "system", Content: cfg.Answer.SystemPrompt},
		{Role: "user", Content: userContent},
	}

	var content, reasoning string
	if cfg.Answer.Agent.Enabled {
		if !capability.ToolCall {
			result.AgentError = "回答模型未标注工具调用能力，已按普通方式作答"
		} else {
			agentCtx := s.modelContext(ctx, "agent", answerService, cfg.Answer.Answer)
			content, reasoning, err = s.runAgent(agentCtx, cfg, answerService, []chatMessage{{Role: "user", Content: userContent}})
			if err != nil {
				result.AgentError = "Agent 作答失败，已回退：" + agentErrorText(err)
				content, reasoning = "", ""
			}
		}
	}
	if strings.TrimSpace(content) == "" && strings.TrimSpace(reasoning) == "" {
		answerCtx := s.modelContext(ctx, "answer", answerService, cfg.Answer.Answer)
		content, reasoning, err = s.models.chat(answerCtx, answerService, cfg.Answer.Answer, prompt, 0.1, 0)
		result.RawContent, result.Reasoning = content, reasoning
		if err != nil {
			result.Answer = "ERROR: " + err.Error()
			return result, err
		}
	} else {
		result.RawContent, result.Reasoning = content, reasoning
	}
	result.Answer = extractAnswer(content, reasoning, kind)
	return result, nil
}

// modelLabel 返回回答模型的展示名，未配置时为空。
func modelLabel(service ServiceConfig, model ModelConfig) string {
	if model.Model == "" {
		return ""
	}
	name := service.Name
	if name == "" {
		name = "未知服务"
	}
	return name + " (" + model.Model + ")"
}

// captionLabel 返回图片转述模型的展示名，未配置时为空。
func captionLabel(service ServiceConfig, model ModelConfig) string {
	if model.Model == "" {
		return ""
	}
	return modelLabel(service, model)
}

// answerDisabled 判断回答模型是否未配置。
func answerDisabled(model ModelConfig) bool {
	return strings.TrimSpace(model.Model) == "" || strings.TrimSpace(model.Service) == ""
}

var errAnswerMissing = errors.New("请先在设置中配置回答模型")
