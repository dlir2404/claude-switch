package profile

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"claude-switch/pkg/config"
)

type ProfileItem struct {
	Name        string       `json:"name"`
	Path        string       `json:"path"`
	IsActive    bool         `json:"is_active"`
	AccountInfo *AccountInfo `json:"account_info"`
}

func GetDataDir() string {
	home, _ := os.UserHomeDir()
	custom := os.Getenv("CLAUDE_DATA_DIR")
	if custom != "" {
		return custom
	}
	return filepath.Join(home, "Library", "Application Support", "Claude")
}

func IsRunning() bool {
	cmd := exec.Command("pgrep", "-f", "Claude.app/Contents/")
	return cmd.Run() == nil
}

func QuitApp(force bool) error {
	if !IsRunning() {
		return nil
	}

	fmt.Println("Đang đóng Claude Desktop...")
	_ = exec.Command("osascript", "-e", `tell application "Claude" to quit`).Run()

	for i := 0; i < 20; i++ {
		if !IsRunning() {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}

	if IsRunning() {
		if force {
			_ = exec.Command("pkill", "-f", "Claude.app/Contents/").Run()
			time.Sleep(1 * time.Second)
		} else {
			return fmt.Errorf("Claude Desktop vẫn đang chạy. Hãy đóng app hoặc dùng --force")
		}
	}
	return nil
}

func GetActiveProfileName() string {
	dataDir := GetDataDir()
	target, err := os.Readlink(dataDir)
	if err != nil {
		return ""
	}
	return filepath.Base(target)
}

func ListProfiles() ([]ProfileItem, error) {
	profilesDir := config.GetProfilesDir()
	entries, err := os.ReadDir(profilesDir)
	if os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}

	activeName := GetActiveProfileName()
	var profiles []ProfileItem

	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		pName := entry.Name()
		pPath := filepath.Join(profilesDir, pName)

		accInfo := ExtractAccountInfo(pPath)
		profiles = append(profiles, ProfileItem{
			Name:        pName,
			Path:        pPath,
			IsActive:    pName == activeName,
			AccountInfo: accInfo,
		})
	}

	return profiles, nil
}

func ResolveProfile(query string) (*ProfileItem, error) {
	profiles, err := ListProfiles()
	if err != nil {
		return nil, err
	}
	if len(profiles) == 0 {
		return nil, fmt.Errorf("chưa có profile nào. Hãy chạy: claude-switch add")
	}

	queryLower := strings.ToLower(strings.TrimSpace(query))
	// 1. Exact match by Name or Email
	for _, p := range profiles {
		if strings.ToLower(p.Name) == queryLower || (p.AccountInfo != nil && strings.ToLower(p.AccountInfo.Email) == queryLower) {
			return &p, nil
		}
	}

	// 2. Prefix / Fuzzy match
	var matches []ProfileItem
	for _, p := range profiles {
		if strings.HasPrefix(strings.ToLower(p.Name), queryLower) ||
			(p.AccountInfo != nil && strings.Contains(strings.ToLower(p.AccountInfo.Email), queryLower)) {
			matches = append(matches, p)
		}
	}

	if len(matches) == 1 {
		return &matches[0], nil
	} else if len(matches) > 1 {
		var names []string
		for _, m := range matches {
			names = append(names, m.Name)
		}
		return nil, fmt.Errorf("tìm thấy nhiều profile khớp với '%s': %s. Hãy nhập cụ thể hơn", query, strings.Join(names, ", "))
	}

	return nil, fmt.Errorf("không tìm thấy profile nào khớp với '%s'", query)
}

func SwitchToProfile(target *ProfileItem, force bool) error {
	dataDir := GetDataDir()
	active := GetActiveProfileName()

	if active == target.Name && IsRunning() {
		fmt.Printf("Profile '%s' đã đang được sử dụng và Claude Desktop đang chạy.\n", target.Name)
		return nil
	}

	if err := QuitApp(force); err != nil {
		return err
	}

	// Ensure allowed_directories in extension
	EnsureFilesystemExtensionConfig(target.Path)

	if fi, err := os.Lstat(dataDir); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			_ = os.Remove(dataDir)
		} else if fi.IsDir() {
			// Real directory: back it up into profiles before replacing with symlink
			backupDir := filepath.Join(config.GetProfilesDir(), fmt.Sprintf("initial_backup_%d", time.Now().Unix()))
			if err := os.Rename(dataDir, backupDir); err != nil {
				return fmt.Errorf("không thể chuyển thư mục dữ liệu gốc sang backup: %w", err)
			}
		}
	}

	if err := os.Symlink(target.Path, dataDir); err != nil {
		return fmt.Errorf("failed to create symlink %s -> %s: %w", dataDir, target.Path, err)
	}

	displayName := target.Name
	if target.AccountInfo != nil && target.AccountInfo.Email != "" {
		displayName = target.AccountInfo.Email
	}
	fmt.Printf("Đã chuyển sang profile '%s'. Đang mở Claude Desktop...\n", displayName)
	_ = exec.Command("open", "-a", "Claude").Run()

	return nil
}

func EnsureFilesystemExtensionConfig(profilePath string) {
	settingsDir := filepath.Join(profilePath, "Claude Extensions Settings")
	_ = os.MkdirAll(settingsDir, 0755)

	configFile := filepath.Join(settingsDir, "ant.dir.ant.anthropic.filesystem.json")
	handoffsBase := config.GetBaseDir()

	type fsConfig struct {
		IsEnabled  bool `json:"isEnabled"`
		UserConfig struct {
			AllowedDirectories []string `json:"allowed_directories"`
		} `json:"userConfig"`
	}

	cfg := fsConfig{IsEnabled: true}
	if data, err := os.ReadFile(configFile); err == nil {
		_ = json.Unmarshal(data, &cfg)
	}

	hasHandoffs := false
	for _, dir := range cfg.UserConfig.AllowedDirectories {
		if dir == handoffsBase || dir == config.GetHandoffsDir() {
			hasHandoffs = true
			break
		}
	}

	if !hasHandoffs {
		cfg.UserConfig.AllowedDirectories = append(cfg.UserConfig.AllowedDirectories, handoffsBase)
		data, err := json.MarshalIndent(cfg, "", "  ")
		if err == nil {
			_ = os.WriteFile(configFile, data, 0644)
		}
	}
}

func RestartApp(force bool) error {
	active := GetActiveProfileName()
	if active == "" {
		return fmt.Errorf("chưa có profile nào đang hoạt động để restart")
	}
	p, err := ResolveProfile(active)
	if err != nil {
		return err
	}

	if err := QuitApp(force); err != nil {
		return err
	}

	EnsureFilesystemExtensionConfig(p.Path)
	fmt.Printf("Đang khởi động lại Claude Desktop với profile '%s'...\n", p.Name)
	return exec.Command("open", "-a", "Claude").Run()
}

func RenameProfilesByEmail() error {
	profiles, err := ListProfiles()
	if err != nil {
		return err
	}

	activeName := GetActiveProfileName()
	dataDir := GetDataDir()

	for _, p := range profiles {
		if p.AccountInfo != nil && p.AccountInfo.Email != "" && p.Name != p.AccountInfo.Email {
			newPath := filepath.Join(filepath.Dir(p.Path), p.AccountInfo.Email)
			if _, err := os.Stat(newPath); err == nil {
				fmt.Printf("Bỏ qua %s: Thư mục %s đã tồn tại\n", p.Name, p.AccountInfo.Email)
				continue
			}
			fmt.Printf("Đổi tên profile: %s -> %s\n", p.Name, p.AccountInfo.Email)
			if err := os.Rename(p.Path, newPath); err != nil {
				fmt.Printf("Lỗi khi đổi tên %s: %v\n", p.Name, err)
				continue
			}
			if p.Name == activeName {
				_ = os.Remove(dataDir)
				_ = os.Symlink(newPath, dataDir)
			}
		}
	}
	return nil
}

func ValidateProfileName(name string) error {
	if name == "" {
		return fmt.Errorf("tên profile không được để trống")
	}
	if strings.HasPrefix(name, ".") || strings.Contains(name, "/") || strings.Contains(name, "\\") {
		return fmt.Errorf("tên profile '%s' không hợp lệ (không được chứa '/', '\\' hoặc bắt đầu bằng dấu '.')", name)
	}
	if filepath.Base(name) != name || filepath.Clean(name) != name {
		return fmt.Errorf("tên profile '%s' chứa đường dẫn không hợp lệ", name)
	}
	for _, r := range name {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' || r == '@' || r == '+') {
			return fmt.Errorf("tên profile '%s' chứa ký tự không hợp lệ (chỉ chấp nhận chữ, số, . _ - @ +)", name)
		}
	}
	return nil
}
