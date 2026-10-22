package handoff

import (
	"fmt"
	"strings"
	"testing"

	"claude-switch/pkg/config"
	"claude-switch/pkg/extractor"
)

func TestMaxRecentMessages(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Handoff.MaxRecentMessages = 3

	detail := &extractor.ConversationDetail{
		Summary: extractor.ConversationSummary{
			Title: "Test Conversation",
		},
	}
	for i := 1; i <= 10; i++ {
		detail.Messages = append(detail.Messages, extractor.Message{
			Sender: "human",
			Text:   fmt.Sprintf("Message number %d", i),
		})
	}

	prompt := buildLLMPrompt(cfg, detail, "test@example.com")

	// Must NOT contain Message number 1 to 7
	if strings.Contains(prompt, "Message number 1\n") || strings.Contains(prompt, "Message number 7\n") {
		t.Errorf("prompt contains older messages when MaxRecentMessages=3")
	}

	// MUST contain Message number 8, 9, 10
	if !strings.Contains(prompt, "Message number 8") || !strings.Contains(prompt, "Message number 10") {
		t.Errorf("prompt missing recent messages 8, 9, 10")
	}
}

func TestIncludeArtifactsFlag(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Handoff.IncludeArtifacts = false

	detail := &extractor.ConversationDetail{
		Summary: extractor.ConversationSummary{
			Title: "Test Artifacts Omission",
		},
		Artifacts: []extractor.Artifact{
			{
				Identifier: "secret-code",
				Title:      "Secret Component",
				Language:   "typescript",
				Content:    "const secret = 'do_not_leak';",
			},
		},
	}

	// 1. In LLM prompt
	prompt := buildLLMPrompt(cfg, detail, "test@example.com")
	if strings.Contains(prompt, "secret-code") || strings.Contains(prompt, "do_not_leak") {
		t.Errorf("LLM prompt contains artifact when IncludeArtifacts=false")
	}

	// 2. In Rule-based markdown
	md := GenerateRuleBasedMarkdown(cfg, detail, "test@example.com")
	if strings.Contains(md, "secret-code") || strings.Contains(md, "do_not_leak") {
		t.Errorf("Markdown contains artifact when IncludeArtifacts=false")
	}
}
