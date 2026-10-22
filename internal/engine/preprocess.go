package engine

import (
	"bytes"
	"context"
	"crypto/md5"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

var imageReferencePattern = regexp.MustCompile(`(?i)https?://[^\s]+|\[图片[1-9][0-9]*\]`)

var referers = map[string]string{
	"chaoxing.com": "https://mooc1.chaoxing.com/", "xueyinonline.com": "https://mooc1.chaoxing.com/",
	"hnsyu.net": "https://mooc1.chaoxing.com/", "zhihuishu.com": "https://onlineweb.zhihuishu.com/",
	"icve.com.cn": "https://zjy2.icve.com.cn/", "icourse163.org": "https://www.icourse163.org/",
	"yuketang.cn": "https://www.yuketang.cn/",
}

const imageUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/125.0.0.0 Safari/537.36"

// ImageInfo 是一张题图在预处理阶段的公开事实。
type ImageInfo struct {
	URL         string `json:"url"`
	Description string `json:"description"`
	Src         string `json:"src"`
	Status      string `json:"status"`
}

// CaptionTask 描述图片转述任务：使用转述模型把图片转换为文字。
type CaptionTask struct {
	Run func(ctx context.Context, data []byte) (string, error)
}

// ImageProcessor 下载、校验、压缩并保存题图。
type ImageProcessor struct {
	Dir   string
	HTTP  *http.Client
	Fetch func(context.Context, string) ([]byte, error)
}

// NewImageProcessor 创建带私网地址拦截的图片处理器。
func NewImageProcessor(dir string) (*ImageProcessor, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	p := &ImageProcessor{Dir: dir}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		for _, ip := range ips {
			if ip.IP.IsPrivate() || ip.IP.IsLoopback() || ip.IP.IsLinkLocalUnicast() || ip.IP.IsUnspecified() || ip.IP.IsMulticast() {
				continue
			}
			return (&net.Dialer{Timeout: 15 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
		}
		return nil, errors.New("图片地址不可访问")
	}
	p.HTTP = &http.Client{Timeout: 30 * time.Second, Transport: transport, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("图片跳转次数过多")
		}
		return ValidateImageURL(req.URL)
	}}
	p.Fetch = p.Download
	return p, nil
}

// ValidateImageURL 拦截本机、私网和非 http(s) 图片地址。
func ValidateImageURL(u *url.URL) error {
	if (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil {
		return errors.New("图片地址无效")
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return errors.New("图片地址不可访问")
	}
	if ip := net.ParseIP(host); ip != nil && (ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() || ip.IsMulticast()) {
		return errors.New("图片地址不可访问")
	}
	return nil
}

// Download 下载并校验一张题图。
func (p *ImageProcessor) Download(ctx context.Context, raw string) ([]byte, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if err := ValidateImageURL(u); err != nil {
		return nil, err
	}
	var last error
	var result []byte
	for attempt := 0; attempt < 3; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", imageUserAgent)
		req.Header.Set("Accept", "image/avif,image/webp,image/apng,image/*,*/*;q=0.8")
		for domain, ref := range referers {
			if u.Hostname() == domain || strings.HasSuffix(u.Hostname(), "."+domain) {
				req.Header.Set("Referer", ref)
				break
			}
		}
		resp, err := p.HTTP.Do(req)
		if err != nil {
			last = err
		} else {
			func() {
				defer resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					last = fmt.Errorf("HTTP %d", resp.StatusCode)
					return
				}
				if !strings.HasPrefix(resp.Header.Get("Content-Type"), "image/") && !strings.Contains(resp.Header.Get("Content-Type"), "octet-stream") {
					last = errors.New("响应不是图片")
					return
				}
				var data []byte
				data, last = io.ReadAll(io.LimitReader(resp.Body, 20<<20+1))
				if last == nil && len(data) > 20<<20 {
					last = errors.New("图片超过 20 MB")
				}
				if last == nil && len(data) > 5<<20 {
					data, last = ShrinkImage(data)
				}
				if last == nil {
					if _, err := ImageExtension(data); err != nil {
						last = err
					} else {
						result = data
					}
				}
			}()
			if result != nil {
				return result, nil
			}
		}
		if last != nil && (strings.Contains(last.Error(), "404") || strings.Contains(last.Error(), "不可访问") || strings.Contains(last.Error(), "超过")) {
			break
		}
		if attempt < 2 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt+1) * time.Second):
			}
		}
	}
	return nil, last
}

// ShrinkImage 按上游规则把超过 1024 像素边长的图片缩小。
func ShrinkImage(data []byte) ([]byte, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if cfg.Width < 1 || cfg.Height < 1 || int64(cfg.Width)*int64(cfg.Height) > 16000000 {
		return nil, errors.New("图片尺寸无效")
	}
	original, format, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	width, height := cfg.Width, cfg.Height
	if width > 1024 || height > 1024 {
		if width >= height {
			height = max(1, height*1024/width)
			width = 1024
		} else {
			width = max(1, width*1024/height)
			height = 1024
		}
	}
	out := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.CatmullRom.Scale(out, out.Bounds(), original, original.Bounds(), draw.Over, nil)
	var b bytes.Buffer
	if format == "jpeg" {
		err = jpeg.Encode(&b, out, &jpeg.Options{Quality: 85})
	} else {
		err = png.Encode(&b, out)
	}
	return b.Bytes(), err
}

// ImageExtension 根据实际内容返回扩展名。
func ImageExtension(data []byte) (string, error) {
	switch http.DetectContentType(data) {
	case "image/jpeg":
		return ".jpg", nil
	case "image/png":
		return ".png", nil
	case "image/gif":
		return ".gif", nil
	case "image/webp":
		return ".webp", nil
	default:
		return "", errors.New("不支持的图片格式")
	}
}

// Save 将题图写入图片目录，按 URL 或内联内容去重。
func (p *ImageProcessor) Save(raw string, data []byte) (string, error) {
	ext, err := ImageExtension(data)
	if err != nil {
		return "", err
	}
	digest := md5.Sum([]byte(raw))
	if strings.HasPrefix(raw, "[图片") {
		// 同一编号在不同题目中对应不同图片，上传图片按内容缓存。
		digest = md5.Sum(data)
	}
	name := fmt.Sprintf("%x", digest)[:12] + ext
	path := filepath.Join(p.Dir, name)
	if _, err = os.Stat(path); errors.Is(err, os.ErrNotExist) {
		err = os.WriteFile(path, data, 0600)
	}
	if err != nil {
		return "", err
	}
	return "/images/" + name, nil
}

// ExtractImageReferences 按出现顺序提取图片 URL 和 [图片N] 占位符。
func ExtractImageReferences(text string) []string {
	matches := imageReferencePattern.FindAllString(text, -1)
	result := make([]string, 0, len(matches))
	for _, raw := range matches {
		if !strings.HasPrefix(raw, "[图片") {
			raw = strings.TrimRight(raw, ".,;:!?)]}\"'")
		}
		if raw != "" {
			result = append(result, raw)
		}
	}
	return result
}

// Process 处理题目中的图片。直传模式下保留原图数据交给回答模型；
// 转述模式下用转述模型生成文字描述并替换图片引用。
func (p *ImageProcessor) Process(ctx context.Context, title, options string, caption *CaptionTask, direct bool, inlineImages map[string][]byte) (string, string, []ImageInfo, map[string][]byte, error) {
	references := append(ExtractImageReferences(title), ExtractImageReferences(options)...)
	infos := []ImageInfo{}
	images := map[string][]byte{}
	if len(references) == 0 {
		return title, options, infos, images, nil
	}
	if !direct && caption == nil {
		return title, options, infos, nil, errors.New("回答模型未标注图片能力，且未配置图片转述模型")
	}
	fetch := p.Fetch
	if fetch == nil {
		fetch = p.Download
	}
	seen := map[string]bool{}
	var processErr error
	for _, raw := range references {
		if seen[raw] {
			continue
		}
		seen[raw] = true
		info := ImageInfo{URL: raw, Status: "failed"}
		data, uploaded := inlineImages[raw]
		var err error
		if !uploaded {
			if strings.HasPrefix(raw, "[图片") {
				err = errors.New("缺少上传的图片数据")
			} else {
				data, err = fetch(ctx, raw)
			}
		}
		if err == nil {
			info.Src, err = p.Save(raw, data)
		}
		if err == nil {
			images[raw] = data
			if direct {
				if uploaded {
					info.Description = "已接收"
				} else {
					info.Description = "已下载"
				}
			} else {
				info.Description, err = caption.Run(ctx, data)
				info.Description = strings.TrimSpace(info.Description)
				if err == nil && info.Description == "" {
					err = errors.New("未识别")
				}
			}
		}
		if err != nil {
			info.Description = ClassifyImageError(err)
			processErr = errors.Join(processErr, fmt.Errorf("第 %d 张图片处理失败: %w", len(infos)+1, err))
		} else {
			info.Status = "done"
		}
		infos = append(infos, info)
		if !direct {
			title = strings.ReplaceAll(title, raw, "[图片: "+info.Description+"]")
			options = strings.ReplaceAll(options, raw, "[图片: "+info.Description+"]")
		}
	}
	return title, options, infos, images, processErr
}

// ClassifyImageError 把图片错误折叠为可展示的短文本。
func ClassifyImageError(err error) string {
	s := err.Error()
	switch {
	case strings.Contains(s, "404"):
		return "(图片已失效)"
	case strings.Contains(s, "403"):
		return "(图片拒绝访问)"
	case strings.Contains(s, "空白"):
		return "(空白)"
	default:
		return "(未识别)"
	}
}
