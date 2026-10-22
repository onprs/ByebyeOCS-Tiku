package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func inlineTestPNG(t *testing.T, width, height int, seed int64) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	random := rand.New(rand.NewSource(seed))
	for y := range height {
		for x := range width {
			img.SetRGBA(x, y, color.RGBA{R: byte(random.Intn(256)), G: byte(random.Intn(256)), B: byte(random.Intn(256)), A: 255})
		}
	}
	var data bytes.Buffer
	if err := png.Encode(&data, img); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func inlineTestURL(data []byte) string {
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(data)
}

func TestOCSImageConfiguration(t *testing.T) {
	_, web, _ := setupServer(t)
	var config []struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	requestJSON(t, web, "GET", "/api/config", nil, 200, &config)
	if len(config) != 1 {
		t.Fatalf("题库配置数量异常: %d", len(config))
	}
	for _, field := range []string{"title", "options", "type", "suggestion_title", "suggestion_options"} {
		var placeholder string
		if err := json.Unmarshal(config[0].Data[field], &placeholder); err != nil || placeholder != "${"+field+"}" {
			t.Fatalf("缺少 OCS 字段占位符 %s: %s", field, config[0].Data[field])
		}
	}
	var images struct {
		Handler string `json:"handler"`
	}
	if err := json.Unmarshal(config[0].Data["images"], &images); err != nil {
		t.Fatal(err)
	}
	// OCS 使用 JavaScript 执行字段解析器，图片必须保留为数组。
	command := exec.Command("node", "-e", `const read = Function(process.argv[1])();
process.stdout.write(JSON.stringify([read({}), read({images: ['data:image/png;base64,first', 'data:image/png;base64,second']})]));`, images.Handler)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("图片字段解析器执行失败: %v，%s", err, output)
	}
	var actual [][]string
	if err := json.Unmarshal(output, &actual); err != nil || !reflect.DeepEqual(actual, [][]string{{}, {"data:image/png;base64,first", "data:image/png;base64,second"}}) {
		t.Fatalf("图片字段未保留数组类型: %s，错误=%v", output, err)
	}
}

func TestSearchWithOCSInlineImages(t *testing.T) {
	titleData := inlineTestPNG(t, 2, 2, 1)
	optionData := inlineTestPNG(t, 640, 480, 2)
	titleURL, optionURL := inlineTestURL(titleData), inlineTestURL(optionData)
	for _, caption := range []bool{false, true} {
		name := "直接识图"
		if caption {
			name = "图片转述"
		}
		t.Run(name, func(t *testing.T) {
			s, web, _ := setupServer(t)
			s.solver.images.Fetch = func(context.Context, string) ([]byte, error) {
				return nil, errors.New("浏览器已上传图片，不应重新下载原地址")
			}
			var mu sync.Mutex
			calls := 0
			fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Model    string `json:"model"`
					Messages []struct {
						Content json.RawMessage `json:"content"`
					} `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Messages) == 0 {
					t.Errorf("模型请求无效: %v", err)
					w.WriteHeader(400)
					return
				}
				mu.Lock()
				calls++
				mu.Unlock()
				content := body.Messages[len(body.Messages)-1].Content
				answer := "<answer>A</answer>"
				if body.Model == "caption-model" || !caption {
					var parts []struct {
						Type     string `json:"type"`
						Text     string `json:"text"`
						ImageURL struct {
							URL string `json:"url"`
						} `json:"image_url"`
					}
					if err := json.Unmarshal(content, &parts); err != nil {
						t.Errorf("图片消息无效: %v", err)
					}
					var urls, texts []string
					for _, part := range parts {
						if part.Type == "image_url" {
							urls = append(urls, part.ImageURL.URL)
						}
						texts = append(texts, part.Text)
					}
					if body.Model == "caption-model" {
						if len(urls) != 1 {
							t.Errorf("每次转述应只含一张图片: %d", len(urls))
						} else if urls[0] == titleURL {
							answer = "题图说明"
						} else if urls[0] == optionURL {
							answer = "选项图说明"
						} else {
							t.Error("转述模型收到的图片内容不符")
						}
					} else {
						if !reflect.DeepEqual(urls, []string{titleURL, optionURL}) {
							t.Error("直传图片与题目、选项的编号不匹配")
						}
						if !strings.Contains(strings.Join(texts, "\n"), "[选项 A 的图片] [图片1]") {
							t.Error("选项图片缺少编号或归属")
						}
					}
				} else {
					var text string
					if err := json.Unmarshal(content, &text); err != nil || !strings.Contains(text, "题图说明") || !strings.Contains(text, "选项图说明") {
						t.Errorf("回答模型未收到完整图片转述: %v", err)
					}
				}
				jsonResponse(w, 200, map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": answer}}}})
			}))
			defer fake.Close()
			vision := CapabilityOn
			if caption {
				vision = CapabilityOff
			}
			configureAnswer(t, s, ServiceConfig{Protocol: ProtocolOpenAIChat, BaseURL: fake.URL, APIKey: "test-key"}, ModelConfig{Model: "answer-model", Vision: vision})
			if caption {
				answer := s.config.snapshot().Answer
				answer.Caption = ModelConfig{Service: "fake", Model: "caption-model", Vision: CapabilityOn}
				if err := s.config.updateAnswer(answer); err != nil {
					t.Fatal(err)
				}
			}
			input := searchRequest{
				Title: "题目 https://images.example.com/title.png", Options: "https://images.example.com/option.png\n文字", Type: "single",
				Images: []string{optionURL, titleURL}, SuggestionTitle: "题目 [图片2]", SuggestionOptions: "[图片1]\n文字",
			}
			encoded, err := json.Marshal(input)
			if err != nil || len(encoded) <= 1<<20 {
				t.Fatalf("测试请求应大于 1 MB: %d，错误=%v", len(encoded), err)
			}
			var response struct{ Question, Answer string }
			requestJSON(t, web, "POST", "/api/search", input, 200, &response)
			if response.Answer != "A" {
				t.Fatalf("图片题答案异常: %+v", response)
			}
			var record Record
			requestJSON(t, web, "GET", "/api/records/1", nil, 200, &record)
			if record.RawTitle != input.Title || record.RawOptions != input.Options || len(record.Images) != 2 {
				t.Fatal("原题、选项或图片记录不完整")
			}
			for i, want := range [][]byte{titleData, optionData} {
				info := record.Images[i]
				if info.Status != "done" || !strings.HasPrefix(info.Src, "/images/") {
					t.Fatalf("上传图片未保存: %+v", info)
				}
				resp, err := web.Client().Get(web.URL + info.Src)
				if err != nil {
					t.Fatal(err)
				}
				actual, err := io.ReadAll(resp.Body)
				resp.Body.Close()
				if err != nil || !bytes.Equal(actual, want) {
					t.Fatalf("第 %d 张图片缓存内容错误: %v", i+1, err)
				}
			}
			mu.Lock()
			defer mu.Unlock()
			wantCalls := 1
			if caption {
				wantCalls = 3
			}
			if calls != wantCalls {
				t.Fatalf("模型调用次数=%d，期望=%d", calls, wantCalls)
			}
		})
	}
}

func TestInvalidOCSInlineImagesDoNotCreateRecords(t *testing.T) {
	_, web, _ := setupServer(t)
	valid := inlineTestURL(inlineTestPNG(t, 2, 2, 3))
	for name, input := range map[string]searchRequest{
		"缺少图片":     {SuggestionTitle: "[图片1]"},
		"编号超出范围":   {Images: []string{valid}, SuggestionTitle: "[图片2]"},
		"缺少编号":     {Images: []string{valid}, SuggestionTitle: "题目"},
		"不是数据地址":   {Images: []string{"https://images.example.com/a.png"}, SuggestionTitle: "[图片1]"},
		"Base64损坏": {Images: []string{"data:image/png;base64,@@@"}, SuggestionTitle: "[图片1]"},
		"内容不是图片":   {Images: []string{inlineTestURL([]byte("text"))}, SuggestionTitle: "[图片1]"},
	} {
		t.Run(name, func(t *testing.T) {
			input.Title, input.Type = "题目", "single"
			requestJSON(t, web, "POST", "/api/search", input, 400, nil)
		})
	}
	var records struct{ Total int }
	requestJSON(t, web, "GET", "/api/records", nil, 200, &records)
	if records.Total != 0 {
		t.Fatalf("无效图片请求产生了记录: %d", records.Total)
	}
}

func TestInlineImageCacheKeepsDifferentQuestions(t *testing.T) {
	dir := t.TempDir()
	processor, err := newImageProcessor(dir)
	if err != nil {
		t.Fatal(err)
	}
	first, second := inlineTestPNG(t, 2, 2, 4), inlineTestPNG(t, 2, 2, 5)
	firstURL, err := processor.Save("[图片1]", first)
	if err != nil {
		t.Fatal(err)
	}
	secondURL, err := processor.Save("[图片1]", second)
	if err != nil || firstURL == secondURL {
		t.Fatalf("不同题目的同编号图片共用了缓存: %v", err)
	}
	for path, want := range map[string][]byte{firstURL: first, secondURL: second} {
		actual, err := os.ReadFile(filepath.Join(dir, filepath.Base(path)))
		if err != nil || !bytes.Equal(actual, want) {
			t.Fatalf("缓存图片发生覆盖: %v", err)
		}
	}
}

func TestInlineImageUsesActualFormat(t *testing.T) {
	var data bytes.Buffer
	if err := jpeg.Encode(&data, image.NewRGBA(image.Rect(0, 0, 2, 2)), nil); err != nil {
		t.Fatal(err)
	}
	actual, err := decodeInlineImage(inlineTestURL(data.Bytes()))
	if err != nil || http.DetectContentType(actual) != "image/jpeg" {
		t.Fatalf("OCS 统一的 PNG 前缀不应改变实际图片格式: %v", err)
	}
}
