package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"claude-switch/pkg/config"
	"claude-switch/pkg/extractor"
	"claude-switch/pkg/handoff"
	"claude-switch/pkg/llm"
	"claude-switch/pkg/profile"
	"claude-switch/pkg/storage"
	"claude-switch/pkg/ui"
)

const version = "1.0.0"

func main() {
	if len(os.Args) < 2 {
		printUsage()
		return
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	switch cmd {
	case "use":
		handleUse(args)
	case "restart":
		handleRestart(args)
	case "list", "ls":
		handleList(args)
	case "current":
		handleCurrent(args)
	case "handoff":
		handleHandoff(args)
	case "add":
		handleAdd(args)
	case "remove", "rm":
		handleRemove(args)
	case "rename-by-email":
		handleRenameByEmail()
	case "config":
		handleConfig(args)
	case "doctor":
		handleDoctor()
	case "recover":
		handleRecover()
	case "storage":
		handleStorage(args)
	case "menu":
		handleMenu(args)
	case "-v", "--version", "version":
		fmt.Printf("claude-switch version %s\n", version)
	case "-h", "--help", "help":
		printUsage()
	default:
		// Check if first arg is a profile name to switch directly
		if p, err := profile.ResolveProfile(cmd); err == nil {
			_ = profile.SwitchToProfile(p, false)
			return
		}
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Print(`claude-switch: Trình quản lý nhiều tài khoản Claude Desktop & Context Handoff Engine

Sử dụng:
  claude-switch use <tên|email> [-h|--handoff] [--latest] [--force]
                                Chuyển sang profile tương ứng (tùy chọn tạo handoff context)
  claude-switch list [--json]   Xem danh sách các tài khoản (* = đang dùng)
  claude-switch current         Xem tài khoản hiện tại
  claude-switch handoff         Trích xuất bối cảnh phiên chat hiện tại ra Markdown
  claude-switch handoff copy    Sao chép lại câu lệnh chỉ dẫn handoff vào clipboard
  claude-switch handoff open    Mở file snapshot handoff gần nhất
  claude-switch handoff list    Xem danh sách các bản snapshot handoff
  claude-switch handoff prune   Dọn dẹp các snapshot cũ theo cấu hình retention
  claude-switch handoff pin <f> Ghim snapshot quan trọng không cho tự động dọn dẹp
  claude-switch add [tên]       Tạo tài khoản mới và mở app để đăng nhập
  claude-switch remove <tên>    Xóa một profile
  claude-switch rename-by-email Quét và đổi tên thư mục các profile cũ theo email
  claude-switch config          Xem hoặc sửa cấu hình LLM / Handoff
  claude-switch doctor          Kiểm tra symlink, quyền extension và trạng thái app
  claude-switch recover         Khôi phục phiên chuyển đổi nếu bị gián đoạn giữa chừng
  claude-switch storage         Xem dung lượng profile và cache có thể dọn
  claude-switch storage clean   Xem trước cache có thể dọn; thêm --yes để xóa
  claude-switch menu            Bật icon menu bar trên header desktop macOS

Tùy chọn chung:
  -h, --handoff     Bật menu chọn đoạn chat để chuyển context trước khi đổi tài khoản
  --latest          Tự động lấy đoạn chat mới nhất mà không hiển thị menu chọn
  --force           Buộc tắt Claude Desktop nếu app không phản hồi
  --allow-switch-without-handoff  Cho phép switch tiếp ngay cả khi tạo handoff thất bại
`)
}

func handleStorage(args []string) {
	if len(args) > 0 && args[0] == "clean" {
		clean := storage.CleanCandidates(config.GetProfilesDir())
		if len(clean) == 0 {
			fmt.Println("Không tìm thấy cache có thể dọn.")
			return
		}

		if !hasFlag(args[1:], "--yes") {
			fmt.Println("Xem trước cache có thể tạo lại (chưa xóa gì):")
			for _, item := range clean {
				fmt.Printf("  %s / %s  %s\n", item.Profile, item.Name, storage.FormatBytes(item.Bytes))
			}
			fmt.Printf("Có thể giải phóng khoảng %s. Chạy `claude-switch storage clean --yes` để xóa sau khi đã đóng Claude.\n", storage.FormatBytes(storage.TotalCandidateBytes(clean)))
			return
		}

		if profile.IsRunning() {
			fmt.Println("Lỗi: Hãy đóng Claude Desktop hoàn toàn trước khi dọn cache.")
			os.Exit(1)
		}
		removed, freed, err := storage.Clean(config.GetProfilesDir())
		for _, path := range removed {
			fmt.Printf("Đã dọn: %s\n", path)
		}
		if err != nil {
			fmt.Printf("Lỗi khi dọn cache: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Đã dọn %s cache, giải phóng khoảng %s.\n", strconv.Itoa(len(removed)), storage.FormatBytes(freed))
		return
	}

	reports, err := storage.Scan(config.GetProfilesDir())
	if err != nil {
		fmt.Printf("Lỗi khi đọc dung lượng profile: %v\n", err)
		os.Exit(1)
	}
	if len(reports) == 0 {
		fmt.Println("Chưa có profile nào.")
		return
	}
	fmt.Println("=== DUNG LƯỢNG CLAUDE PROFILES ===")
	var grandTotal, cacheTotal int64
	for _, report := range reports {
		grandTotal += report.TotalBytes
		cacheTotal += report.CacheBytes
		fmt.Printf("%s  tổng %s, cache %s\n", report.Name, storage.FormatBytes(report.TotalBytes), storage.FormatBytes(report.CacheBytes))
		for _, dir := range report.Largest {
			fmt.Printf("  %-32s %s\n", dir.Name, storage.FormatBytes(dir.Bytes))
		}
	}
	fmt.Printf("Tổng: %s; cache có thể dọn: khoảng %s\n", storage.FormatBytes(grandTotal), storage.FormatBytes(cacheTotal))
}

func handleList(args []string) {
	asJSON := hasFlag(args, "--json")
	profiles, err := profile.ListProfiles()
	if err != nil {
		fmt.Printf("Lỗi: %v\n", err)
		os.Exit(1)
	}

	if asJSON {
		b, _ := json.MarshalIndent(profiles, "", "  ")
		fmt.Println(string(b))
		return
	}

	if len(profiles) == 0 {
		fmt.Println("Chưa có profile nào. Hãy chạy: claude-switch add")
		return
	}

	for _, p := range profiles {
		prefix := "  "
		if p.IsActive {
			prefix = "* "
		}
		displayName := p.Name
		extra := ""
		if p.AccountInfo != nil {
			if p.AccountInfo.Email != "" && p.AccountInfo.Email != p.Name {
				displayName = fmt.Sprintf("%s (%s)", p.Name, p.AccountInfo.Email)
			} else if p.AccountInfo.FullName != "" {
				extra = fmt.Sprintf(" [%s]", p.AccountInfo.FullName)
			}
		}
		fmt.Printf("%s%s%s\n", prefix, displayName, extra)
	}
}

func handleCurrent(args []string) {
	asJSON := hasFlag(args, "--json")
	active := profile.GetActiveProfileName()
	if active == "" {
		if asJSON {
			fmt.Println(`{"active": null}`)
		} else {
			fmt.Println("(chưa thiết lập profile nào)")
		}
		return
	}

	p, err := profile.ResolveProfile(active)
	if err != nil || p == nil {
		if asJSON {
			fmt.Printf(`{"name": "%s"}`+"\n", active)
		} else {
			fmt.Println(active)
		}
		return
	}

	if asJSON {
		b, _ := json.Marshal(p)
		fmt.Println(string(b))
	} else {
		name := p.Name
		if p.AccountInfo != nil && p.AccountInfo.Email != "" {
			name = fmt.Sprintf("%s (%s)", p.Name, p.AccountInfo.Email)
		}
		fmt.Println(name)
	}
}

func handleRestart(args []string) {
	force := hasFlag(args, "--force")
	if err := profile.RestartApp(force); err != nil {
		fmt.Printf("Lỗi: %v\n", err)
		os.Exit(1)
	}
}

func handleUse(args []string) {
	if len(args) == 0 {
		fmt.Println("Lỗi: thiếu tên hoặc email tài khoản cần chuyển. Ví dụ: claude-switch use personal")
		os.Exit(1)
	}

	targetQuery := args[0]
	force := hasFlag(args, "--force")

	if targetQuery == "current" || targetQuery == "restart" {
		handleRestart(args)
		return
	}

	lock, err := profile.AcquireSwitchLock()
	if err != nil {
		fmt.Printf("Lỗi: %v\n", err)
		os.Exit(1)
	}
	defer lock.Release()

	doHandoff := hasFlag(args, "-h") || hasFlag(args, "--handoff")
	useLatest := hasFlag(args, "--latest")
	allowWithoutHandoff := hasFlag(args, "--allow-switch-without-handoff")

	targetProfile, err := profile.ResolveProfile(targetQuery)
	if err != nil {
		fmt.Printf("Lỗi: %v\n", err)
		os.Exit(1)
	}

	activeName := profile.GetActiveProfileName()
	var currentProfile *profile.ProfileItem
	if activeName != "" {
		currentProfile, _ = profile.ResolveProfile(activeName)
	}

	opID := fmt.Sprintf("op_%d", time.Now().Unix())
	sourceName := ""
	if currentProfile != nil {
		sourceName = currentProfile.Name
	}
	if err := profile.RecordJournalStep(opID, sourceName, targetProfile.Name, "", profile.StepStarted); err != nil {
		fmt.Printf("Lỗi ghi journal: %v\n", err)
		os.Exit(1)
	}

	var handoffPath string
	if doHandoff && currentProfile != nil && currentProfile.Name != targetProfile.Name {
		result, err := executeHandoffFlow(currentProfile, targetProfile, useLatest, force, allowWithoutHandoff)
		if err != nil {
			fmt.Printf("Lỗi Handoff: %v\n", err)
			os.Exit(1)
		}
		if result != nil && result.SnapshotPath != "" {
			handoffPath = result.SnapshotPath
			if err := profile.RecordJournalStep(opID, sourceName, targetProfile.Name, handoffPath, profile.StepHandoffDone); err != nil {
				fmt.Printf("Lỗi ghi journal: %v\n", err)
				os.Exit(1)
			}
		}
	}

	if err := profile.SwitchToProfile(targetProfile, force); err != nil {
		fmt.Printf("Lỗi: %v\n", err)
		os.Exit(1)
	}

	if err := profile.RecordJournalStep(opID, sourceName, targetProfile.Name, handoffPath, profile.StepSymlinkUpdated); err != nil {
		fmt.Printf("Lỗi ghi journal: %v\n", err)
		os.Exit(1)
	}

	if profile.GetActiveProfileName() != targetProfile.Name {
		fmt.Printf("⚠️  Cảnh báo: Symlink chưa trỏ đúng về profile đích %s\n", targetProfile.Name)
	} else {
		_ = profile.RecordJournalStep(opID, sourceName, targetProfile.Name, handoffPath, profile.StepVerified)
		profile.ClearJournal()
	}
}

func handleRecover() {
	lock, err := profile.AcquireSwitchLock()
	if err != nil {
		fmt.Printf("Lỗi: %v\n", err)
		os.Exit(1)
	}
	defer lock.Release()

	j, err := profile.ReadJournal()
	if err != nil || j == nil {
		fmt.Println("Không có phiên chuyển đổi nào bị dở dang cần phục hồi.")
		return
	}
	fmt.Printf("Phát hiện phiên chuyển đổi chưa hoàn tất: %s (%s -> %s, bước: %s)\n", j.OperationID, j.Source, j.Target, j.Step)
	targetProfile, err := profile.ResolveProfile(j.Target)
	if err != nil {
		fmt.Printf("Lỗi: Không tìm thấy profile đích '%s': %v\n", j.Target, err)
		return
	}
	fmt.Printf("Đang hoàn tất chuyển đổi sang profile '%s'...\n", targetProfile.Name)
	if err := profile.SwitchToProfile(targetProfile, true); err != nil {
		fmt.Printf("Lỗi phục hồi: %v\n", err)
		return
	}
	profile.ClearJournal()
	fmt.Println("✓ Đã phục hồi thành công!")
}

func executeHandoffFlow(currentProfile, targetProfile *profile.ProfileItem, useLatest, force, allowWithoutHandoff bool) (*handoff.HandoffResult, error) {
	fmt.Println("Đang kiểm tra dữ liệu hội thoại trong tài khoản hiện tại...")
	convs, err := extractor.ListConversations(currentProfile.Path)
	if err != nil || len(convs) == 0 {
		if allowWithoutHandoff {
			fmt.Println("Cảnh báo: Không tìm thấy hội thoại để handoff. Tiếp tục switch do có cờ --allow-switch-without-handoff.")
			return &handoff.HandoffResult{Status: handoff.StatusSkipped}, nil
		}
		return nil, fmt.Errorf("không tìm thấy hội thoại nào gần đây để handoff. Đã hủy chuyển tài khoản để bảo toàn dữ liệu (thêm --allow-switch-without-handoff nếu muốn bỏ qua)")
	}

	var chosen *extractor.ConversationSummary
	if useLatest {
		chosen = &convs[0]
	} else {
		picked, ok := ui.PickConversation(convs)
		if !ok || picked == nil {
			fmt.Println("Người dùng đã chọn bỏ qua handoff. Tiếp tục chuyển tài khoản...")
			return &handoff.HandoffResult{Status: handoff.StatusSkipped}, nil
		}
		chosen = picked
	}

	// Now close Claude Desktop before reading and writing DB
	wasRunning := profile.IsRunning()
	if wasRunning {
		fmt.Println("Đang đóng Claude Desktop để đồng bộ trạng thái database...")
		if err := profile.QuitApp(force); err != nil {
			if allowWithoutHandoff {
				fmt.Printf("Cảnh báo: Không thể đóng Claude Desktop (%v). Bỏ qua handoff và tiếp tục switch do có cờ --allow-switch-without-handoff.\n", err)
				return &handoff.HandoffResult{Status: handoff.StatusSkipped}, nil
			} else {
				return nil, fmt.Errorf("không thể đóng Claude Desktop trước khi đọc database: %w. Đã hủy chuyển tài khoản", err)
			}
		}
	}

	// For standalone handoff (target is current profile), reopen Claude if it was running
	if currentProfile.Name == targetProfile.Name && wasRunning {
		defer func() {
			fmt.Println("Đang mở lại Claude Desktop...")
			_ = exec.Command("open", "-a", "Claude").Run()
		}()
	}

	fmt.Printf("Đang trích xuất và tạo Handoff cho: '%s'...\n", chosen.Title)
	detail, err := extractor.GetConversationDetail(currentProfile.Path, chosen.UUID)
	if err != nil {
		if allowWithoutHandoff {
			fmt.Printf("Cảnh báo: Trích xuất hội thoại thất bại (%v). Tiếp tục switch do có cờ --allow-switch-without-handoff.\n", err)
			return &handoff.HandoffResult{Status: handoff.StatusExtractionFailed, Error: err.Error()}, nil
		}
		return nil, fmt.Errorf("trích xuất hội thoại thất bại: %w. Đã hủy chuyển tài khoản để bảo toàn dữ liệu", err)
	}

	cfg, _ := config.LoadConfig()
	fromAccount := currentProfile.Name
	if currentProfile.AccountInfo != nil && currentProfile.AccountInfo.Email != "" {
		fromAccount = currentProfile.AccountInfo.Email
	}

	res, err := handoff.CreateHandoff(cfg, detail, fromAccount)
	if err != nil {
		if allowWithoutHandoff {
			fmt.Printf("Cảnh báo: Tạo file handoff thất bại (%v). Tiếp tục switch do có cờ --allow-switch-without-handoff.\n", err)
			return &handoff.HandoffResult{Status: handoff.StatusWriteFailed, Error: err.Error()}, nil
		}
		return nil, fmt.Errorf("tạo gói handoff thất bại: %w. Đã hủy chuyển tài khoản để bảo vệ bối cảnh", err)
	}

	fmt.Printf("✓ Đã tạo snapshot handoff: %s\n", res.SnapshotPath)
	if res.Status == handoff.StatusClipboardFailed {
		fmt.Println("⚠️  Lưu ý: Không thể ghi vào Clipboard macOS. Bạn có thể xem file trực tiếp hoặc chạy `claude-switch handoff copy`.")
	} else {
		fmt.Println("✓ Đã cập nhật symlink latest.md và copy câu lệnh chỉ dẫn vào Clipboard!")
		fmt.Println("  (Sau khi Claude mở lên, bạn chỉ cần nhấn Cmd+V và Enter).")
	}

	return res, nil
}

func handleHandoff(args []string) {
	if len(args) > 0 {
		sub := args[0]
		subArgs := args[1:]
		switch sub {
		case "list", "ls":
			handleHandoffList(subArgs)
			return
		case "prune":
			handleHandoffPrune(subArgs)
			return
		case "pin":
			if len(subArgs) == 0 {
				fmt.Println("Lỗi: Cần truyền tên file snapshot cần ghim. Ví dụ: claude-switch handoff pin file.md")
				return
			}
			_ = handoff.PinHandoff(config.GetHandoffsDir(), subArgs[0])
			fmt.Printf("Đã ghim: %s\n", subArgs[0])
			return
		case "unpin":
			if len(subArgs) == 0 {
				fmt.Println("Lỗi: Cần truyền tên file snapshot cần bỏ ghim.")
				return
			}
			_ = handoff.UnpinHandoff(config.GetHandoffsDir(), subArgs[0])
			fmt.Printf("Đã bỏ ghim: %s\n", subArgs[0])
			return
		case "copy":
			latestLink := filepath.Join(config.GetHandoffsDir(), "latest.md")
			if _, err := os.Stat(latestLink); err != nil {
				fmt.Println("Lỗi: Chưa có bản snapshot handoff nào.")
				return
			}
			directive := handoff.BuildDirective(latestLink)
			if err := handoff.SetClipboard(directive); err != nil {
				fmt.Printf("Lỗi khi ghi vào clipboard: %v\n", err)
			} else {
				fmt.Println("✓ Đã copy lại câu lệnh chỉ dẫn handoff vào Clipboard!")
			}
			return
		case "open":
			latestLink := filepath.Join(config.GetHandoffsDir(), "latest.md")
			target, err := os.Readlink(latestLink)
			if err != nil {
				fmt.Println("Lỗi: Chưa có bản snapshot handoff nào.")
				return
			}
			fullPath := filepath.Join(config.GetHandoffsDir(), target)
			_ = exec.Command("open", fullPath).Run()
			fmt.Printf("Đang mở: %s\n", fullPath)
			return
		}
	}

	activeName := profile.GetActiveProfileName()
	if activeName == "" {
		fmt.Println("Lỗi: Chưa có profile nào đang hoạt động.")
		os.Exit(1)
	}
	p, err := profile.ResolveProfile(activeName)
	if err != nil {
		fmt.Printf("Lỗi: %v\n", err)
		os.Exit(1)
	}
	useLatest := hasFlag(args, "--latest")
	force := hasFlag(args, "--force")
	res, err := executeHandoffFlow(p, p, useLatest, force, false)
	if err != nil {
		fmt.Printf("Lỗi: %v\n", err)
		os.Exit(1)
	}
	if res == nil || !res.Succeeded() {
		if res != nil && res.Error != "" {
			fmt.Printf("Lỗi tạo handoff: %s\n", res.Error)
		} else if res != nil && res.Status == handoff.StatusSkipped {
			fmt.Println("Thao tác tạo handoff đã bị hủy bởi người dùng.")
		} else {
			fmt.Println("Lỗi: Không tạo được snapshot handoff.")
		}
		os.Exit(1)
	}
}

func handleHandoffList(args []string) {
	asJSON := hasFlag(args, "--json")
	items, err := handoff.ListHandoffs(config.GetHandoffsDir())
	if err != nil {
		fmt.Printf("Lỗi: %v\n", err)
		return
	}
	if asJSON {
		b, _ := json.MarshalIndent(items, "", "  ")
		fmt.Println(string(b))
		return
	}

	if len(items) == 0 {
		fmt.Println("Chưa có bản snapshot handoff nào.")
		return
	}

	fmt.Println("=== DANH SÁCH HANDOFF SNAPSHOTS ===")
	for _, it := range items {
		tag := "[normal]"
		if it.IsLatest {
			tag = "[latest]"
		} else if it.IsPinned {
			tag = "[pinned]"
		}
		sizeKB := float64(it.Size) / 1024.0
		timeStr := it.ModTime.Format("2006-01-02 15:04")
		fmt.Printf("  %-8s %-45s (%5.1f KB) - %s\n", tag, it.Name, sizeKB, timeStr)
	}
}

func handleHandoffPrune(args []string) {
	dryRun := hasFlag(args, "--dry-run")
	cfg, _ := config.LoadConfig()
	deleted, err := handoff.PruneHandoffs(config.GetHandoffsDir(), &cfg.Handoff.Pruning, dryRun)
	if err != nil {
		fmt.Printf("Lỗi: %v\n", err)
		return
	}
	if len(deleted) == 0 {
		fmt.Println("Không có bản snapshot nào cần dọn dẹp.")
		return
	}

	action := "Đã xóa"
	if dryRun {
		action = "[Dry Run] Sẽ xóa"
	}
	for _, d := range deleted {
		fmt.Printf("%s: %s\n", action, filepath.Base(d))
	}
}

func handleAdd(args []string) {
	var name string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		name = args[0]
	} else {
		name = fmt.Sprintf("account_%d", time.Now().Unix())
	}

	if err := profile.ValidateProfileName(name); err != nil {
		fmt.Printf("Lỗi: %v\n", err)
		return
	}

	profilesDir := config.GetProfilesDir()
	targetPath := filepath.Join(profilesDir, name)

	// Extra check against path traversal
	if filepath.Dir(filepath.Clean(targetPath)) != filepath.Clean(profilesDir) {
		fmt.Println("Lỗi: Đường dẫn profile không hợp lệ.")
		return
	}

	if _, err := os.Stat(targetPath); err == nil {
		fmt.Printf("Lỗi: Profile '%s' đã tồn tại.\n", name)
		return
	}

	if err := os.MkdirAll(targetPath, 0755); err != nil {
		fmt.Printf("Lỗi: %v\n", err)
		return
	}

	fmt.Printf("Đã tạo profile mới: %s. Đang chuyển sang profile này và mở Claude để đăng nhập...\n", name)
	p := &profile.ProfileItem{
		Name: name,
		Path: targetPath,
	}
	_ = profile.SwitchToProfile(p, false)
}

func handleRemove(args []string) {
	if len(args) == 0 {
		fmt.Println("Lỗi: Cần truyền tên profile cần xóa.")
		return
	}
	targetName := args[0]
	p, err := profile.ResolveProfile(targetName)
	if err != nil {
		fmt.Printf("Lỗi: %v\n", err)
		return
	}

	if p.IsActive {
		fmt.Println("Lỗi: Không thể xóa profile đang được sử dụng. Hãy chuyển sang profile khác trước.")
		return
	}

	_ = os.RemoveAll(p.Path)
	fmt.Printf("Đã xóa profile: %s\n", p.Name)
}

func handleRenameByEmail() {
	if err := profile.RenameProfilesByEmail(); err != nil {
		fmt.Printf("Lỗi: %v\n", err)
	} else {
		fmt.Println("Đã hoàn tất kiểm tra và cập nhật tên profile theo email.")
	}
}

func handleConfig(args []string) {
	cfg, _ := config.LoadConfig()
	if len(args) == 0 {
		// Print current config
		maskedKey := "chưa thiết lập"
		if key := cfg.GetResolvedAPIKey(); key != "" {
			if len(key) > 8 {
				maskedKey = key[:4] + "..." + key[len(key)-4:]
			} else {
				maskedKey = "***"
			}
		}
		fmt.Println("=== CẤU HÌNH CLAUDE-SWITCH ===")
		fmt.Printf("File config: %s\n", config.GetConfigPath())
		fmt.Printf("LLM Engine:\n")
		fmt.Printf("  - Bật:             %v\n", cfg.LLM.Enabled)
		fmt.Printf("  - Base URL:        %s\n", cfg.LLM.BaseURL)
		fmt.Printf("  - Model:           %s\n", cfg.LLM.Model)
		fmt.Printf("  - API Key:         %s\n", maskedKey)
		fmt.Printf("  - Timeout:         %d giây\n", cfg.LLM.TimeoutSeconds)
		fmt.Printf("Handoff & Pruning:\n")
		fmt.Printf("  - Thư mục lưu:     %s\n", cfg.Handoff.SaveDir)
		fmt.Printf("  - Giữ tối đa:      %d snapshots\n", cfg.Handoff.Pruning.MaxSnapshots)
		fmt.Printf("  - Tuổi thọ tối đa: %d ngày\n", cfg.Handoff.Pruning.RetentionDays)
		return
	}

	sub := args[0]
	switch sub {
	case "test":
		fmt.Printf("Đang kiểm tra kết nối tới LLM endpoint (%s, model: %s)...\n", cfg.LLM.BaseURL, cfg.LLM.Model)
		dur, err := llm.PingTest(cfg)
		if err != nil {
			fmt.Printf("✗ Kết nối thất bại: %v\n", err)
		} else {
			fmt.Printf("✓ Kết nối thành công! Độ trễ: %v\n", dur)
		}
	case "set":
		if len(args) < 3 {
			fmt.Println("Lỗi: Cú pháp: claude-switch config set <key> <value>")
			return
		}
		key := args[1]
		val := args[2]
		switch key {
		case "llm.base_url":
			cfg.LLM.BaseURL = val
		case "llm.model":
			cfg.LLM.Model = val
		case "llm.api_key":
			cfg.LLM.APIKey = val
		case "llm.enabled":
			cfg.LLM.Enabled = val == "true" || val == "1"
		case "llm.timeout":
			t, _ := strconv.Atoi(val)
			cfg.LLM.TimeoutSeconds = t
		default:
			fmt.Printf("Lỗi: Thuộc tính '%s' không được hỗ trợ.\n", key)
			return
		}
		_ = config.SaveConfig(cfg)
		fmt.Printf("Đã lưu %s = %s\n", key, val)
	}
}

func handleDoctor() {
	fmt.Println("=== CLAUDE-SWITCH DOCTOR ===")
	dataDir := profile.GetDataDir()
	fmt.Printf("Claude Data dir:   %s\n", dataDir)

	target, err := os.Readlink(dataDir)
	if err == nil {
		fmt.Printf("  Loại:            Symlink -> %s\n", target)
	} else {
		fmt.Printf("  Loại:            Thư mục bình thường (chưa kích hoạt symlink)\n")
	}

	fmt.Printf("Profiles dir:      %s\n", config.GetProfilesDir())
	fmt.Printf("Handoffs dir:      %s\n", config.GetHandoffsDir())
	fmt.Printf("Active profile:    %s\n", profile.GetActiveProfileName())
	fmt.Printf("Claude đang chạy:  %v\n", profile.IsRunning())

	cfg, _ := config.LoadConfig()
	fmt.Printf("LLM Engine:        %s (%s)\n", cfg.LLM.BaseURL, cfg.LLM.Model)

	if j, err := profile.ReadJournal(); err == nil && j != nil {
		fmt.Printf("Journal:           ⚠️  Phát hiện phiên dở dang %s (%s -> %s, bước: %s)\n", j.OperationID, j.Source, j.Target, j.Step)
		fmt.Println("                   Chạy `claude-switch recover` để phục hồi.")
	} else {
		fmt.Printf("Journal:           Sạch sẽ (không có thao tác dở dang)\n")
	}
}

func handleMenu(args []string) {
	barAppPath := resolveMenuBarExecutable()
	if barAppPath == "" {
		fmt.Fprintln(os.Stderr, "Không tìm thấy Claude Switch Menu Bar app. Hãy build và cài bằng `make install`.")
		os.Exit(1)
	}

	if hasFlag(args, "--install") {
		// Install LaunchAgent
		fmt.Println("Đang thiết lập LaunchAgent khởi động cùng máy...")
		installLaunchAgent(barAppPath)
		return
	}

	fmt.Println("Đang khởi động Menu Bar Header App...")
	cmd := exec.Command(barAppPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		fmt.Printf("Lỗi khi mở Menu Bar App: %v\n", err)
	} else {
		fmt.Println("✓ Đã gửi lệnh khởi chạy Menu Bar app.")
	}
}

func resolveMenuBarExecutable() string {
	home, _ := os.UserHomeDir()
	var prefixes []string
	if exe, err := os.Executable(); err == nil {
		prefixes = append(prefixes, filepath.Dir(exe))
	}
	if home != "" {
		prefixes = append(prefixes, filepath.Join(home, ".local", "bin"))
	}
	prefixes = append(prefixes, ".")

	for _, prefix := range prefixes {
		candidates := []string{
			filepath.Join(prefix, "claude-switch-bar.app", "Contents", "MacOS", "claude-switch-bar"),
			filepath.Join(prefix, "claude-switch-bar"), // Compatibility with older standalone builds.
		}
		for _, candidate := range candidates {
			info, err := os.Stat(candidate)
			if err == nil && !info.IsDir() {
				return candidate
			}
		}
	}
	return ""
}

func installLaunchAgent(barAppPath string) {
	home, _ := os.UserHomeDir()
	launchAgentsDir := filepath.Join(home, "Library", "LaunchAgents")
	_ = os.MkdirAll(launchAgentsDir, 0755)
	plistPath := filepath.Join(launchAgentsDir, "com.dlir.claude-switch.plist")

	plistContent := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.dlir.claude-switch</string>
    <key>ProgramArguments</key>
    <array>
        <string>%s</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
</dict>
</plist>`, barAppPath)

	_ = os.WriteFile(plistPath, []byte(plistContent), 0644)
	_ = exec.Command("launchctl", "unload", plistPath).Run()
	_ = exec.Command("launchctl", "load", plistPath).Run()
	fmt.Printf("✓ Đã cài đặt LaunchAgent tại: %s\n", plistPath)
}

func hasFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}
