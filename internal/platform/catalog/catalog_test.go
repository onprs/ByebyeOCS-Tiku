package catalog

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestPublicIDAndPricingValidation(t *testing.T) {
	id := NewPublicID()
	if !ValidPublicID(id) {
		t.Fatalf("生成的公开编号无效：%s", id)
	}
	for _, value := range []string{"", "qm_", "qm_0123456789abcdef0123456789abcde", "qm_0123456789ABCDEF0123456789ABCDEF", "xx_0123456789abcdef0123456789abcdef"} {
		if ValidPublicID(value) {
			t.Fatalf("无效公开编号被接受：%s", value)
		}
	}
	price := &Pricing{Version: "v1", InputMicrosPerMillionTokens: 1_000_000, OutputMicrosPerMillionTokens: 2_000_000,
		MicrosPerMinor: 10000, Rounding: "ceil", Rule: "tokens_x_micros_per_million_v1"}
	if err := price.Validate(); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(price)
	if err != nil || string(encoded) != `{"version":"v1","input_micros_per_million_tokens":"1000000","output_micros_per_million_tokens":"2000000","micros_per_minor":"10000","rounding":"ceil","rule":"tokens_x_micros_per_million_v1"}` {
		t.Fatalf("价格金额必须使用十进制字符串：%s/%v", encoded, err)
	}
	var decoded Pricing
	if err := json.Unmarshal(encoded, &decoded); err != nil || decoded.InputMicrosPerMillionTokens != price.InputMicrosPerMillionTokens {
		t.Fatalf("价格金额字符串往返失败：%+v/%v", decoded, err)
	}
	invalid := []Pricing{
		{Version: "", InputMicrosPerMillionTokens: 1, OutputMicrosPerMillionTokens: 1, MicrosPerMinor: 10000, Rounding: "ceil", Rule: "x"},
		{Version: "v1", InputMicrosPerMillionTokens: 1, OutputMicrosPerMillionTokens: 1, MicrosPerMinor: 100, Rounding: "ceil", Rule: "x"},
		{Version: "v1", InputMicrosPerMillionTokens: 1, OutputMicrosPerMillionTokens: 1, MicrosPerMinor: 10000, Rounding: "half_up", Rule: "x"},
	}
	for _, item := range invalid {
		if err := item.Validate(); err == nil {
			t.Fatalf("无效价格被接受：%+v", item)
		}
	}
}

func TestOfferingNormalize(t *testing.T) {
	item := Offering{
		DisplayName: " 标准模型 ", Description: " 说明 ", ProviderRef: " standard ",
		Capabilities: []string{"vision", "text", "vision"}, Pricing: nil,
		CachePolicy: CachePolicy{Allow: true, Scope: "user", TTLDays: 7}, Enabled: true,
	}
	profile := ModelProfile{Vision: true}
	if err := item.Normalize(profile, 30, true); err != nil {
		t.Fatal(err)
	}
	if item.DisplayName != "标准模型" || item.Description != "说明" || item.ProviderRef != "standard" {
		t.Fatalf("输入未清理：%+v", item)
	}
	if len(item.Capabilities) != 2 || item.Capabilities[0] != "text" || item.Capabilities[1] != "vision" {
		t.Fatalf("能力未去重排序：%+v", item.Capabilities)
	}
	cases := []Offering{
		{DisplayName: "模型", ProviderRef: "standard", Capabilities: []string{"vision"}, CachePolicy: CachePolicy{Scope: "user", TTLDays: 7}, Enabled: true},
		{DisplayName: "模型", ProviderRef: "standard", CachePolicy: CachePolicy{Scope: "shared", TTLDays: 7}, Enabled: true},
		{DisplayName: "模型", ProviderRef: "standard", CachePolicy: CachePolicy{Scope: "user", TTLDays: 31}, Enabled: true},
		{DisplayName: "模型", ProviderRef: "standard", CachePolicy: CachePolicy{Scope: "user", TTLDays: 7}, Enabled: false, Default: true},
	}
	for index, candidate := range cases {
		shared := index != 1
		if err := candidate.Normalize(ModelProfile{}, 30, shared); err == nil {
			t.Fatalf("非法公开条目被接受：%d %+v", index, candidate)
		} else {
			var validation *ValidationError
			if !errors.As(err, &validation) {
				t.Fatalf("校验错误类型不符：%T", err)
			}
		}
	}
	if EffectiveHealth(Offering{}, true, true) != HealthUnavailable || EffectiveHealth(Offering{}, false, false) != HealthUnavailable {
		t.Fatal("停用或缺失的模型必须为不可用")
	}
	if EffectiveHealth(Offering{}, true, false) != HealthConfigured {
		t.Fatal("已配置且启用的模型应为可用")
	}
}
