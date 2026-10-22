# claude-switch

> Trình quản lý nhiều tài khoản Claude Desktop trên macOS kèm cơ chế chuyển tiếp ngữ cảnh (Context & Artifacts Handoff) không phụ thuộc API của Claude.
> Tự động nhận diện **Email & Tên tài khoản**, tích hợp **LLM Handoff Engine** (OpenAI-compatible / 9router / Ollama), hỗ trợ cả **Terminal CLI** và **macOS Menu Bar Header App**.

---

## 1. Giới thiệu

`claude-switch` mang đến trải nghiệm làm việc mượt mà với Claude Desktop:
- **Tự động nhận diện Email**: Không cần tự đặt tên profile rườm rà. Tool tự động trích xuất Email và Tên hiển thị từ session đăng nhập của Claude để làm định danh.
- **LLM Handoff Engine**: Tự động trích xuất lịch sử chat và các Artifacts từ LevelDB nội bộ của app, đưa qua mô hình AI (9router, Ollama, OpenAI...) để tổng hợp thành bản briefing kỹ thuật súc tích, lưu Markdown và nạp câu lệnh vào clipboard.
- **2 giao diện linh hoạt**:
  1. **Terminal CLI** (viết bằng Golang, chạy tức thì trong ~2-5ms).
  2. **macOS Menu Bar Header Item** (icon thường trực trên thanh trạng thái desktop, sổ menu danh sách email switch nhanh bằng 1 click).

---

## 2. Giao diện Desktop Menu Bar

Icon Claude xuất hiện trực tiếp trên thanh Menu Bar của macOS:

```text
┌────────────────────────────────────────────────────────┐
│  ✻ Claude Desktop: Running                             │
│  Active: ● dev@company.com (Alex)                      │
│  AI Engine: ● 9router (gpt-4o-mini)                    │
├────────────────────────────────────────────────────────┤
│  ACCOUNTS                                              │
│    ✓ dev@company.com (Alex)                            │
│      personal@gmail.com (Alex Dev)                     │
│      team-lead@partner.org                             │
│    + Add Account...                                    │
├────────────────────────────────────────────────────────┤
│  ⚡ QUICK HANDOFF & SWITCH                              │
│    Switch to personal@gmail.com (with AI Handoff)      │
│    Switch to team-lead@partner.org (with AI Handoff)   │
│    Select Chat to Handoff...                           │
├────────────────────────────────────────────────────────┤
│  TOOLS                                                 │
│    📋 Copy Latest Handoff Directive                    │
│    📁 Open Handoffs Folder                             │
│    🩺 Doctor / System Status                           │
├────────────────────────────────────────────────────────┤
│  ⚙ PREFERENCES                                          │
│    ✓ Use AI Summary                                    │
│    ✓ Launch at Login (Khởi động cùng máy)              │
│    Restart Claude Desktop                              │
│    Quit Menu Bar                                       │
└────────────────────────────────────────────────────────┘
```

---

## 3. Cấu hình LLM Engine

Tool hỗ trợ cấu hình linh hoạt endpoint OpenAI-compatible bất kỳ (9router, Ollama, vLLM, OpenAI...) tại:
`~/.claude-switch/config.json`

```json
{
  "llm": {
    "enabled": true,
    "base_url": "http://127.0.0.1:20128/v1",
    "api_key": "env:OPENAI_API_KEY",
    "model": "gpt-4o-mini",
    "temperature": 0.2,
    "max_tokens": 4096,
    "timeout_seconds": 30
  },
  "handoff": {
    "include_artifacts": true,
    "max_recent_messages": 0,
    "max_input_tokens": 64000,
    "filter_thinking_blocks": true,
    "truncate_tool_results_chars": 800,
    "pruning": {
      "enabled": true,
      "max_snapshots": 20,
      "retention_days": 14,
      "max_total_size_mb": 50
    }
  }
}
```

### Các lệnh quản lý config:
```bash
# Xem cấu hình hiện tại
claude-switch config

# Thay đổi endpoint hoặc model
claude-switch config set llm.base_url "http://127.0.0.1:20128/v1"
claude-switch config set llm.model "gpt-4o-mini"
claude-switch config set llm.api_key "env:OPENAI_API_KEY"

# Kiểm tra kết nối tới LLM endpoint (ping test)
claude-switch config test
```

> **Cơ chế Fallback an toàn**: Nếu LLM endpoint bị offline hoặc lỗi mạng, tool tự động chuyển sang chế độ trích xuất quy tắc thuần túy (Rule-based) mà không làm gián đoạn việc switch account.

---

## 4. Giao diện Terminal CLI

### Danh sách tài khoản:
```bash
claude-switch list
# Output:
# * dev@company.com (Alex)
#   personal@gmail.com (Alex Dev)
#   team-lead@partner.org
```

### Chuyển tài khoản (hỗ trợ cả full email lẫn gõ tắt prefix):
```bash
claude-switch use personal@gmail.com
# Hoặc gõ tắt:
claude-switch use personal
```

### Chuyển tài khoản kèm Handoff bối cảnh:
```bash
claude-switch use personal -h
```
Menu tương tác trên terminal:
```text
=== CHỌN ĐOẠN CHAT ĐỂ TIẾP TỤC BỐI CẢNH ===
  [1] Refactor Payment Gateway Service (10:15 hôm nay) [Mới nhất]
  [2] Kubernetes Cluster Autoscaler Design (Hôm qua)
  [3] Tối ưu hóa Database Query Indexing (2 ngày trước)
  [0] Bỏ qua (chỉ chuyển tài khoản)

Chọn [1]: 1
```

### Quản lý Handoff & Dọn dẹp (Pruning):
```bash
claude-switch handoff           # Chỉ xuất file handoff mà không switch profile
claude-switch handoff list      # Liệt kê danh sách snapshot hiện có kèm dung lượng
claude-switch handoff prune     # Chạy dọn dẹp các snapshot cũ theo cấu hình
claude-switch handoff pin <file> # Ghim snapshot quan trọng không cho tự động xóa
```

### Kiểm tra và dọn cache Claude Desktop:
```bash
claude-switch storage                    # Xem tổng dung lượng và các thư mục lớn theo profile
claude-switch storage clean --dry-run    # Xem trước cache có thể tạo lại, chưa xóa gì
claude-switch storage clean --yes        # Xóa cache sau khi đã đóng Claude Desktop
```

Lệnh dọn chỉ nhắm vào cache Chromium/Electron có thể tạo lại (Cache, Code Cache, GPUCache và cache đồ họa). Nó giữ nguyên Cookies, config đăng nhập, IndexedDB, Local Storage, dữ liệu hội thoại, Claude Extensions và Crashpad reports. Claude Desktop phải được đóng hoàn toàn trước khi xóa.

### Các lệnh quản trị khác:
```bash
claude-switch add               # Thêm tài khoản mới (đăng nhập -> tự lưu email)
claude-switch remove <tên>      # Xóa profile
claude-switch rename-by-email   # Đổi tên các folder profile cũ sang email
claude-switch doctor            # Kiểm tra symlink, quyền extension
claude-switch menu              # Bật icon trên thanh Menu Bar macOS
```

Trong menu của icon `✻`, chọn **LLM Settings…** để bật/tắt tóm tắt LLM, cấu hình Base URL/model/timeout, nhập hoặc xóa API key, và kiểm tra kết nối. API key nhập trong cửa sổ được gửi qua stdin tới CLI, không đưa vào arguments của process; file config được lưu với quyền chỉ owner đọc/ghi.

---

## 5. Cài đặt & Build

1. Đi tới thư mục dự án:
   ```bash
   cd /Users/dlir/code/claude-switch
   ```

2. Biên dịch cả Core CLI (Go) và Menu Bar App (Swift):
   ```bash
   make install
   ```

Binary được đặt tại `~/.local/bin/claude-switch`.

---

## 6. Tài liệu kỹ thuật chi tiết

Chi tiết kiến trúc, cấu trúc module Go & Swift, cơ chế bóc tách LevelDB và lộ trình phát triển xem tại [PLAN.md](./PLAN.md).
