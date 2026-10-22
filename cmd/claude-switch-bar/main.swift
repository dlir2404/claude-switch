import AppKit
import Foundation
import UserNotifications

class AppDelegate: NSObject, NSApplicationDelegate, NSMenuDelegate {
    private struct Conversation: Decodable {
        let uuid: String
        let title: String
        let updatedAt: String

        enum CodingKeys: String, CodingKey {
            case uuid
            case title
            case updatedAt = "updated_at"
        }
    }

    var statusItem: NSStatusItem!
    let menu = NSMenu()
    var timer: Timer?
    private var llmSettingsWindowController: LLMSettingsWindowController?

    func applicationDidFinishLaunching(_ notification: Notification) {
        NSApp.setActivationPolicy(.accessory)
        installTextEditingMenu()
        
        statusItem = NSStatusBar.system.statusItem(withLength: NSStatusItem.variableLength)
        if let button = statusItem.button {
            button.title = "✻"
            button.toolTip = "Claude Switcher"
        }
        
        menu.delegate = self
        statusItem.menu = menu
        
        requestNotificationPermission()
        rebuildMenu()
        
        // Refresh every 5 seconds
        timer = Timer.scheduledTimer(withTimeInterval: 5.0, repeats: true) { [weak self] _ in
            self?.updateStatusIcon()
        }
    }

    private func installTextEditingMenu() {
        let mainMenu = NSMenu()
        let editMenuItem = NSMenuItem(title: "Edit", action: nil, keyEquivalent: "")
        let editMenu = NSMenu(title: "Edit")
        editMenu.addItem(NSMenuItem(title: "Cut", action: #selector(NSText.cut(_:)), keyEquivalent: "x"))
        editMenu.addItem(NSMenuItem(title: "Copy", action: #selector(NSText.copy(_:)), keyEquivalent: "c"))
        editMenu.addItem(NSMenuItem(title: "Paste", action: #selector(NSText.paste(_:)), keyEquivalent: "v"))
        editMenu.addItem(NSMenuItem(title: "Select All", action: #selector(NSText.selectAll(_:)), keyEquivalent: "a"))
        editMenuItem.submenu = editMenu
        mainMenu.addItem(editMenuItem)
        NSApp.mainMenu = mainMenu
    }

    func requestNotificationPermission() {
        UNUserNotificationCenter.current().requestAuthorization(options: [.alert, .sound]) { _, _ in }
    }

    func showNotification(title: String, message: String) {
        let content = UNMutableNotificationContent()
        content.title = title
        content.body = message
        content.sound = UNNotificationSound.default
        let request = UNNotificationRequest(identifier: UUID().uuidString, content: content, trigger: nil)
        UNUserNotificationCenter.current().add(request, withCompletionHandler: nil)
    }

    func menuWillOpen(_ menu: NSMenu) {
        rebuildMenu()
    }

    func updateStatusIcon() {
        let isRunning = checkClaudeRunning()
        DispatchQueue.main.async { [weak self] in
            if let button = self?.statusItem.button {
                button.title = isRunning ? "✻" : "✻·"
            }
        }
    }

    func getCLIPath() -> String {
        let fileManager = FileManager.default
        let currentDir = Bundle.main.bundlePath
        let sibling = (currentDir as NSString).deletingLastPathComponent + "/claude-switch"
        if fileManager.fileExists(atPath: sibling) {
            return sibling
        }
        let home = NSHomeDirectory()
        let candidates = [
            "\(home)/.local/bin/claude-switch",
            "/opt/homebrew/bin/claude-switch",
            "/usr/local/bin/claude-switch"
        ]
        for p in candidates {
            if fileManager.fileExists(atPath: p) {
                return p
            }
        }
        return "\(home)/.local/bin/claude-switch"
    }

    func runCLI(args: [String], input: Data? = nil) -> (output: String, exitCode: Int32) {
        let process = Process()
        process.executableURL = URL(fileURLWithPath: getCLIPath())
        process.arguments = args
        let pipe = Pipe()
        process.standardOutput = pipe
        process.standardError = pipe
        let inputPipe = input == nil ? nil : Pipe()
        if let inputPipe {
            process.standardInput = inputPipe
        }

        var outputData = Data()
        let group = DispatchGroup()
        group.enter()

        let fileHandle = pipe.fileHandleForReading
        DispatchQueue.global(qos: .userInitiated).async {
            outputData = fileHandle.readDataToEndOfFile()
            group.leave()
        }

        do {
            try process.run()
            if let input, let inputPipe {
                inputPipe.fileHandleForWriting.write(input)
                try? inputPipe.fileHandleForWriting.close()
            }
            process.waitUntilExit()
            group.wait()
            let outStr = String(data: outputData, encoding: .utf8) ?? ""
            return (outStr, process.terminationStatus)
        } catch {
            return ("", -1)
        }
    }

    func checkClaudeRunning() -> Bool {
        let process = Process()
        process.executableURL = URL(fileURLWithPath: "/usr/bin/pgrep")
        process.arguments = ["-f", "Claude.app/Contents/"]
        process.standardOutput = FileHandle.nullDevice
        process.standardError = FileHandle.nullDevice
        do {
            try process.run()
            process.waitUntilExit()
            return process.terminationStatus == 0
        } catch {
            return false
        }
    }

    func rebuildMenu() {
        menu.removeAllItems()

        let isRunning = checkClaudeRunning()
        let statusHeader = NSMenuItem(title: isRunning ? "Claude Desktop: Running" : "Claude Desktop: Stopped", action: nil, keyEquivalent: "")
        statusHeader.isEnabled = false
        menu.addItem(statusHeader)

        // Parse profiles
        let jsonOutput = runCLI(args: ["list", "--json"]).output
        struct Profile: Codable {
            let name: String
            let is_active: Bool
            let account_info: AccountInfo?
        }
        struct AccountInfo: Codable {
            let email: String?
            let full_name: String?
            let display_name: String?
        }

        var profiles: [Profile] = []
        if let data = jsonOutput.data(using: .utf8) {
            profiles = (try? JSONDecoder().decode([Profile].self, from: data)) ?? []
        }

        var activeProfile: Profile? = nil
        for p in profiles {
            if p.is_active {
                activeProfile = p
                break
            }
        }

        let activeTitle: String
        if let act = activeProfile {
            let email = act.account_info?.email ?? ""
            activeTitle = email.isEmpty ? "Active: ● \(act.name)" : "Active: ● \(email)"
        } else {
            activeTitle = "Active: (none)"
        }
        let activeItem = NSMenuItem(title: activeTitle, action: nil, keyEquivalent: "")
        activeItem.isEnabled = false
        menu.addItem(activeItem)

        menu.addItem(NSMenuItem.separator())

        // Accounts section
        let accHeader = NSMenuItem(title: "ACCOUNTS", action: nil, keyEquivalent: "")
        accHeader.isEnabled = false
        menu.addItem(accHeader)

        for p in profiles {
            var label = p.name
            if let email = p.account_info?.email, !email.isEmpty {
                label = email
                if let fn = p.account_info?.full_name, !fn.isEmpty {
                    label += " (\(fn))"
                }
            }
            if p.is_active {
                label = "✓ " + label
            } else {
                label = "   " + label
            }

            let item = NSMenuItem(title: label, action: #selector(onSelectProfile(_:)), keyEquivalent: "")
            item.target = self
            item.representedObject = p.name
            item.isEnabled = !p.is_active
            menu.addItem(item)
        }

        menu.addItem(NSMenuItem.separator())

        // Quick Handoff section
        let handoffHeader = NSMenuItem(title: "⚡ QUICK HANDOFF & SWITCH", action: nil, keyEquivalent: "")
        handoffHeader.isEnabled = false
        menu.addItem(handoffHeader)

        for p in profiles where !p.is_active {
            let targetLabel = p.account_info?.email ?? p.name
            let item = NSMenuItem(title: "Switch to \(targetLabel) (with Handoff)", action: #selector(onHandoffSwitch(_:)), keyEquivalent: "")
            item.target = self
            item.representedObject = p.name
            menu.addItem(item)
        }

        let selectChatItem = NSMenuItem(title: "Select Chat to Handoff…", action: #selector(onHandoffOnly), keyEquivalent: "")
        selectChatItem.target = self
        menu.addItem(selectChatItem)

        menu.addItem(NSMenuItem.separator())

        // Tools
        let toolsHeader = NSMenuItem(title: "TOOLS", action: nil, keyEquivalent: "")
        toolsHeader.isEnabled = false
        menu.addItem(toolsHeader)

        let copyDirectiveItem = NSMenuItem(title: "📋 Copy Latest Handoff Directive", action: #selector(onCopyDirective), keyEquivalent: "")
        copyDirectiveItem.target = self
        menu.addItem(copyDirectiveItem)

        let openFolderItem = NSMenuItem(title: "📁 Open Handoffs Folder", action: #selector(onOpenHandoffsFolder), keyEquivalent: "")
        openFolderItem.target = self
        menu.addItem(openFolderItem)

        menu.addItem(NSMenuItem.separator())

        let llmSettingsItem = NSMenuItem(title: "LLM Settings…", action: #selector(onLLMSettings), keyEquivalent: "")
        llmSettingsItem.target = self
        menu.addItem(llmSettingsItem)

        menu.addItem(NSMenuItem.separator())

        // App controls
        let restartItem = NSMenuItem(title: "Restart Claude Desktop", action: #selector(onRestartClaude), keyEquivalent: "")
        restartItem.target = self
        menu.addItem(restartItem)

        let quitItem = NSMenuItem(title: "Quit Menu Bar", action: #selector(onQuitApp), keyEquivalent: "q")
        quitItem.target = self
        menu.addItem(quitItem)
    }

    @objc func onSelectProfile(_ sender: NSMenuItem) {
        guard let name = sender.representedObject as? String else { return }
        DispatchQueue.global(qos: .userInitiated).async { [weak self] in
            let res = self?.runCLI(args: ["use", name])
            let exitCode = res?.exitCode ?? -1
            let output = res?.output ?? ""
            DispatchQueue.main.async {
                if exitCode == 0 {
                    self?.showNotification(title: "Claude Switch", message: "Đã chuyển sang tài khoản \(name)")
                } else {
                    let errMsg = output.trimmingCharacters(in: .whitespacesAndNewlines)
                    self?.showNotification(title: "Claude Switch", message: errMsg.isEmpty ? "Lỗi khi chuyển sang tài khoản \(name)." : errMsg)
                }
                self?.rebuildMenu()
            }
        }
    }

    @objc func onHandoffSwitch(_ sender: NSMenuItem) {
        guard let name = sender.representedObject as? String else { return }
        DispatchQueue.global(qos: .userInitiated).async { [weak self] in
            guard let self else { return }
            let listResult = self.runCLI(args: ["handoff", "conversations", "--json"])
            guard listResult.exitCode == 0,
                  let data = listResult.output.data(using: .utf8),
                  let conversations = try? JSONDecoder().decode([Conversation].self, from: data),
                  !conversations.isEmpty else {
                let output = listResult.output.trimmingCharacters(in: .whitespacesAndNewlines)
                DispatchQueue.main.async {
                    self.showNotification(
                        title: "Claude Switch Handoff",
                        message: output.isEmpty ? "Không tìm thấy hội thoại trong profile hiện tại." : output
                    )
                }
                return
            }

            DispatchQueue.main.async {
                guard let conversation = self.chooseConversation(from: conversations) else { return }
                DispatchQueue.global(qos: .userInitiated).async { [weak self] in
                    let res = self?.runCLI(args: ["use", name, "-h", "--conversation", conversation.uuid])
                    let exitCode = res?.exitCode ?? -1
                    let output = res?.output ?? ""
                    DispatchQueue.main.async {
                        if exitCode == 0 {
                            self?.showNotification(title: "Claude Switch Handoff", message: "Đã handoff ‘\(conversation.title)’ và chuyển tài khoản. Context đã nằm trong clipboard, nhấn Cmd+V.")
                        } else {
                            let errMsg = output.trimmingCharacters(in: .whitespacesAndNewlines)
                            self?.showNotification(title: "Claude Switch Handoff", message: errMsg.isEmpty ? "Lỗi khi tạo handoff hoặc chuyển tài khoản." : errMsg)
                        }
                        self?.rebuildMenu()
                    }
                }
            }
        }
    }

    @objc func onHandoffOnly() {
        DispatchQueue.global(qos: .userInitiated).async { [weak self] in
            guard let self else { return }
            let listResult = self.runCLI(args: ["handoff", "conversations", "--json"])
            guard listResult.exitCode == 0,
                  let data = listResult.output.data(using: .utf8),
                  let conversations = try? JSONDecoder().decode([Conversation].self, from: data),
                  !conversations.isEmpty else {
                let output = listResult.output.trimmingCharacters(in: .whitespacesAndNewlines)
                DispatchQueue.main.async {
                    self.showNotification(title: "Claude Switch Handoff", message: output.isEmpty ? "Không tìm thấy hội thoại trong profile hiện tại." : output)
                }
                return
            }

            DispatchQueue.main.async {
                guard let conversation = self.chooseConversation(from: conversations) else { return }
                DispatchQueue.global(qos: .userInitiated).async { [weak self] in
                    let res = self?.runCLI(args: ["handoff", "--conversation", conversation.uuid])
                    let exitCode = res?.exitCode ?? -1
                    let output = res?.output ?? ""
                    DispatchQueue.main.async {
                        if exitCode == 0 {
                            self?.showNotification(title: "Claude Switch Handoff", message: "Đã tạo handoff cho ‘\(conversation.title)’ và copy chỉ dẫn vào clipboard.")
                        } else {
                            let errMsg = output.trimmingCharacters(in: .whitespacesAndNewlines)
                            self?.showNotification(title: "Claude Switch Handoff", message: errMsg.isEmpty ? "Lỗi khi tạo handoff." : errMsg)
                        }
                    }
                }
            }
        }
    }

    private func chooseConversation(from conversations: [Conversation]) -> Conversation? {
        let recent = Array(conversations.prefix(10))
        let picker = NSPopUpButton(frame: NSRect(x: 0, y: 0, width: 380, height: 28), pullsDown: false)
        picker.addItems(withTitles: recent.map { conversation in
            let time = conversation.updatedAt.replacingOccurrences(of: "T", with: " ")
            return time.isEmpty ? conversation.title : "\(conversation.title)  —  \(time)"
        })

        let alert = NSAlert()
        alert.messageText = "Choose a conversation to hand off"
        alert.informativeText = "Select one of the 10 most recently updated conversations. Cancel keeps the current account unchanged."
        alert.accessoryView = picker
        alert.addButton(withTitle: "Handoff & Switch")
        alert.addButton(withTitle: "Cancel")

        guard alert.runModal() == .alertFirstButtonReturn else { return nil }
        let index = picker.indexOfSelectedItem
        guard recent.indices.contains(index) else { return nil }
        return recent[index]
    }

    @objc func onCopyDirective() {
        let home = NSHomeDirectory()
        let latestPath = "\(home)/.claude-switch/handoffs/latest.md"
        let text = "Hãy dùng filesystem tool đọc file \(latestPath) để nắm toàn bộ bối cảnh dự án, các artifacts đã tạo và công việc đang làm dở, sau đó tiếp tục thực hiện bước tiếp theo."
        let pasteboard = NSPasteboard.general
        pasteboard.clearContents()
        pasteboard.setString(text, forType: .string)
        showNotification(title: "Claude Switch", message: "Đã sao chép câu lệnh chỉ dẫn vào Clipboard!")
    }

    @objc func onOpenHandoffsFolder() {
        let home = NSHomeDirectory()
        let dir = "\(home)/.claude-switch/handoffs"
        NSWorkspace.shared.open(URL(fileURLWithPath: dir))
    }

    @objc func onRestartClaude() {
        DispatchQueue.global(qos: .userInitiated).async { [weak self] in
            let res = self?.runCLI(args: ["restart"])
            let exitCode = res?.exitCode ?? -1
            let output = res?.output ?? ""
            DispatchQueue.main.async {
                if exitCode == 0 {
                    self?.showNotification(title: "Claude Switch", message: "Đã khởi động lại Claude Desktop")
                } else {
                    let errMsg = output.trimmingCharacters(in: .whitespacesAndNewlines)
                    self?.showNotification(title: "Claude Switch", message: errMsg.isEmpty ? "Lỗi khi khởi động lại Claude." : errMsg)
                }
                self?.rebuildMenu()
            }
        }
    }

    @objc func onLLMSettings() {
        if llmSettingsWindowController == nil {
            llmSettingsWindowController = LLMSettingsWindowController { [weak self] args, input in
                guard let self else { return ("Menu Bar app is no longer available.", -1) }
                return self.runCLI(args: args, input: input)
            }
        }
        llmSettingsWindowController?.showWindow(nil)
        NSApp.activate(ignoringOtherApps: true)
    }

    @objc func onQuitApp() {
        NSApplication.shared.terminate(nil)
    }
}

final class LLMSettingsWindowController: NSWindowController {
    typealias CommandRunner = ([String], Data?) -> (output: String, exitCode: Int32)

    private struct SettingsSnapshot: Decodable {
        let enabled: Bool
        let base_url: String
        let model: String
        let timeout_seconds: Int
        let has_api_key: Bool
    }

    private let runCommand: CommandRunner
    private let enabledButton = NSButton(checkboxWithTitle: "Enable LLM summaries for handoff", target: nil, action: nil)
    private let baseURLField = NSTextField()
    private let modelField = NSTextField()
    private let apiKeyField = NSSecureTextField()
    private let timeoutField = NSTextField()
    private let clearAPIKeyButton = NSButton(checkboxWithTitle: "Clear API key from config (environment keys still apply)", target: nil, action: nil)
    private let statusLabel = NSTextField(labelWithString: "")
    private let saveButton = NSButton(title: "Save", target: nil, action: nil)
    private let testButton = NSButton(title: "Save & Test Connection", target: nil, action: nil)

    init(runCommand: @escaping CommandRunner) {
        self.runCommand = runCommand
        let window = NSWindow(
            contentRect: NSRect(x: 0, y: 0, width: 560, height: 390),
            styleMask: [.titled, .closable],
            backing: .buffered,
            defer: false
        )
        window.title = "Claude Switch — LLM Settings"
        window.isReleasedWhenClosed = false
        super.init(window: window)
        configureControls()
        buildLayout()
        loadSettings()
    }

    required init?(coder: NSCoder) {
        fatalError("init(coder:) has not been implemented")
    }

    private func configureControls() {
        baseURLField.placeholderString = "https://api.openai.com/v1"
        modelField.placeholderString = "gpt-4o-mini"
        apiKeyField.placeholderString = "Leave blank to keep the saved key"
        timeoutField.placeholderString = "30"
        clearAPIKeyButton.target = self
        clearAPIKeyButton.action = #selector(onToggleClearAPIKey)
        saveButton.target = self
        saveButton.action = #selector(onSave)
        testButton.target = self
        testButton.action = #selector(onTestConnection)
        statusLabel.lineBreakMode = .byWordWrapping
        statusLabel.maximumNumberOfLines = 3
    }

    private func buildLayout() {
        guard let contentView = window?.contentView else { return }
        let description = NSTextField(wrappingLabelWithString: "Configure the OpenAI-compatible endpoint used to summarize handoff context. The API key is saved without displaying it in this window.")

        let buttons = NSStackView(views: [testButton, saveButton])
        buttons.orientation = .horizontal
        buttons.alignment = .centerY
        buttons.spacing = 10

        let stack = NSStackView(views: [
            enabledButton,
            description,
            row(label: "Base URL", control: baseURLField),
            row(label: "Model", control: modelField),
            row(label: "API key", control: apiKeyField),
            clearAPIKeyButton,
            row(label: "Timeout (sec)", control: timeoutField),
            statusLabel,
            buttons
        ])
        stack.orientation = .vertical
        stack.alignment = .leading
        stack.spacing = 12
        stack.translatesAutoresizingMaskIntoConstraints = false
        contentView.addSubview(stack)

        NSLayoutConstraint.activate([
            stack.leadingAnchor.constraint(equalTo: contentView.leadingAnchor, constant: 20),
            stack.trailingAnchor.constraint(equalTo: contentView.trailingAnchor, constant: -20),
            stack.topAnchor.constraint(equalTo: contentView.topAnchor, constant: 20),
            stack.bottomAnchor.constraint(lessThanOrEqualTo: contentView.bottomAnchor, constant: -20),
            baseURLField.widthAnchor.constraint(greaterThanOrEqualToConstant: 360),
            modelField.widthAnchor.constraint(greaterThanOrEqualToConstant: 360),
            apiKeyField.widthAnchor.constraint(greaterThanOrEqualToConstant: 360),
            timeoutField.widthAnchor.constraint(greaterThanOrEqualToConstant: 360),
            buttons.trailingAnchor.constraint(equalTo: stack.trailingAnchor)
        ])
    }

    private func row(label title: String, control: NSView) -> NSView {
        let label = NSTextField(labelWithString: title)
        label.widthAnchor.constraint(equalToConstant: 95).isActive = true
        let row = NSStackView(views: [label, control])
        row.orientation = .horizontal
        row.alignment = .centerY
        row.spacing = 10
        return row
    }

    private func loadSettings() {
        let result = runCommand(["config", "json"], nil)
        guard result.exitCode == 0,
              let data = result.output.data(using: .utf8),
              let settings = try? JSONDecoder().decode(SettingsSnapshot.self, from: data) else {
            statusLabel.stringValue = "Could not load LLM settings. Check that claude-switch is installed."
            return
        }
        enabledButton.state = settings.enabled ? .on : .off
        baseURLField.stringValue = settings.base_url
        modelField.stringValue = settings.model
        timeoutField.stringValue = String(settings.timeout_seconds)
        apiKeyField.stringValue = ""
        apiKeyField.placeholderString = settings.has_api_key ? "Saved key is set; leave blank to keep it" : "Enter an API key (optional for local endpoints)"
    }

    @objc private func onToggleClearAPIKey() {
        apiKeyField.isEnabled = clearAPIKeyButton.state != .on
    }

    @objc private func onSave() {
        saveSettings(testAfterSave: false)
    }

    @objc private func onTestConnection() {
        saveSettings(testAfterSave: true)
    }

    private func saveSettings(testAfterSave: Bool) {
        guard let timeout = Int(timeoutField.stringValue), (1...600).contains(timeout) else {
            statusLabel.stringValue = "Timeout must be a number between 1 and 600 seconds."
            return
        }
        guard let baseURL = URL(string: baseURLField.stringValue),
              ["http", "https"].contains(baseURL.scheme?.lowercased() ?? "") else {
            statusLabel.stringValue = "Base URL must start with http:// or https://."
            return
        }
        guard !modelField.stringValue.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else {
            statusLabel.stringValue = "Model cannot be empty."
            return
        }

        var payload: [String: Any] = [
            "enabled": enabledButton.state == .on,
            "base_url": baseURLField.stringValue,
            "model": modelField.stringValue,
            "timeout_seconds": timeout
        ]
        if clearAPIKeyButton.state == .on {
            payload["clear_api_key"] = true
        } else if !apiKeyField.stringValue.isEmpty {
            payload["api_key"] = apiKeyField.stringValue
        }
        guard let input = try? JSONSerialization.data(withJSONObject: payload) else {
            statusLabel.stringValue = "Could not encode settings."
            return
        }

        saveButton.isEnabled = false
        testButton.isEnabled = false
        statusLabel.stringValue = testAfterSave ? "Saving settings and testing the endpoint…" : "Saving settings…"
        DispatchQueue.global(qos: .userInitiated).async { [weak self] in
            guard let self else { return }
            let saveResult = self.runCommand(["config", "update"], input)
            var message = saveResult.output.trimmingCharacters(in: .whitespacesAndNewlines)
            var success = saveResult.exitCode == 0
            if success && testAfterSave {
                let testResult = self.runCommand(["config", "test"], nil)
                message = testResult.output.trimmingCharacters(in: .whitespacesAndNewlines)
                success = testResult.exitCode == 0
            }
            DispatchQueue.main.async {
                self.saveButton.isEnabled = true
                self.testButton.isEnabled = true
                self.statusLabel.stringValue = message.isEmpty ? (success ? "Settings saved." : "Operation failed.") : message
                if saveResult.exitCode == 0 {
                    self.apiKeyField.stringValue = ""
                    self.clearAPIKeyButton.state = .off
                    self.apiKeyField.isEnabled = true
                    self.apiKeyField.placeholderString = "Leave blank to keep the configured key"
                }
            }
        }
    }
}

let app = NSApplication.shared
let delegate = AppDelegate()
app.delegate = delegate
app.run()
