package main

// protocols 是全部可用协议。
var protocols = []string{
	ProtocolOpenAIChat, ProtocolAnthropicMessages, ProtocolGeminiGenerate, ProtocolOllamaChat,
}

// protocolLabel 返回协议的中文说明，用于设置页展示。
func protocolLabel(protocol string) string {
	switch protocol {
	case ProtocolOpenAIChat:
		return "OpenAI 兼容"
	case ProtocolAnthropicMessages:
		return "Anthropic Messages"
	case ProtocolGeminiGenerate:
		return "Google Gemini"
	case ProtocolOllamaChat:
		return "Ollama"
	default:
		return protocol
	}
}

// ServiceTemplate 是预置服务条目模板，用户填入密钥后即可使用。
type ServiceTemplate struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
	BaseURL  string `json:"base_url"`
	Vendor   string `json:"vendor"`
	Doc      string `json:"doc"`
}

// serviceTemplates 是内置的服务条目模板，端点取自各厂商官方文档。
var serviceTemplates = []ServiceTemplate{
	// 国际厂商
	{ID: "openai", Name: "OpenAI", Protocol: ProtocolOpenAIChat, BaseURL: "https://api.openai.com/v1", Vendor: "openai", Doc: "https://platform.openai.com/docs/models"},
	{ID: "anthropic", Name: "Anthropic", Protocol: ProtocolAnthropicMessages, BaseURL: "https://api.anthropic.com", Vendor: "anthropic", Doc: "https://docs.anthropic.com/en/docs/about-claude/models"},
	{ID: "google", Name: "Google Gemini", Protocol: ProtocolGeminiGenerate, Vendor: "google", Doc: "https://ai.google.dev/gemini-api/docs/models"},
	{ID: "xai", Name: "xAI", Protocol: ProtocolOpenAIChat, BaseURL: "https://api.x.ai/v1", Vendor: "xai", Doc: "https://docs.x.ai/docs/models"},
	{ID: "mistral", Name: "Mistral", Protocol: ProtocolOpenAIChat, BaseURL: "https://api.mistral.ai/v1", Vendor: "mistral", Doc: "https://docs.mistral.ai/getting-started/models/"},
	{ID: "cohere", Name: "Cohere", Protocol: ProtocolOpenAIChat, BaseURL: "https://api.cohere.ai/compatibility/v1", Vendor: "cohere", Doc: "https://docs.cohere.com/docs/models"},
	{ID: "groq", Name: "Groq", Protocol: ProtocolOpenAIChat, BaseURL: "https://api.groq.com/openai/v1", Vendor: "groq", Doc: "https://console.groq.com/docs/models"},
	{ID: "deepinfra", Name: "DeepInfra", Protocol: ProtocolOpenAIChat, BaseURL: "https://api.deepinfra.com/v1/openai", Vendor: "deepinfra", Doc: "https://deepinfra.com/models"},
	{ID: "togetherai", Name: "Together AI", Protocol: ProtocolOpenAIChat, BaseURL: "https://api.together.xyz/v1", Vendor: "togetherai", Doc: "https://docs.together.ai/docs/serverless-models"},
	{ID: "perplexity", Name: "Perplexity", Protocol: ProtocolOpenAIChat, BaseURL: "https://api.perplexity.ai", Vendor: "perplexity", Doc: "https://docs.perplexity.ai"},
	{ID: "openrouter", Name: "OpenRouter", Protocol: ProtocolOpenAIChat, BaseURL: "https://openrouter.ai/api/v1", Vendor: "openrouter", Doc: "https://openrouter.ai/models"},

	// 国内厂商
	{ID: "deepseek", Name: "DeepSeek", Protocol: ProtocolOpenAIChat, BaseURL: "https://api.deepseek.com", Vendor: "deepseek", Doc: "https://api-docs.deepseek.com"},
	{ID: "alibaba", Name: "通义千问（国际）", Protocol: ProtocolOpenAIChat, BaseURL: "https://dashscope-intl.aliyuncs.com/compatible-mode/v1", Vendor: "alibaba", Doc: "https://www.alibabacloud.com/help/en/model-studio/models"},
	{ID: "alibaba-cn", Name: "通义千问", Protocol: ProtocolOpenAIChat, BaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1", Vendor: "alibaba-cn", Doc: "https://help.aliyun.com/zh/model-studio"},
	{ID: "moonshotai", Name: "Kimi（国际）", Protocol: ProtocolOpenAIChat, BaseURL: "https://api.moonshot.ai/v1", Vendor: "moonshotai", Doc: "https://platform.moonshot.ai/docs/api/chat"},
	{ID: "moonshotai-cn", Name: "Kimi", Protocol: ProtocolOpenAIChat, BaseURL: "https://api.moonshot.cn/v1", Vendor: "moonshotai-cn", Doc: "https://platform.moonshot.cn/docs/api/chat"},
	{ID: "zhipuai", Name: "智谱 GLM", Protocol: ProtocolOpenAIChat, BaseURL: "https://open.bigmodel.cn/api/paas/v4", Vendor: "zhipuai", Doc: "https://docs.bigmodel.cn"},
	{ID: "minimax", Name: "MiniMax（国际）", Protocol: ProtocolOpenAIChat, BaseURL: "https://api.minimax.io/v1", Vendor: "minimax", Doc: "https://platform.minimax.io/docs/guides/quickstart"},
	{ID: "minimax-cn", Name: "MiniMax", Protocol: ProtocolOpenAIChat, BaseURL: "https://api.minimax.cn/v1", Vendor: "minimax-cn", Doc: "https://platform.minimaxi.com/docs/guides/quickstart"},
	{ID: "volcengine", Name: "火山方舟", Protocol: ProtocolOpenAIChat, BaseURL: "https://ark.cn-beijing.volces.com/api/v3", Vendor: "volcengine", Doc: "https://www.volcengine.com/docs/82379"},
	{ID: "stepfun-ai", Name: "阶跃星辰（国际）", Protocol: ProtocolOpenAIChat, BaseURL: "https://api.stepfun.ai/v1", Vendor: "stepfun-ai", Doc: "https://platform.stepfun.ai/docs/en/overview/concept"},
	{ID: "stepfun", Name: "阶跃星辰", Protocol: ProtocolOpenAIChat, BaseURL: "https://api.stepfun.com/v1", Vendor: "stepfun", Doc: "https://platform.stepfun.com/docs/zh/overview/concept"},
	{ID: "siliconflow", Name: "SiliconFlow（国际）", Protocol: ProtocolOpenAIChat, BaseURL: "https://api.siliconflow.com/v1", Vendor: "siliconflow", Doc: "https://cloud.siliconflow.com/models"},
	{ID: "siliconflow-cn", Name: "硅基流动", Protocol: ProtocolOpenAIChat, BaseURL: "https://api.siliconflow.cn/v1", Vendor: "siliconflow-cn", Doc: "https://cloud.siliconflow.cn/models"},
	{ID: "modelscope", Name: "魔搭 ModelScope", Protocol: ProtocolOpenAIChat, BaseURL: "https://api-inference.modelscope.cn/v1", Vendor: "modelscope", Doc: "https://modelscope.cn/docs/model-service/API-Inference/intro"},
	{ID: "longcat", Name: "LongCat", Protocol: ProtocolOpenAIChat, BaseURL: "https://api.longcat.chat/openai", Vendor: "longcat", Doc: "https://longcat.chat/platform/docs/"},
	{ID: "sensenova", Name: "商汤日日新", Protocol: ProtocolOpenAIChat, BaseURL: "https://api.sensenova.cn/compatible-mode/v2", Vendor: "sensenova", Doc: "https://platform.sensenova.cn/docs"},
	{ID: "xiaomi", Name: "小米 MiMo", Protocol: ProtocolOpenAIChat, BaseURL: "https://api.xiaomimimo.com/v1", Vendor: "xiaomi", Doc: "https://platform.xiaomimimo.com/#/docs"},
	{ID: "tencent", Name: "腾讯混元", Protocol: ProtocolOpenAIChat, BaseURL: "https://api.hunyuan.cloud.tencent.com/v1", Vendor: "tencent-tokenhub", Doc: "https://cloud.tencent.com/document/product/1729"},
	{ID: "inclusionai", Name: "蚂蚁百灵", Protocol: ProtocolOpenAIChat, BaseURL: "https://api.ant-ling.com/v1", Vendor: "inclusionai", Doc: "https://developer.ant-ling.com"},

	// 本地部署
	{ID: "ollama", Name: "Ollama（本地）", Protocol: ProtocolOllamaChat, BaseURL: "http://127.0.0.1:11434", Vendor: "", Doc: "https://docs.ollama.com"},
}

// findServiceTemplate 按标识查找预置模板。
func findServiceTemplate(id string) (ServiceTemplate, bool) {
	for _, template := range serviceTemplates {
		if template.ID == id {
			return template, true
		}
	}
	return ServiceTemplate{}, false
}
