package ui

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"claude-switch/pkg/extractor"
)

func FormatRelativeTime(isoTime string) string {
	if isoTime == "" {
		return ""
	}
	t, err := time.Parse("2006-01-02T15:04:05", isoTime)
	if err != nil {
		if len(isoTime) >= 16 {
			return isoTime[11:16]
		}
		return isoTime
	}

	now := time.Now()
	diff := now.Sub(t)

	if diff < 1*time.Minute {
		return "vừa xong"
	} else if diff < 60*time.Minute {
		return fmt.Sprintf("%d phút trước", int(diff.Minutes()))
	} else if diff < 24*time.Hour && t.Day() == now.Day() {
		return fmt.Sprintf("%s hôm nay", t.Format("15:04"))
	} else if diff < 48*time.Hour {
		return fmt.Sprintf("%s hôm qua", t.Format("15:04"))
	}
	return fmt.Sprintf("%d ngày trước", int(diff.Hours()/24))
}

func PickConversation(conversations []extractor.ConversationSummary) (*extractor.ConversationSummary, bool) {
	if len(conversations) == 0 {
		fmt.Println("Không tìm thấy cuộc trò chuyện nào trong profile hiện tại.")
		return nil, false
	}

	fmt.Println("\n=== CHỌN ĐOẠN CHAT ĐỂ TIẾP TỤC BỐI CẢNH (HANDOFF) ===")
	limit := 5
	if len(conversations) < limit {
		limit = len(conversations)
	}

	for i := 0; i < limit; i++ {
		c := conversations[i]
		tag := ""
		if i == 0 {
			tag = " [Mới nhất]"
		}
		timeStr := FormatRelativeTime(c.UpdatedAt)
		if timeStr != "" {
			timeStr = fmt.Sprintf(" (%s)", timeStr)
		}
		fmt.Printf("  [%d] %s%s%s\n", i+1, c.Title, timeStr, tag)
	}
	fmt.Println("  [0] Bỏ qua (chỉ chuyển tài khoản, không handoff)")
	fmt.Printf("\nChọn [1]: ")

	reader := bufio.NewReader(os.Stdin)
	input, _ := reader.ReadString('\n')
	input = strings.TrimSpace(input)

	if input == "" || input == "1" {
		return &conversations[0], true
	}
	if input == "0" {
		return nil, false
	}

	idx, err := strconv.Atoi(input)
	if err == nil && idx >= 1 && idx <= limit {
		return &conversations[idx-1], true
	}

	fmt.Println("Lựa chọn không hợp lệ. Mặc định chọn đoạn chat mới nhất.")
	return &conversations[0], true
}
