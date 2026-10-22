import AppKit
import Foundation
import UserNotifications

class AppDelegate: NSObject, NSApplicationDelegate, NSMenuDelegate {
    var statusItem: NSStatusItem!
    let menu = NSMenu()
    var timer: Timer?

    func applicationDidFinishLaunching(_ notification: Notification) {
        NSApp.setActivationPolicy(.accessory)
        
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

    func runCLI(args: [String]) -> (output: String, exitCode: Int32) {
        let process = Process()
        process.executableURL = URL(fileURLWithPath: getCLIPath())
        process.arguments = args
        let pipe = Pipe()
        process.standardOutput = pipe
        process.standardError = pipe

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
            let res = self?.runCLI(args: ["use", name, "-h", "--latest"])
            let exitCode = res?.exitCode ?? -1
            let output = res?.output ?? ""
            DispatchQueue.main.async {
                if exitCode == 0 {
                    self?.showNotification(title: "Claude Switch Handoff", message: "Đã chuyển sang \(name)! Context đã nằm trong clipboard, nhấn Cmd+V.")
                } else {
                    let errMsg = output.trimmingCharacters(in: .whitespacesAndNewlines)
                    self?.showNotification(title: "Claude Switch Handoff", message: errMsg.isEmpty ? "Lỗi khi tạo handoff hoặc chuyển tài khoản." : errMsg)
                }
                self?.rebuildMenu()
            }
        }
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

    @objc func onQuitApp() {
        NSApplication.shared.terminate(nil)
    }
}

let app = NSApplication.shared
let delegate = AppDelegate()
app.delegate = delegate
app.run()
