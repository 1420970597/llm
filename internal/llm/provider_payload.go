package llm

func applyReasoningEffort(payload map[string]any, provider ProviderConfig) {
	effort := provider.ReasoningEffort
	if effort == "" {
		return
	}
	if provider.Model != "" {
		switch effort {
		case "xhigh":
			effort = "high"
		}
	}
	payload["reasoning_effort"] = effort
}
