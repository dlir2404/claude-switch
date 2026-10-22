package profile

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

type AccountInfo struct {
	Email       string `json:"email"`
	FullName    string `json:"full_name"`
	DisplayName string `json:"display_name"`
	AccountUUID string `json:"account_uuid"`
	OrgUUID     string `json:"org_uuid"`
}

func ExtractAccountInfo(profilePath string) *AccountInfo {
	info := &AccountInfo{}
	blobDir := filepath.Join(profilePath, "IndexedDB", "https_claude.ai_0.indexeddb.blob")
	if _, err := os.Stat(blobDir); os.IsNotExist(err) {
		return info
	}

	_ = filepath.WalkDir(blobDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}

		if bytes.Contains(data, []byte("email_address\"")) {
			parseBlobAccountInfo(data, info)
			if info.Email != "" {
				return fs.SkipAll
			}
		}
		return nil
	})

	return info
}

func parseBlobAccountInfo(data []byte, info *AccountInfo) {
	// Look for keys: email_address", full_name", display_name", uuid", organization"
	extractField := func(key string) string {
		needle := []byte(key)
		idx := bytes.Index(data, needle)
		if idx == -1 {
			return ""
		}
		pos := idx + len(needle)
		if pos >= len(data) {
			return ""
		}
		// In V8 serialization, next byte is string length or tag
		length := int(data[pos])
		// Check for valid length
		if length > 0 && pos+1+length <= len(data) {
			val := string(data[pos+1 : pos+1+length])
			if strings.Contains(val, "\x00") {
				return ""
			}
			return strings.TrimSpace(val)
		}
		return ""
	}

	if info.Email == "" {
		email := extractField("email_address\"")
		if strings.Contains(email, "@") {
			info.Email = email
		}
	}
	if info.FullName == "" {
		info.FullName = extractField("full_name\"")
	}
	if info.DisplayName == "" {
		info.DisplayName = extractField("display_name\"")
	}
	if info.AccountUUID == "" {
		info.AccountUUID = extractField("uuid\"$")
	}
}
