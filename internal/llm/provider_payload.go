package llm

func applyReasoningEffort(payload map[string]any, provider ProviderConfig) {
	if provider.ReasoningEffort == "" {
		return
	}
	payload["reasoning_effort"] = provider.ReasoningEffort
}
