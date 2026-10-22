package handoff

import (
	"fmt"
	"os/exec"
	"strings"
)

func SetClipboard(text string) error {
	cmd := exec.Command("pbcopy")
	cmd.Stdin = strings.NewReader(text)
	return cmd.Run()
}

func BuildDirective(latestFilePath string) string {
	return fmt.Sprintf("Hãy dùng filesystem tool đọc file %s để nắm toàn bộ bối cảnh dự án, các artifacts đã tạo và công việc đang làm dở, sau đó tiếp tục thực hiện bước tiếp theo.", latestFilePath)
}
