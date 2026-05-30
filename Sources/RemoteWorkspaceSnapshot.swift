import CryptoKit
import Foundation
import AppKit

typealias BonsplitTreeSerialized = SessionWorkspaceLayoutSnapshot

enum RemoteWorkspaceSnapshotVersion: Int, Codable, Sendable {
    case v1 = 1
}

enum RemoteWorkspaceSnapshotStatus: String, Codable, Sendable, Equatable {
    case live
    case detached
}

struct RemoteWorkspaceSnapshotV1: Codable, Sendable, Equatable {
    var version: RemoteWorkspaceSnapshotVersion = .v1
    var workspaceId: UUID
    var title: String
    var detachedAt: Date
    var splitTree: BonsplitTreeSerialized
    var panes: [PaneSnapshot]
    var activePaneId: UUID?
    var displayTarget: String
    var metadataEntries: [String: String]? = nil
    var statusEntries: [RemoteWorkspaceStatusEntrySnapshot]? = nil
}

enum PaneSnapshot: Codable, Sendable, Equatable {
    case terminal(TerminalPaneSnapshot)
    case browser(BrowserPaneSnapshot)
    case markdownViewer(MarkdownViewerPaneSnapshot)

    private enum CodingKeys: String, CodingKey {
        case type
        case terminal
        case browser
        case markdownViewer
    }

    init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        let type = try container.decode(String.self, forKey: .type)
        switch type {
        case "terminal":
            self = .terminal(try container.decode(TerminalPaneSnapshot.self, forKey: .terminal))
        case "browser":
            self = .browser(try container.decode(BrowserPaneSnapshot.self, forKey: .browser))
        case "markdownViewer":
            self = .markdownViewer(try container.decode(MarkdownViewerPaneSnapshot.self, forKey: .markdownViewer))
        default:
            throw DecodingError.dataCorruptedError(
                forKey: .type,
                in: container,
                debugDescription: "Unsupported pane snapshot type: \(type)"
            )
        }
    }

    func encode(to encoder: Encoder) throws {
        var container = encoder.container(keyedBy: CodingKeys.self)
        switch self {
        case .terminal(let snapshot):
            try container.encode("terminal", forKey: .type)
            try container.encode(snapshot, forKey: .terminal)
        case .browser(let snapshot):
            try container.encode("browser", forKey: .type)
            try container.encode(snapshot, forKey: .browser)
        case .markdownViewer(let snapshot):
            try container.encode("markdownViewer", forKey: .type)
            try container.encode(snapshot, forKey: .markdownViewer)
        }
    }
}

struct TerminalPaneSnapshot: Codable, Sendable, Equatable {
    var paneId: UUID
    var remotePTYSessionId: String
    var title: String?
    var cwdHint: String?
    var agentKind: String?
    var metadataEntries: [String: String]? = nil
}

struct BrowserPaneSnapshot: Codable, Sendable, Equatable {
    var paneId: UUID
    var currentURL: String
    var title: String?
    var metadataEntries: [String: String]? = nil
}

struct MarkdownViewerPaneSnapshot: Codable, Sendable, Equatable {
    var paneId: UUID
    var path: String
    var metadataEntries: [String: String]? = nil
}

struct RemoteWorkspaceStatusEntrySnapshot: Codable, Sendable, Equatable {
    var key: String
    var value: String
    var icon: String?
    var color: String?
    var url: String?
    var priority: Int
    var format: String
    var timestamp: Date
}

enum RemoteWorkspaceSnapshotCodec {
    struct VersionProbe: Decodable, Equatable {
        let version: Int
    }

    enum SnapshotError: LocalizedError, Equatable {
        case unknownVersion(Int)
        case invalidUTF8

        var errorDescription: String? {
            switch self {
            case .unknownVersion(let version):
                return "Unsupported remote workspace snapshot version \(version). Use ssh-workspace-snapshot-clear to remove it."
            case .invalidUTF8:
                return "Remote workspace snapshot body is not valid UTF-8."
            }
        }
    }

    static func encoder() -> JSONEncoder {
        let encoder = JSONEncoder()
        encoder.outputFormatting = [.sortedKeys, .withoutEscapingSlashes]
        encoder.dateEncodingStrategy = .custom { date, encoder in
            var container = encoder.singleValueContainer()
            try container.encode(Self.iso8601Formatter().string(from: date))
        }
        return encoder
    }

    static func decoder() -> JSONDecoder {
        let decoder = JSONDecoder()
        decoder.dateDecodingStrategy = .custom { decoder in
            let container = try decoder.singleValueContainer()
            let value = try container.decode(String.self)
            if let date = Self.iso8601Formatter().date(from: value) {
                return date
            }
            if let date = ISO8601DateFormatter().date(from: value) {
                return date
            }
            throw DecodingError.dataCorruptedError(
                in: container,
                debugDescription: "Invalid ISO8601 date: \(value)"
            )
        }
        return decoder
    }

    static func encode(_ snapshot: RemoteWorkspaceSnapshotV1) throws -> Data {
        try encoder().encode(snapshot)
    }

    static func encodeString(_ snapshot: RemoteWorkspaceSnapshotV1) throws -> String {
        let data = try encode(snapshot)
        guard let string = String(data: data, encoding: .utf8) else {
            throw SnapshotError.invalidUTF8
        }
        return string
    }

    static func decode(_ data: Data) throws -> RemoteWorkspaceSnapshotV1 {
        let probe = try decoder().decode(VersionProbe.self, from: data)
        guard probe.version == RemoteWorkspaceSnapshotVersion.v1.rawValue else {
            throw SnapshotError.unknownVersion(probe.version)
        }
        return try decoder().decode(RemoteWorkspaceSnapshotV1.self, from: data)
    }

    static func decodeString(_ body: String) throws -> RemoteWorkspaceSnapshotV1 {
        try decode(Data(body.utf8))
    }

    static func sha256Hex(for data: Data) -> String {
        SHA256.hash(data: data).map { String(format: "%02x", $0) }.joined()
    }

    static func sha256Hex(for string: String) -> String {
        sha256Hex(for: Data(string.utf8))
    }

    static func iso8601String(_ date: Date) -> String {
        iso8601Formatter().string(from: date)
    }

    private static func iso8601Formatter() -> ISO8601DateFormatter {
        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        formatter.timeZone = TimeZone(secondsFromGMT: 0)
        return formatter
    }
}

enum RemoteWorkspaceSnapshotWorkspaceError: LocalizedError, Equatable {
    case ineligibleWorkspace(String)
    case unsupportedPanelType(UUID, PanelType)
    case missingRemotePTYSession(UUID)
    case missingBrowserURL(UUID)
    case missingMarkdownPath(UUID)

    var errorDescription: String? {
        switch self {
        case .ineligibleWorkspace(let reason):
            return reason
        case .unsupportedPanelType(let panelId, let panelType):
            return "Remote workspace snapshot does not support \(panelType.rawValue) pane \(panelId.uuidString)."
        case .missingRemotePTYSession(let panelId):
            return "Terminal pane \(panelId.uuidString) has no persistent remote PTY session id."
        case .missingBrowserURL(let panelId):
            return "Browser pane \(panelId.uuidString) has no current URL to snapshot."
        case .missingMarkdownPath(let panelId):
            return "Markdown pane \(panelId.uuidString) has no path to snapshot."
        }
    }
}

struct RemoteWorkspaceRestoreResult: Equatable, Sendable {
    var panelIdMap: [UUID: UUID]
    var panesRestored: Int
    var panesLost: Int
}

struct RemoteWorkspaceSnapshotUpload {
    var workspaceID: UUID
    var title: String
    var configuration: WorkspaceRemoteConfiguration
    var daemonPath: String
    var capturedAt: Date
    var body: String
    var sha256: String
    var paneCount: Int
}

struct RemoteWorkspaceSnapshotSyncResult {
    var uploaded: Bool
    var sha256: String
    var paneCount: Int
}

struct RemoteWorkspaceSnapshotDetachResult {
    var workspaceID: UUID
    var title: String
    var host: String
    var persistentDaemonSlot: String?
    var snapshotSHA256: String
    var detachedAt: Date
    var paneCount: Int
}

struct RemoteWorkspaceSnapshotAttachResult {
    var workspaceID: UUID
    var localWorkspaceID: UUID
    var title: String
    var host: String
    var persistentDaemonSlot: String
    var panesRestored: Int
    var panesLost: Int
    var snapshotSHA256: String
    var alreadyAttached: Bool = false
}

struct RemoteWorkspaceAttachmentMatch {
    var owner: TabManager
    var workspace: Workspace
}

enum RemoteWorkspaceAttachmentGuard {
    @MainActor
    static func findAttachedWorkspace(
        host: String,
        slot: String,
        excluding excludedWorkspaceID: UUID? = nil
    ) -> RemoteWorkspaceAttachmentMatch? {
        let normalizedHost = normalizedRemoteIdentityPart(host)
        let normalizedSlot = normalizedRemoteIdentityPart(slot)
        guard !normalizedHost.isEmpty, !normalizedSlot.isEmpty else { return nil }
        guard let app = AppDelegate.shared else { return nil }

        var visitedManagers = Set<ObjectIdentifier>()
        func search(_ owner: TabManager?) -> RemoteWorkspaceAttachmentMatch? {
            guard let owner else { return nil }
            let identifier = ObjectIdentifier(owner)
            guard visitedManagers.insert(identifier).inserted else { return nil }
            for workspace in owner.tabs {
                if workspace.id == excludedWorkspaceID {
                    continue
                }
                guard let configuration = workspace.remoteConfiguration,
                      configuration.transport == .ssh,
                      normalizedRemoteIdentityPart(configuration.destination) == normalizedHost,
                      normalizedRemoteIdentityPart(configuration.persistentDaemonSlot) == normalizedSlot else {
                    continue
                }
                return RemoteWorkspaceAttachmentMatch(owner: owner, workspace: workspace)
            }
            return nil
        }

        if let match = search(app.tabManager) {
            return match
        }
        for context in app.mainWindowContexts.values {
            if let match = search(context.tabManager) {
                return match
            }
        }
        for route in app.recoverableMainWindowRoutes() {
            if let match = search(route.tabManager) {
                return match
            }
        }
        return nil
    }

    @MainActor
    static func focus(_ match: RemoteWorkspaceAttachmentMatch) {
        match.owner.selectWorkspace(match.workspace)
        if let windowID = AppDelegate.shared?.windowId(for: match.owner) {
            _ = AppDelegate.shared?.focusMainWindow(windowId: windowID)
        }
    }

    private static func normalizedRemoteIdentityPart(_ value: String?) -> String {
        value?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
    }
}

enum RemoteWorkspaceSnapshotDetachController {
    @MainActor
    static func detach(workspaceID: UUID) async throws -> RemoteWorkspaceSnapshotDetachResult {
        guard let owner = AppDelegate.shared?.tabManagerFor(tabId: workspaceID),
              let workspace = owner.tabs.first(where: { $0.id == workspaceID }) else {
            throw RemoteWorkspaceSnapshotWorkspaceError.ineligibleWorkspace("Workspace not found.")
        }

        let upload = try workspace.prepareRemoteWorkspaceSnapshotUpload(capturedAt: Date(), requireCapability: true)
        _ = try workspace.storeRemoteWorkspaceSnapshot(
            body: upload.body,
            bodySHA256: upload.sha256,
            capturedAt: upload.capturedAt,
            status: .detached
        )
        NotificationCenter.default.post(name: .remoteWorkspaceHostManagerStateDidChange, object: workspace)
        try DetachedWorkspaceHostRegistry.upsert(DetachedWorkspaceHostRegistryRecord(
            host: upload.configuration.destination,
            port: upload.configuration.port,
            identityFile: upload.configuration.identityFile,
            sshOptions: upload.configuration.sshOptions,
            daemonBinPath: upload.daemonPath,
            addedAt: upload.capturedAt,
            lastSeenAt: upload.capturedAt
        ))

        guard owner.tabs.contains(where: { $0.id == workspace.id }) else {
            return RemoteWorkspaceSnapshotDetachResult(
                workspaceID: upload.workspaceID,
                title: upload.title,
                host: upload.configuration.destination,
                persistentDaemonSlot: upload.configuration.persistentDaemonSlot,
                snapshotSHA256: upload.sha256,
                detachedAt: upload.capturedAt,
                paneCount: upload.paneCount
            )
        }
        if owner.tabs.count <= 1 {
            _ = owner.addWorkspace(title: "Terminal", select: true, autoWelcomeIfNeeded: false)
        }
        workspace.performRemoteWorkspaceDetachCloseTransaction {
            owner.closeWorkspace(workspace, recordHistory: false)
        }

        return RemoteWorkspaceSnapshotDetachResult(
            workspaceID: upload.workspaceID,
            title: upload.title,
            host: upload.configuration.destination,
            persistentDaemonSlot: upload.configuration.persistentDaemonSlot,
            snapshotSHA256: upload.sha256,
            detachedAt: upload.capturedAt,
            paneCount: upload.paneCount
        )
    }
}

enum RemoteWorkspaceSnapshotAttachController {
    private struct PreparedSnapshot: Sendable {
        var snapshot: RemoteWorkspaceSnapshotV1
        var bodySHA256: String
    }

    @MainActor
    static func attach(
        host: String,
        slot: String,
        title: String?,
        preferredWorkspaceID: UUID? = nil,
        preferredWindow: NSWindow? = nil,
        daemonPathOverride: String? = nil
    ) async throws -> RemoteWorkspaceSnapshotAttachResult {
        let normalizedHost = host.trimmingCharacters(in: .whitespacesAndNewlines)
        let normalizedSlot = slot.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !normalizedHost.isEmpty, !normalizedSlot.isEmpty else {
            throw RemoteWorkspaceSnapshotWorkspaceError.ineligibleWorkspace("Detached workspace host and slot are required.")
        }
        if let attached = RemoteWorkspaceAttachmentGuard.findAttachedWorkspace(host: normalizedHost, slot: normalizedSlot) {
            RemoteWorkspaceAttachmentGuard.focus(attached)
            let configuration = attached.workspace.remoteConfiguration
            return RemoteWorkspaceSnapshotAttachResult(
                workspaceID: attached.workspace.id,
                localWorkspaceID: attached.workspace.id,
                title: attached.workspace.title,
                host: configuration?.destination ?? normalizedHost,
                persistentDaemonSlot: configuration?.persistentDaemonSlot ?? normalizedSlot,
                panesRestored: 0,
                panesLost: 0,
                snapshotSHA256: "",
                alreadyAttached: true
            )
        }
        let registry = try DetachedWorkspaceHostRegistry.load()
        guard let record = registry.hosts.first(where: { $0.host == normalizedHost }) else {
            throw RemoteWorkspaceSnapshotWorkspaceError.ineligibleWorkspace("Host is no longer in the detached workspace registry: \(normalizedHost)")
        }
        guard let owner = AppDelegate.shared?.activeTabManagerForCommands(preferredWindow: preferredWindow) else {
            throw RemoteWorkspaceSnapshotWorkspaceError.ineligibleWorkspace("No cmux window is available for attaching the workspace.")
        }

        let workspaceID = preferredWorkspaceID ?? UUID()
        let suspensionKey = RemoteWorkspaceSnapshotSyncCoordinator.shared.suspend(
            workspaceID: workspaceID,
            host: normalizedHost,
            slot: normalizedSlot
        )
        var isSnapshotSyncSuspended = true
        func resumeSnapshotSyncIfNeeded() {
            guard isSnapshotSyncSuspended else { return }
            isSnapshotSyncSuspended = false
            RemoteWorkspaceSnapshotSyncCoordinator.shared.resume(key: suspensionKey)
        }
        defer { resumeSnapshotSyncIfNeeded() }

        let initialTitle = title?.trimmingCharacters(in: .whitespacesAndNewlines).nilIfBlankForRemoteSnapshotAttach
            ?? "Detached \(String(normalizedSlot.prefix(8)))"
        let configuration = try workspaceConfiguration(for: record, slot: normalizedSlot)
        let remoteSnapshot = try configuration.sessionSnapshot()
            .requiredForRemoteSnapshotAttach("Configured remote workspace cannot be snapshotted.")
        let overridePath = daemonPathOverride?.trimmingCharacters(in: .whitespacesAndNewlines)
        let recordPath = record.daemonBinPath.trimmingCharacters(in: .whitespacesAndNewlines)
        let daemonPath: String
        if let overridePath, !overridePath.isEmpty {
            daemonPath = overridePath
        } else {
            daemonPath = recordPath.isEmpty ? "~/.cmux/bin/cmuxd-remote-current" : recordPath
        }
        let preparedSnapshot = try await Task.detached(priority: .utility) {
            try fetchSnapshot(configuration: configuration, daemonPath: daemonPath)
        }.value

        let workspace = owner.addWorkspace(
            id: workspaceID,
            title: initialTitle,
            select: true,
            autoWelcomeIfNeeded: false,
            createInitialTerminal: false
        )
        do {
            workspace.configureRemoteConnection(configuration, autoConnect: true)
            try await waitForRemoteDaemonReady(workspace: workspace, timeout: 45)
            let restoreResult = restoreSnapshot(
                preparedSnapshot.snapshot,
                into: workspace,
                remote: remoteSnapshot
            )
            let snapshot = preparedSnapshot.snapshot
            if !snapshot.panes.isEmpty, restoreResult.panesRestored == 0 {
                throw RemoteWorkspaceSnapshotWorkspaceError.ineligibleWorkspace(
                    "Remote workspace snapshot restore produced no panes; preserving detached snapshot for retry."
                )
            }
            workspace.setCustomTitle(snapshot.title)
            owner.selectWorkspace(workspace)
            resumeSnapshotSyncIfNeeded()
            RemoteWorkspaceSnapshotSyncCoordinator.shared.scheduleLiveSync(
                workspaces: [workspace],
                force: true
            )
            NotificationCenter.default.post(name: .remoteWorkspaceHostManagerStateDidChange, object: workspace)
            try? DetachedWorkspaceHostRegistry.upsert(DetachedWorkspaceHostRegistryRecord(
                host: configuration.destination,
                port: configuration.port,
                identityFile: configuration.identityFile,
                sshOptions: configuration.sshOptions,
                daemonBinPath: workspace.remoteDaemonStatus.remotePath ?? record.daemonBinPath,
                addedAt: record.addedAt,
                lastSeenAt: Date()
            ))
            return RemoteWorkspaceSnapshotAttachResult(
                workspaceID: snapshot.workspaceId,
                localWorkspaceID: workspace.id,
                title: snapshot.title,
                host: configuration.destination,
                persistentDaemonSlot: normalizedSlot,
                panesRestored: restoreResult.panesRestored,
                panesLost: restoreResult.panesLost,
                snapshotSHA256: preparedSnapshot.bodySHA256
            )
        } catch {
            if owner.tabs.contains(where: { $0.id == workspace.id }) {
                workspace.performRemoteWorkspaceDetachCloseTransaction {
                    owner.closeWorkspace(workspace, recordHistory: false)
                }
            }
            throw error
        }
    }

    private static func workspaceConfiguration(
        for record: DetachedWorkspaceHostRegistryRecord,
        slot: String
    ) throws -> WorkspaceRemoteConfiguration {
        let relayPort = Int.random(in: 49152...65535)
        let options = sshOptionsWithDetachedWorkspaceRestoreDefaults(record.sshOptions, relayPort: relayPort)
        let session = SessionRemoteWorkspaceSnapshot(
            transport: .ssh,
            destination: record.host,
            port: record.port,
            identityFile: record.identityFile,
            sshOptions: options,
            preserveAfterTerminalExit: true,
            skipDaemonBootstrap: false,
            relayPort: relayPort,
            persistentDaemonSlot: slot,
            preferAutoConnectOnRestore: false
        )
        guard let configuration = session.workspaceConfiguration(
            localSocketPath: TerminalController.shared.activeSocketPath(
                preferredPath: SocketControlSettings.socketPath()
            ),
            allowPersistentPTYRestore: true,
            preserveSSHOptions: true
        ) else {
            throw RemoteWorkspaceSnapshotWorkspaceError.ineligibleWorkspace("Could not build remote configuration for \(record.host):\(slot).")
        }
        return configuration
    }

    @MainActor
    private static func waitForRemoteDaemonReady(workspace: Workspace, timeout: TimeInterval) async throws {
        let deadline = Date().addingTimeInterval(timeout)
        var lastState = workspace.remoteDaemonStatus.state.rawValue
        var lastDetail = workspace.remoteDaemonStatus.detail ?? ""
        while Date() < deadline {
            lastState = workspace.remoteDaemonStatus.state.rawValue
            lastDetail = workspace.remoteDaemonStatus.detail ?? ""
            if workspace.remoteDaemonStatus.state == .ready {
                return
            }
            try await Task.sleep(nanoseconds: 250_000_000)
        }
        let suffix = lastDetail.isEmpty ? "" : " detail=\(lastDetail)"
        throw RemoteWorkspaceSnapshotWorkspaceError.ineligibleWorkspace(
            "remote daemon did not become ready before timeout (state=\(lastState)\(suffix))"
        )
    }

    private static func fetchSnapshot(
        configuration: WorkspaceRemoteConfiguration,
        daemonPath: String
    ) throws -> PreparedSnapshot {
        let fetch = try Workspace.fetchPreparedRemoteWorkspaceSnapshot(
            configuration: configuration,
            daemonPath: daemonPath,
            timeout: 45
        )
        guard (fetch["exists"] as? Bool) == true else {
            throw RemoteWorkspaceSnapshotWorkspaceError.ineligibleWorkspace(
                "Remote workspace snapshot is absent; use ssh-workspace-snapshot-clear if stale local state remains."
            )
        }
        guard let body = fetch["body"] as? String,
              let meta = fetch["meta"] as? [String: Any] else {
            throw RemoteWorkspaceSnapshotWorkspaceError.ineligibleWorkspace("Remote workspace snapshot fetch returned an invalid payload.")
        }
        let bodySHA256 = RemoteWorkspaceSnapshotCodec.sha256Hex(for: body)
        if let expected = meta["snapshot_sha256"] as? String,
           !expected.isEmpty,
           expected.lowercased() != bodySHA256 {
            throw RemoteWorkspaceSnapshotWorkspaceError.ineligibleWorkspace(
                "Remote workspace snapshot hash mismatch; use ssh-workspace-snapshot-clear to remove it."
            )
        }
        let snapshot = try RemoteWorkspaceSnapshotCodec.decodeString(body)
        return PreparedSnapshot(snapshot: snapshot, bodySHA256: bodySHA256)
    }

    @MainActor
    private static func restoreSnapshot(
        _ snapshot: RemoteWorkspaceSnapshotV1,
        into workspace: Workspace,
        remote: SessionRemoteWorkspaceSnapshot
    ) -> RemoteWorkspaceRestoreResult {
        workspace.restoreRemoteWorkspaceSnapshotV1(snapshot, remote: remote)
    }

    private static func sshOptionsWithDetachedWorkspaceRestoreDefaults(
        _ options: [String],
        relayPort: Int
    ) -> [String] {
        var merged = options
            .map { $0.trimmingCharacters(in: .whitespacesAndNewlines) }
            .filter { !$0.isEmpty }
            .filter { option in
                guard let key = sshOptionKey(option) else { return true }
                return ![
                    "controlmaster",
                    "controlpersist",
                    "controlpath",
                    "localcommand",
                    "permitlocalcommand",
                ].contains(key)
            }
        if !hasSSHOptionKey(merged, key: "StrictHostKeyChecking") {
            merged.append("StrictHostKeyChecking=accept-new")
        }
        return SSHPTYAttachStartupCommandBuilder.sshOptionsWithRestoreControlDefaults(
            merged,
            relayPort: relayPort
        )
    }

    private static func hasSSHOptionKey(_ options: [String], key: String) -> Bool {
        let lowered = key.lowercased()
        return options.contains { sshOptionKey($0) == lowered }
    }

    private static func sshOptionKey(_ option: String) -> String? {
        option
            .trimmingCharacters(in: .whitespacesAndNewlines)
            .split(whereSeparator: { $0 == "=" || $0.isWhitespace })
            .first
            .map(String.init)?
            .lowercased()
    }
}

private extension Optional {
    func requiredForRemoteSnapshotAttach(_ message: String) throws -> Wrapped {
        guard let value = self else {
            throw RemoteWorkspaceSnapshotWorkspaceError.ineligibleWorkspace(message)
        }
        return value
    }
}

private extension String {
    var nilIfBlankForRemoteSnapshotAttach: String? {
        let trimmed = trimmingCharacters(in: .whitespacesAndNewlines)
        return trimmed.isEmpty ? nil : trimmed
    }
}

extension Workspace {
    func validateRemoteWorkspaceSnapshotEligibility() throws -> WorkspaceRemoteConfiguration {
        guard let configuration = remoteConfiguration else {
            throw RemoteWorkspaceSnapshotWorkspaceError.ineligibleWorkspace(
                "Workspace is not remote; ssh-workspace-detach only supports remote workspaces."
            )
        }
        guard configuration.transport == .ssh else {
            throw RemoteWorkspaceSnapshotWorkspaceError.ineligibleWorkspace(
                "Workspace does not use SSH transport; ssh-workspace-detach only supports SSH workspaces."
            )
        }
        guard configuration.preserveAfterTerminalExit else {
            throw RemoteWorkspaceSnapshotWorkspaceError.ineligibleWorkspace(
                "Workspace is not configured to preserve remote terminals after exit."
            )
        }
        guard configuration.persistentDaemonSlot != nil else {
            throw RemoteWorkspaceSnapshotWorkspaceError.ineligibleWorkspace(
                "Workspace has no persistent daemon slot."
            )
        }
        return configuration
    }

    func captureRemoteWorkspaceSnapshotV1(
        detachedAt: Date = Date(),
        restorableAgentIndex: RestorableAgentSessionIndex? = RestorableAgentSessionIndex.load()
    ) throws -> RemoteWorkspaceSnapshotV1 {
        let configuration = try validateRemoteWorkspaceSnapshotEligibility()
        let session = sessionSnapshot(
            includeScrollback: false,
            restorableAgentIndex: restorableAgentIndex
        )
        let paneSnapshots = try session.panels.map(Self.remotePaneSnapshot(from:))
        let slotSuffix = configuration.persistentDaemonSlot.map { ":\($0)" } ?? ""
        return RemoteWorkspaceSnapshotV1(
            workspaceId: id,
            title: title,
            detachedAt: detachedAt,
            splitTree: session.layout,
            panes: paneSnapshots,
            activePaneId: session.focusedPanelId,
            displayTarget: configuration.displayTarget + slotSuffix,
            metadataEntries: session.metadataEntries.map { entries in
                Dictionary(uniqueKeysWithValues: entries.map { ($0.key, $0.value) })
            },
            statusEntries: statusEntries.values
                .sorted { lhs, rhs in lhs.key < rhs.key }
                .map(Self.remoteStatusEntrySnapshot(from:))
        )
    }

    func prepareRemoteWorkspaceSnapshotUpload(
        capturedAt: Date = Date(),
        restorableAgentIndex: RestorableAgentSessionIndex? = RestorableAgentSessionIndex.load(),
        requireCapability: Bool = true
    ) throws -> RemoteWorkspaceSnapshotUpload {
        let configuration = try validateRemoteWorkspaceSnapshotEligibility()
        if requireCapability, !remoteDaemonStatus.capabilities.contains("workspace.snapshot") {
            throw RemoteWorkspaceSnapshotWorkspaceError.ineligibleWorkspace(
                "remote cmuxd-remote does not advertise workspace.snapshot; rebuild the remote daemon for this local feature branch"
            )
        }
        let snapshot = try captureRemoteWorkspaceSnapshotV1(
            detachedAt: capturedAt,
            restorableAgentIndex: restorableAgentIndex
        )
        let body = try RemoteWorkspaceSnapshotCodec.encodeString(snapshot)
        return RemoteWorkspaceSnapshotUpload(
            workspaceID: id,
            title: title,
            configuration: configuration,
            daemonPath: remoteDaemonStatus.remotePath ?? "~/.cmux/bin/cmuxd-remote",
            capturedAt: capturedAt,
            body: body,
            sha256: RemoteWorkspaceSnapshotCodec.sha256Hex(for: body),
            paneCount: snapshot.panes.count
        )
    }

    @discardableResult
    func restoreRemoteWorkspaceSnapshotV1(
        _ snapshot: RemoteWorkspaceSnapshotV1,
        remote: SessionRemoteWorkspaceSnapshot
    ) -> RemoteWorkspaceRestoreResult {
        let session = Self.sessionSnapshot(from: snapshot, remote: remote)
        let panelIdMap = restoreSessionSnapshot(session)
        applyRemoteWorkspaceStatusEntries(snapshot.statusEntries)
        let restored = snapshot.panes.reduce(0) { count, pane in
            count + (panelIdMap[Self.panelId(from: pane)] == nil ? 0 : 1)
        }
        return RemoteWorkspaceRestoreResult(
            panelIdMap: panelIdMap,
            panesRestored: restored,
            panesLost: max(0, snapshot.panes.count - restored)
        )
    }

    private static func remoteStatusEntrySnapshot(from entry: SidebarStatusEntry) -> RemoteWorkspaceStatusEntrySnapshot {
        RemoteWorkspaceStatusEntrySnapshot(
            key: entry.key,
            value: entry.value,
            icon: entry.icon,
            color: entry.color,
            url: entry.url?.absoluteString,
            priority: entry.priority,
            format: entry.format.rawValue,
            timestamp: entry.timestamp
        )
    }

    private func applyRemoteWorkspaceStatusEntries(_ entries: [RemoteWorkspaceStatusEntrySnapshot]?) {
        statusEntries.removeAll(keepingCapacity: true)
        guard let entries else { return }
        for entry in entries {
            let trimmedKey = entry.key.trimmingCharacters(in: .whitespacesAndNewlines)
            guard !trimmedKey.isEmpty else { continue }
            let url = entry.url
                .flatMap { URL(string: $0.trimmingCharacters(in: .whitespacesAndNewlines)) }
            statusEntries[trimmedKey] = SidebarStatusEntry(
                key: trimmedKey,
                value: entry.value,
                icon: entry.icon,
                color: entry.color,
                url: url,
                priority: entry.priority,
                format: SidebarMetadataFormat(rawValue: entry.format) ?? .plain,
                timestamp: entry.timestamp
            )
        }
    }

    private static func remotePaneSnapshot(from panel: SessionPanelSnapshot) throws -> PaneSnapshot {
        switch panel.type {
        case .terminal:
            guard let sessionID = panel.terminal?.remotePTYSessionID?.trimmingCharacters(in: .whitespacesAndNewlines),
                  !sessionID.isEmpty else {
                throw RemoteWorkspaceSnapshotWorkspaceError.missingRemotePTYSession(panel.id)
            }
            return .terminal(
                TerminalPaneSnapshot(
                    paneId: panel.id,
                    remotePTYSessionId: sessionID,
                    title: panel.title,
                    cwdHint: panel.directory ?? panel.terminal?.workingDirectory,
                    agentKind: panel.terminal?.agent?.kind.rawValue ?? panel.terminal?.resumeBinding?.kind,
                    metadataEntries: panel.metadataEntries?.reduce(into: [String: String]()) { result, entry in
                        result[entry.key] = entry.value
                    }
                )
            )
        case .browser:
            guard let urlString = panel.browser?.urlString?.trimmingCharacters(in: .whitespacesAndNewlines),
                  !urlString.isEmpty else {
                throw RemoteWorkspaceSnapshotWorkspaceError.missingBrowserURL(panel.id)
            }
            return .browser(
                BrowserPaneSnapshot(
                    paneId: panel.id,
                    currentURL: urlString,
                    title: panel.title,
                    metadataEntries: panel.metadataEntries?.reduce(into: [String: String]()) { result, entry in
                        result[entry.key] = entry.value
                    }
                )
            )
        case .markdown:
            guard let path = panel.markdown?.filePath.trimmingCharacters(in: .whitespacesAndNewlines),
                  !path.isEmpty else {
                throw RemoteWorkspaceSnapshotWorkspaceError.missingMarkdownPath(panel.id)
            }
            return .markdownViewer(
                MarkdownViewerPaneSnapshot(
                    paneId: panel.id,
                    path: path,
                    metadataEntries: panel.metadataEntries?.reduce(into: [String: String]()) { result, entry in
                        result[entry.key] = entry.value
                    }
                )
            )
        case .filePreview, .rightSidebarTool:
            throw RemoteWorkspaceSnapshotWorkspaceError.unsupportedPanelType(panel.id, panel.type)
        }
    }

    private static func sessionSnapshot(
        from snapshot: RemoteWorkspaceSnapshotV1,
        remote: SessionRemoteWorkspaceSnapshot
    ) -> SessionWorkspaceSnapshot {
        var restoreRemote = remote
        restoreRemote.preferAutoConnectOnRestore = true
        return SessionWorkspaceSnapshot(
            processTitle: snapshot.title,
            customTitle: snapshot.title,
            customDescription: nil,
            customColor: nil,
            isPinned: false,
            terminalScrollBarHidden: nil,
            currentDirectory: firstCWDHint(in: snapshot) ?? NSHomeDirectory(),
            focusedPanelId: snapshot.activePaneId,
            layout: snapshot.splitTree,
            panels: snapshot.panes.map(Self.sessionPanelSnapshot(from:)),
            statusEntries: [],
            metadataEntries: snapshot.metadataEntries?.map {
                SessionMetadataEntrySnapshot(key: $0.key, value: $0.value)
            },
            logEntries: [],
            progress: nil,
            gitBranch: nil,
            remote: restoreRemote
        )
    }

    private static func sessionPanelSnapshot(from pane: PaneSnapshot) -> SessionPanelSnapshot {
        switch pane {
        case .terminal(let terminal):
            return SessionPanelSnapshot(
                id: terminal.paneId,
                type: .terminal,
                title: terminal.title,
                customTitle: terminal.title,
                directory: terminal.cwdHint,
                isPinned: false,
                isManuallyUnread: false,
                gitBranch: nil,
                listeningPorts: [],
                ttyName: nil,
                metadataEntries: terminal.metadataEntries?.map {
                    SessionMetadataEntrySnapshot(key: $0.key, value: $0.value)
                },
                terminal: SessionTerminalPanelSnapshot(
                    workingDirectory: terminal.cwdHint,
                    remotePTYSessionID: terminal.remotePTYSessionId
                ),
                browser: nil,
                markdown: nil,
                filePreview: nil,
                rightSidebarTool: nil
            )
        case .browser(let browser):
            return SessionPanelSnapshot(
                id: browser.paneId,
                type: .browser,
                title: browser.title,
                customTitle: browser.title,
                directory: nil,
                isPinned: false,
                isManuallyUnread: false,
                gitBranch: nil,
                listeningPorts: [],
                ttyName: nil,
                metadataEntries: browser.metadataEntries?.map {
                    SessionMetadataEntrySnapshot(key: $0.key, value: $0.value)
                },
                terminal: nil,
                browser: SessionBrowserPanelSnapshot(
                    urlString: browser.currentURL,
                    profileID: nil,
                    shouldRenderWebView: true,
                    pageZoom: 1.0,
                    developerToolsVisible: false,
                    backHistoryURLStrings: nil,
                    forwardHistoryURLStrings: nil
                ),
                markdown: nil,
                filePreview: nil,
                rightSidebarTool: nil
            )
        case .markdownViewer(let markdown):
            return SessionPanelSnapshot(
                id: markdown.paneId,
                type: .markdown,
                title: nil,
                customTitle: nil,
                directory: nil,
                isPinned: false,
                isManuallyUnread: false,
                gitBranch: nil,
                listeningPorts: [],
                ttyName: nil,
                metadataEntries: markdown.metadataEntries?.map {
                    SessionMetadataEntrySnapshot(key: $0.key, value: $0.value)
                },
                terminal: nil,
                browser: nil,
                markdown: SessionMarkdownPanelSnapshot(filePath: markdown.path),
                filePreview: nil,
                rightSidebarTool: nil
            )
        }
    }

    private static func panelId(from pane: PaneSnapshot) -> UUID {
        switch pane {
        case .terminal(let terminal):
            return terminal.paneId
        case .browser(let browser):
            return browser.paneId
        case .markdownViewer(let markdown):
            return markdown.paneId
        }
    }

    private static func firstCWDHint(in snapshot: RemoteWorkspaceSnapshotV1) -> String? {
        for pane in snapshot.panes {
            if case .terminal(let terminal) = pane,
               let cwd = terminal.cwdHint?.trimmingCharacters(in: .whitespacesAndNewlines),
               !cwd.isEmpty {
                return cwd
            }
        }
        return nil
    }
}

final class RemoteWorkspaceSnapshotSyncCoordinator: @unchecked Sendable {
    static let shared = RemoteWorkspaceSnapshotSyncCoordinator()

    private struct UploadState {
        var sha256: String
        var uploadedAt: Date
    }

    private struct PendingUpload {
        var key: String
        var upload: RemoteWorkspaceSnapshotUpload
        var status: RemoteWorkspaceSnapshotStatus
    }

    private let lock = NSLock()
    private let queue = DispatchQueue(label: "com.cmuxterm.remoteWorkspaceSnapshotSync", qos: .utility)
    private var lastUploadedByKey: [String: UploadState] = [:]
    private var inFlightKeys: Set<String> = []
    private var closedKeys: Set<String> = []
    private var suspendedKeys: Set<String> = []
    private let minimumBackgroundUploadInterval: TimeInterval

    init(minimumBackgroundUploadInterval: TimeInterval = 15.0) {
        self.minimumBackgroundUploadInterval = minimumBackgroundUploadInterval
    }

    @discardableResult
    @MainActor
    func storeNow(
        workspace: Workspace,
        status: RemoteWorkspaceSnapshotStatus,
        force: Bool = true,
        restorableAgentIndex: RestorableAgentSessionIndex? = RestorableAgentSessionIndex.load(),
        requireCapability: Bool = true,
        respectSuspension: Bool = true
    ) throws -> RemoteWorkspaceSnapshotSyncResult {
        if status == .live, !workspace.canStoreLiveRemoteWorkspaceSnapshot() {
            throw RemoteWorkspaceSnapshotWorkspaceError.ineligibleWorkspace(
                "Live remote workspace snapshot sync suppressed while persistent remote terminal state is unhealthy."
            )
        }
        let upload = try workspace.prepareRemoteWorkspaceSnapshotUpload(
            capturedAt: Date(),
            restorableAgentIndex: restorableAgentIndex,
            requireCapability: requireCapability
        )
        let key = Self.key(workspaceID: workspace.id, configuration: upload.configuration)
        if respectSuspension, isSuspended(key: key) {
            throw RemoteWorkspaceSnapshotWorkspaceError.ineligibleWorkspace(
                "Live remote workspace snapshot sync suppressed while snapshot attach is in progress."
            )
        }
        clearClosed(key: key)
        if !force, shouldSkipUpload(key: key, sha256: upload.sha256, now: upload.capturedAt) {
            return RemoteWorkspaceSnapshotSyncResult(uploaded: false, sha256: upload.sha256, paneCount: upload.paneCount)
        }
        _ = try store(upload: upload, status: status)
        NotificationCenter.default.post(name: .remoteWorkspaceHostManagerStateDidChange, object: nil)
        recordUploadSuccess(key: key, sha256: upload.sha256, uploadedAt: upload.capturedAt)
        upsertHostRegistry(configuration: upload.configuration, daemonPath: upload.daemonPath, seenAt: upload.capturedAt)
        return RemoteWorkspaceSnapshotSyncResult(uploaded: true, sha256: upload.sha256, paneCount: upload.paneCount)
    }

    @MainActor
    func scheduleLiveSync(
        workspaces: [Workspace],
        restorableAgentIndex: RestorableAgentSessionIndex? = RestorableAgentSessionIndex.load(),
        force: Bool = false
    ) {
        let now = Date()
        for workspace in workspaces {
            guard workspace.canStoreLiveRemoteWorkspaceSnapshot() else {
#if DEBUG
                cmuxDebugLog("remote.workspace.snapshot.sync.skipped_unhealthy workspace=\(workspace.id.uuidString)")
#endif
                continue
            }
            guard let upload = try? workspace.prepareRemoteWorkspaceSnapshotUpload(
                capturedAt: now,
                restorableAgentIndex: restorableAgentIndex
            ) else {
                continue
            }
            let key = Self.key(workspaceID: workspace.id, configuration: upload.configuration)
            guard !isSuspended(key: key) else {
#if DEBUG
                cmuxDebugLog("remote.workspace.snapshot.sync.skipped_suspended workspace=\(workspace.id.uuidString)")
#endif
                continue
            }
            guard shouldEnqueueUpload(key: key, sha256: upload.sha256, now: now, force: force) else {
                continue
            }
            clearClosed(key: key)
            let pending = PendingUpload(
                key: key,
                upload: upload,
                status: .live
            )
            queue.async { [weak self] in
                self?.performBackgroundUpload(pending)
            }
        }
    }

    private func performBackgroundUpload(_ pending: PendingUpload) {
        defer { clearInFlight(key: pending.key) }
        guard !isClosed(key: pending.key) else { return }
        do {
            _ = try store(upload: pending.upload, status: pending.status)
            NotificationCenter.default.post(name: .remoteWorkspaceHostManagerStateDidChange, object: nil)
            recordUploadSuccess(
                key: pending.key,
                sha256: pending.upload.sha256,
                uploadedAt: pending.upload.capturedAt
            )
            upsertHostRegistry(
                configuration: pending.upload.configuration,
                daemonPath: pending.upload.daemonPath,
                seenAt: pending.upload.capturedAt
            )
        } catch {
#if DEBUG
            cmuxDebugLog("remote.workspace.snapshot.sync.failed key=\(pending.key) error=\(error.localizedDescription)")
#endif
        }
    }

    func markClosed(workspaceID: UUID, configuration: WorkspaceRemoteConfiguration) {
        let key = Self.key(workspaceID: workspaceID, configuration: configuration)
        lock.lock()
        closedKeys.insert(key)
        lastUploadedByKey.removeValue(forKey: key)
        lock.unlock()
    }

    func suspend(workspaceID: UUID, host: String, slot: String) -> String {
        let key = Self.key(workspaceID: workspaceID, host: host, slot: slot)
        lock.lock()
        suspendedKeys.insert(key)
        lock.unlock()
        return key
    }

    func resume(key: String) {
        lock.lock()
        suspendedKeys.remove(key)
        lock.unlock()
    }

    private func store(
        upload: RemoteWorkspaceSnapshotUpload,
        status: RemoteWorkspaceSnapshotStatus
    ) throws -> [String: Any] {
        let timestamp = RemoteWorkspaceSnapshotCodec.iso8601String(upload.capturedAt)
        return try Workspace.storePreparedRemoteWorkspaceSnapshotUpload(
            upload,
            status: status,
            timestamp: timestamp
        )
    }

    private func shouldSkipUpload(key: String, sha256: String, now: Date) -> Bool {
        lock.lock()
        defer { lock.unlock() }
        guard let state = lastUploadedByKey[key] else { return false }
        return state.sha256 == sha256 && now.timeIntervalSince(state.uploadedAt) < 60
    }

    private func shouldEnqueueUpload(key: String, sha256: String, now: Date, force: Bool) -> Bool {
        lock.lock()
        defer { lock.unlock() }
        if inFlightKeys.contains(key) {
            return false
        }
        if !force, let state = lastUploadedByKey[key] {
            if state.sha256 == sha256 {
                return false
            }
            if now.timeIntervalSince(state.uploadedAt) < minimumBackgroundUploadInterval {
                return false
            }
        }
        inFlightKeys.insert(key)
        return true
    }

    private func clearInFlight(key: String) {
        lock.lock()
        inFlightKeys.remove(key)
        lock.unlock()
    }

    private func clearClosed(key: String) {
        lock.lock()
        closedKeys.remove(key)
        lock.unlock()
    }

    private func isClosed(key: String) -> Bool {
        lock.lock()
        defer { lock.unlock() }
        return closedKeys.contains(key)
    }

    private func isSuspended(key: String) -> Bool {
        lock.lock()
        defer { lock.unlock() }
        return suspendedKeys.contains(key)
    }

    private func recordUploadSuccess(key: String, sha256: String, uploadedAt: Date) {
        lock.lock()
        lastUploadedByKey[key] = UploadState(sha256: sha256, uploadedAt: uploadedAt)
        lock.unlock()
    }

    private func upsertHostRegistry(
        configuration: WorkspaceRemoteConfiguration,
        daemonPath: String,
        seenAt: Date
    ) {
        try? DetachedWorkspaceHostRegistry.upsert(DetachedWorkspaceHostRegistryRecord(
            host: configuration.destination,
            port: configuration.port,
            identityFile: configuration.identityFile,
            sshOptions: configuration.sshOptions,
            daemonBinPath: daemonPath,
            addedAt: seenAt,
            lastSeenAt: seenAt
        ))
    }

    private static func key(workspaceID: UUID, configuration: WorkspaceRemoteConfiguration) -> String {
        key(
            workspaceID: workspaceID,
            host: configuration.destination,
            slot: configuration.persistentDaemonSlot ?? ""
        )
    }

    private static func key(workspaceID: UUID, host: String, slot: String) -> String {
        [
            host,
            slot,
            workspaceID.uuidString,
        ].joined(separator: "|")
    }
}
