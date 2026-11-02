package panel

import (
	"strings"

	modelmetadata "github.com/onprs/OCS-Tiku/internal/catalog"
	"github.com/onprs/OCS-Tiku/internal/platform/modelclient"
)

func enrichDiscoveredModels(items []modelclient.DiscoveredModel, vendor string) []modelclient.DiscoveredModel {
	metadata, err := modelmetadata.Builtin()
	if err != nil {
		return items
	}
	vendor = strings.ToLower(strings.TrimSpace(vendor))
	for index := range items {
		match := metadata.Lookup(vendor, items[index].ID)
		if !match.Found {
			continue
		}
		entry := match.Entry
		items[index].MetadataMatched = true
		if entry.Name != "" {
			items[index].DisplayName = entry.Name
		}
		items[index].Context = entry.Context
		capabilities := []string{"text"}
		if entry.Vision {
			capabilities = append(capabilities, "vision")
		}
		if entry.ToolCall {
			capabilities = append(capabilities, "tool_call")
		}
		if entry.Reasoning {
			capabilities = append(capabilities, "reasoning")
		}
		items[index].Capabilities = capabilities
	}
	return items
}
