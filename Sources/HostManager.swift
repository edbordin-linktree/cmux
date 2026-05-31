import AppKit
import SwiftUI

enum HostManagerWorkspaceStatus: String, Equatable {
    case connected
    case disconnected
    case detached

    var label: String {
        switch self {
        case .connected:
            return String(localized: "hostManager.status.connected", defaultValue: "Connected")
        case .disconnected:
            return String(localized: "hostManager.status.disconnected", defaultValue: "Disconnected")
        case .detached:
            return String(localized: "hostManager.status.detached", defaultValue: "Detached")
        }
    }

    var tint: Color {
        switch self {
        case .connected:
            return .green
        case .disconnected:
            return .secondary
        case .detached:
            return .orange
        }
    }
}

struct HostManagerWorkspaceRow: Identifiable, Equatable {
    enum Source: Equatable {
        case live(workspaceID: UUID)
        case detached(host: String, workspaceID: UUID?, slot: String, bodyPresent: Bool)
        case error(String)
    }

    var id: String
    var title: String
    var subtitle: String?
    var status: HostManagerWorkspaceStatus
    var source: Source
    var updatedAt: Date?
}

struct HostManagerHostSection: Identifiable, Equatable {
    var id: String { host }
    var host: String
    var rows: [HostManagerWorkspaceRow]
}

private struct HostManagerListAllResponse: Decodable {
    var scannedAt: Date?
    var snapshots: [HostManagerSnapshotEntry]

    enum CodingKeys: String, CodingKey {
        case scannedAt = "scanned_at"
        case snapshots
    }
}

private struct HostManagerSnapshotEntry: Decodable {
    var slot: String
    var workspaceID: UUID?
    var title: String?
    var status: String?
    var detachedAt: Date?
    var updatedAt: Date?
    var bodyPresent: Bool?
    var error: String?

    enum CodingKeys: String, CodingKey {
        case slot
        case workspaceID = "workspace_id"
        case title
        case status
        case detachedAt = "detached_at"
        case updatedAt = "updated_at"
        case bodyPresent = "body_present"
        case error
    }
}

private struct HostManagerDetachedHostResult {
    var host: String
    var snapshots: [HostManagerSnapshotEntry]
    var error: String?
}

final class HostManagerPopoverVisibilityState: ObservableObject {
    static let shared = HostManagerPopoverVisibilityState()

    @Published private(set) var isShown = false
    @Published private(set) var shownWindowNumbers: Set<Int> = []
    private var shownPopoverIDs: Set<ObjectIdentifier> = []
    private var shownPopoverWindowNumbers: [ObjectIdentifier: Int] = [:]
    private var sourceLessShown = false

    private init() {}

    func setShown(_ newValue: Bool, source: AnyObject?, windowNumber: Int? = nil) {
        if Thread.isMainThread {
            setShownOnMain(newValue, source: source, windowNumber: windowNumber)
        } else {
            DispatchQueue.main.async { [weak self] in
                self?.setShownOnMain(newValue, source: source, windowNumber: windowNumber)
            }
        }
    }

    func isShown(in windowNumber: Int?) -> Bool {
        guard let windowNumber else { return isShown }
        return sourceLessShown || shownWindowNumbers.contains(windowNumber)
    }

    private func setShownOnMain(_ newValue: Bool, source: AnyObject?, windowNumber: Int?) {
        if let source {
            let id = ObjectIdentifier(source)
            if newValue {
                shownPopoverIDs.insert(id)
                if let windowNumber {
                    shownPopoverWindowNumbers[id] = windowNumber
                }
            } else {
                shownPopoverIDs.remove(id)
                shownPopoverWindowNumbers.removeValue(forKey: id)
            }
        } else {
            shownPopoverIDs.removeAll()
            shownPopoverWindowNumbers.removeAll()
            sourceLessShown = newValue
        }
        updateShown()
    }

    private func updateShown() {
        let nextWindowNumbers = Set(shownPopoverWindowNumbers.values)
        if shownWindowNumbers != nextWindowNumbers {
            shownWindowNumbers = nextWindowNumbers
        }
        let nextIsShown = sourceLessShown || !shownPopoverIDs.isEmpty
        guard isShown != nextIsShown else { return }
        isShown = nextIsShown
    }
}

func postHostManagerPopoverVisibilityDidChange(isShown: Bool, source: AnyObject? = nil, windowNumber: Int? = nil) {
    HostManagerPopoverVisibilityState.shared.setShown(isShown, source: source, windowNumber: windowNumber)
}

@MainActor
final class HostManagerStore: ObservableObject {
    @Published private(set) var sections: [HostManagerHostSection] = []
    @Published private(set) var isLoading = false
    @Published private(set) var lastError: String?

    private weak var preferredWindow: NSWindow?
    private var scheduledRefreshTask: Task<Void, Never>?
    private var refreshAfterCurrentLoad = false

    init(preferredWindow: NSWindow?) {
        self.preferredWindow = preferredWindow
    }

    deinit {
        scheduledRefreshTask?.cancel()
    }

    func refresh() {
        scheduledRefreshTask?.cancel()
        scheduledRefreshTask = nil
        guard !isLoading else {
            refreshAfterCurrentLoad = true
            return
        }
        isLoading = true
        lastError = nil
        let liveRows = Self.liveRows(preferredWindow: preferredWindow)

        Task {
            let detachedResults: [HostManagerDetachedHostResult]
            do {
                let registry = try DetachedWorkspaceHostRegistry.load()
                detachedResults = await HostManagerSnapshotLister.listDetachedWorkspaces(
                    hosts: registry.hosts,
                    timeout: 5
                )
            } catch {
                detachedResults = []
                lastError = error.localizedDescription
            }

            sections = Self.merge(liveRows: liveRows, detachedResults: detachedResults)
            isLoading = false
            if refreshAfterCurrentLoad {
                refreshAfterCurrentLoad = false
                scheduleRefresh()
            }
        }
    }

    func scheduleRefresh(delay: TimeInterval = 0.45) {
        scheduledRefreshTask?.cancel()
        scheduledRefreshTask = Task { @MainActor [weak self] in
            let nanoseconds = UInt64(max(0, delay) * 1_000_000_000)
            try? await Task.sleep(nanoseconds: nanoseconds)
            guard !Task.isCancelled else { return }
            self?.refresh()
        }
    }

    func cancelScheduledRefresh() {
        scheduledRefreshTask?.cancel()
        scheduledRefreshTask = nil
    }

    func detach(workspaceID: UUID) {
        Task {
            do {
                _ = try await RemoteWorkspaceSnapshotDetachController.detach(workspaceID: workspaceID)
                refresh()
            } catch {
                NSSound.beep()
                lastError = error.localizedDescription
            }
        }
    }

    func attach(host: String, slot: String, workspaceID: UUID?, title: String?) {
        guard !isLoading else { return }
        guard let workspaceID else {
            lastError = String(
                localized: "hostManager.attach.missingWorkspaceID",
                defaultValue: "Cannot attach: snapshot at this slot has no workspace UUID."
            )
            NSSound.beep()
            return
        }
        isLoading = true
        lastError = nil
        Task {
            do {
                _ = try await RemoteWorkspaceSnapshotAttachController.attach(
                    host: host,
                    slot: slot,
                    title: title,
                    workspaceID: workspaceID,
                    preferredWindow: preferredWindow
                )
                isLoading = false
                scheduleRefresh(delay: 0.1)
            } catch {
                NSSound.beep()
                lastError = error.localizedDescription
                isLoading = false
            }
        }
    }

    private static func liveRows(preferredWindow: NSWindow?) -> [String: [HostManagerWorkspaceRow]] {
        guard let manager = AppDelegate.shared?.activeTabManagerForCommands(preferredWindow: preferredWindow) else {
            return [:]
        }
        var rowsByHost: [String: [HostManagerWorkspaceRow]] = [:]
        for workspace in manager.tabs where workspace.isRemoteWorkspace {
            guard let configuration = workspace.remoteConfiguration else { continue }
            let slot = configuration.persistentDaemonSlot?.trimmingCharacters(in: .whitespacesAndNewlines)
            let subtitle = slot?.isEmpty == false ? slot : configuration.displayTarget
            let status: HostManagerWorkspaceStatus = (
                workspace.remoteConnectionState == .connected &&
                    workspace.remoteDaemonStatus.state == .ready
            ) ? .connected : .disconnected
            let row = HostManagerWorkspaceRow(
                id: "live-\(workspace.id.uuidString)",
                title: workspace.title,
                subtitle: subtitle,
                status: status,
                source: .live(workspaceID: workspace.id),
                updatedAt: nil
            )
            rowsByHost[configuration.destination, default: []].append(row)
        }
        return rowsByHost
    }

    private static func merge(
        liveRows: [String: [HostManagerWorkspaceRow]],
        detachedResults: [HostManagerDetachedHostResult]
    ) -> [HostManagerHostSection] {
        var rowsByHost = liveRows

        for result in detachedResults {
            if let error = result.error {
                rowsByHost[result.host, default: []].append(HostManagerWorkspaceRow(
                    id: "error-\(result.host)",
                    title: String(localized: "hostManager.hostError", defaultValue: "Host unavailable"),
                    subtitle: error,
                    status: .disconnected,
                    source: .error(error),
                    updatedAt: nil
                ))
                continue
            }

            let detachedRows = result.snapshots
                .filter { ($0.status ?? RemoteWorkspaceSnapshotStatus.detached.rawValue) == RemoteWorkspaceSnapshotStatus.detached.rawValue }
                .map { snapshot in
                    HostManagerWorkspaceRow(
                        id: "detached-\(result.host)-\(snapshot.slot)",
                        title: snapshot.title?.trimmingCharacters(in: .whitespacesAndNewlines).nilIfBlank ?? snapshot.workspaceID?.uuidString ?? snapshot.slot,
                        subtitle: snapshot.slot,
                        status: .detached,
                        source: .detached(
                            host: result.host,
                            workspaceID: snapshot.workspaceID,
                            slot: snapshot.slot,
                            bodyPresent: snapshot.bodyPresent ?? true
                        ),
                        updatedAt: snapshot.updatedAt ?? snapshot.detachedAt
                    )
                }
            rowsByHost[result.host, default: []].append(contentsOf: detachedRows)
        }

        return rowsByHost
            .map { host, rows in
                HostManagerHostSection(
                    host: host,
                    rows: rows.sorted { lhs, rhs in
                        if lhs.status != rhs.status {
                            return statusSortKey(lhs.status) < statusSortKey(rhs.status)
                        }
                        return lhs.title.localizedCaseInsensitiveCompare(rhs.title) == .orderedAscending
                    }
                )
            }
            .filter { !$0.rows.isEmpty }
            .sorted { $0.host.localizedCaseInsensitiveCompare($1.host) == .orderedAscending }
    }

    private static func statusSortKey(_ status: HostManagerWorkspaceStatus) -> Int {
        switch status {
        case .connected: return 0
        case .disconnected: return 1
        case .detached: return 2
        }
    }
}

private enum HostManagerSnapshotLister {
    static func listDetachedWorkspaces(
        hosts: [DetachedWorkspaceHostRegistryRecord],
        timeout: TimeInterval
    ) async -> [HostManagerDetachedHostResult] {
        await withTaskGroup(of: HostManagerDetachedHostResult.self) { group in
            for host in hosts {
                group.addTask(priority: .userInitiated) {
                    listDetachedWorkspaces(on: host, timeout: timeout)
                }
            }
            var results: [HostManagerDetachedHostResult] = []
            for await result in group {
                results.append(result)
            }
            return results.sorted { $0.host.localizedCaseInsensitiveCompare($1.host) == .orderedAscending }
        }
    }

    private static func listDetachedWorkspaces(
        on host: DetachedWorkspaceHostRegistryRecord,
        timeout: TimeInterval
    ) -> HostManagerDetachedHostResult {
        do {
            let data = try runSnapshotListAllSSH(host: host, timeout: timeout)
            let response = try RemoteWorkspaceSnapshotCodec.decoder().decode(HostManagerListAllResponse.self, from: data)
            return HostManagerDetachedHostResult(host: host.host, snapshots: response.snapshots, error: nil)
        } catch {
            return HostManagerDetachedHostResult(host: host.host, snapshots: [], error: error.localizedDescription)
        }
    }

    private static func runSnapshotListAllSSH(
        host: DetachedWorkspaceHostRegistryRecord,
        timeout: TimeInterval
    ) throws -> Data {
        let process = Process()
        process.executableURL = URL(fileURLWithPath: "/usr/bin/ssh")
        process.arguments = sshArguments(for: host, timeout: timeout) + [
            host.host,
            "\(host.daemonBinPath) workspace-snapshot-list-all --json",
        ]

        let stdout = Pipe()
        let stderr = Pipe()
        process.standardOutput = stdout
        process.standardError = stderr
        try process.run()

        if !waitForProcess(process, timeout: timeout + 1) {
            process.terminate()
            _ = waitForProcess(process, timeout: 1)
            throw NSError(
                domain: "HostManager",
                code: 1,
                userInfo: [NSLocalizedDescriptionKey: "ssh timed out after \(Int(timeout.rounded(.up)))s"]
            )
        }

        let output = stdout.fileHandleForReading.readDataToEndOfFile()
        let errorData = stderr.fileHandleForReading.readDataToEndOfFile()
        guard process.terminationStatus == 0 else {
            let stderrText = String(data: errorData, encoding: .utf8)?
                .trimmingCharacters(in: .whitespacesAndNewlines)
            throw NSError(
                domain: "HostManager",
                code: Int(process.terminationStatus),
                userInfo: [NSLocalizedDescriptionKey: stderrText?.isEmpty == false ? stderrText! : "ssh exited \(process.terminationStatus)"]
            )
        }
        return output
    }

    private static func sshArguments(for host: DetachedWorkspaceHostRegistryRecord, timeout: TimeInterval) -> [String] {
        var args: [String] = [
            "-o", "BatchMode=yes",
            "-o", "ConnectTimeout=\(max(1, Int(timeout.rounded(.up))))",
        ]
        if let port = host.port, port > 0 {
            args += ["-p", String(port)]
        }
        if let identity = host.identityFile?.trimmingCharacters(in: .whitespacesAndNewlines), !identity.isEmpty {
            args += ["-i", identity]
        }
        for option in host.sshOptions where !option.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
            args += ["-o", option]
        }
        return args
    }

    private static func waitForProcess(_ process: Process, timeout: TimeInterval) -> Bool {
        let deadline = Date().addingTimeInterval(max(0.1, timeout))
        while process.isRunning {
            if Date() >= deadline {
                return false
            }
            Thread.sleep(forTimeInterval: 0.05)
        }
        return true
    }
}

struct HostManagerPopoverView: View {
    @StateObject private var store: HostManagerStore
    let onDismiss: () -> Void

    init(preferredWindow: NSWindow?, onDismiss: @escaping () -> Void) {
        _store = StateObject(wrappedValue: HostManagerStore(preferredWindow: preferredWindow))
        self.onDismiss = onDismiss
    }

    var body: some View {
        VStack(spacing: 0) {
            header
            Divider()
            content
        }
        .frame(width: 360, height: 440)
        .background(Color(nsColor: .windowBackgroundColor))
        .onAppear {
            store.refresh()
        }
        .onReceive(NotificationCenter.default.publisher(for: .remoteWorkspaceHostManagerStateDidChange)) { _ in
            store.scheduleRefresh()
        }
        .onDisappear {
            store.cancelScheduledRefresh()
        }
    }

    private var header: some View {
        HStack(spacing: 8) {
            Image(systemName: "server.rack")
                .font(.system(size: 14, weight: .semibold))
            Text(String(localized: "hostManager.title", defaultValue: "Host Manager"))
                .font(.headline)
            Spacer()
            Button {
                store.refresh()
            } label: {
                Image(systemName: "arrow.clockwise")
                    .font(.system(size: 13, weight: .medium))
            }
            .buttonStyle(.plain)
            .disabled(store.isLoading)
            .safeHelp(String(localized: "hostManager.refresh", defaultValue: "Refresh"))

            Button(action: onDismiss) {
                Image(systemName: "xmark")
                    .font(.system(size: 12, weight: .semibold))
            }
            .buttonStyle(.plain)
            .safeHelp(String(localized: "common.close", defaultValue: "Close"))
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 12)
    }

    @ViewBuilder
    private var content: some View {
        if store.isLoading && store.sections.isEmpty {
            ProgressView()
                .frame(maxWidth: .infinity, maxHeight: .infinity)
        } else if store.sections.isEmpty {
            VStack(spacing: 8) {
                Image(systemName: "server.rack")
                    .font(.system(size: 22))
                    .foregroundColor(.secondary)
                Text(String(localized: "hostManager.empty", defaultValue: "No remote workspaces"))
                    .font(.callout)
                    .foregroundColor(.secondary)
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity)
        } else {
            ScrollView {
                LazyVStack(alignment: .leading, spacing: 12) {
                    ForEach(store.sections) { section in
                        hostSection(section)
                    }
                    if let lastError = store.lastError {
                        Text(lastError)
                            .font(.caption)
                            .foregroundColor(.secondary)
                            .lineLimit(2)
                            .padding(.horizontal, 14)
                    }
                }
                .padding(.vertical, 12)
            }
        }
    }

    private func hostSection(_ section: HostManagerHostSection) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack(spacing: 6) {
                Image(systemName: "network")
                    .font(.system(size: 11, weight: .medium))
                    .foregroundColor(.secondary)
                Text(section.host)
                    .font(.system(size: 12, weight: .semibold))
                    .lineLimit(1)
                    .truncationMode(.middle)
                Spacer()
            }
            .padding(.horizontal, 14)

            VStack(spacing: 1) {
                ForEach(section.rows) { row in
                    workspaceRow(row)
                }
            }
        }
    }

    private func workspaceRow(_ row: HostManagerWorkspaceRow) -> some View {
        HStack(spacing: 10) {
            Image(systemName: row.status == .detached ? "rectangle.dashed" : "macwindow")
                .font(.system(size: 13, weight: .medium))
                .foregroundColor(row.status.tint)
                .frame(width: 18)
            VStack(alignment: .leading, spacing: 2) {
                Text(row.title)
                    .font(.system(size: 12, weight: .medium))
                    .lineLimit(1)
                    .truncationMode(.tail)
                if let subtitle = row.subtitle, !subtitle.isEmpty {
                    Text(subtitle)
                        .font(.system(size: 10, design: .monospaced))
                        .foregroundColor(.secondary)
                        .lineLimit(1)
                        .truncationMode(.middle)
                }
            }
            Spacer(minLength: 8)
            statusPill(row.status)
            if case .live(let workspaceID) = row.source {
                Button {
                    store.detach(workspaceID: workspaceID)
                } label: {
                    Image(systemName: "rectangle.portrait.and.arrow.right")
                        .font(.system(size: 12, weight: .semibold))
                }
                .buttonStyle(.plain)
                .safeHelp(String(localized: "hostManager.detach", defaultValue: "Detach Workspace"))
            }
            if case .detached(let host, let workspaceID, let slot, let bodyPresent) = row.source {
                Button {
                    store.attach(host: host, slot: slot, workspaceID: workspaceID, title: row.title)
                } label: {
                    Image(systemName: "arrow.down.left.square")
                        .font(.system(size: 12, weight: .semibold))
                }
                .buttonStyle(.plain)
                .disabled(!bodyPresent || store.isLoading)
                .safeHelp(String(localized: "hostManager.attach", defaultValue: "Attach Workspace"))
            }
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 7)
        .background(Color(nsColor: .controlBackgroundColor).opacity(0.35))
    }

    private func statusPill(_ status: HostManagerWorkspaceStatus) -> some View {
        Text(status.label)
            .font(.system(size: 10, weight: .semibold))
            .foregroundColor(status.tint)
            .padding(.horizontal, 7)
            .padding(.vertical, 3)
            .background(
                Capsule().fill(status.tint.opacity(0.12))
            )
    }
}

private extension String {
    var nilIfBlank: String? {
        let trimmed = trimmingCharacters(in: .whitespacesAndNewlines)
        return trimmed.isEmpty ? nil : trimmed
    }
}
