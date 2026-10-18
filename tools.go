package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// historyTool 提供本站历史答题记录检索。
type historyTool struct{ store *Store }

func (t *historyTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "lookup_history",
		Desc: "检索本站已作答过的相似题目、原选项及答案。历史答案字母仅对应原选项，须按当前题目的选项内容重新判断",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"query": {Type: schema.String, Desc: "检索关键词，例如知识点或题干片段", Required: true},
		}),
	}, nil
}

func (t *historyTool) InvokableRun(ctx context.Context, arguments string, _ ...tool.Option) (string, error) {
	var params struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal([]byte(arguments), &params); err != nil {
		return "", fmt.Errorf("参数解析失败: %w", err)
	}
	query := strings.TrimSpace(params.Query)
	if query == "" {
		return "未提供检索关键词", nil
	}
	if t.store == nil {
		return "历史记录不可用", nil
	}
	records, err := t.store.search(ctx, query, 5)
	if err != nil {
		return "", err
	}
	if len(records) == 0 {
		return "没有找到相关历史记录", nil
	}
	var builder strings.Builder
	builder.WriteString("找到以下历史记录：\n")
	for _, record := range records {
		title := cleanTitle(record.RawTitle)
		if len(title) > 120 {
			title = truncateText(title, 120) + "…"
		}
		answer := record.Answer
		if len(answer) > 200 {
			answer = truncateText(answer, 200) + "…"
		}
		options := record.ProcessedOptions
		if strings.TrimSpace(options) == "" {
			options = record.RawOptions
		}
		options = labelOptions(options)
		if len(options) > 2000 {
			options = truncateText(options, 2000) + "…（选项已截断）"
		}
		fmt.Fprintf(&builder, "- 题型：%s\n  题目：%s\n", questionLabel(record.Type), title)
		if options != "" {
			fmt.Fprintf(&builder, "  原选项：\n%s\n", options)
		}
		fmt.Fprintf(&builder, "  答案：%s\n", answer)
	}
	return builder.String(), nil
}
