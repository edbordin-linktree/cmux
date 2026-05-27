import CryptoKit
import Foundation

typealias BonsplitTreeSerialized = SessionWorkspaceLayoutSnapshot

enum RemoteWorkspaceSnapshotVersion: Int, Codable, Sendable {
    case v1 = 1
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
}

struct BrowserPaneSnapshot: Codable, Sendable, Equatable {
    var paneId: UUID
    var currentURL: String
    var title: String?
}

struct MarkdownViewerPaneSnapshot: Codable, Sendable, Equatable {
    var paneId: UUID
    var path: String
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
            displayTarget: configuration.displayTarget + slotSuffix
        )
    }

    @discardableResult
    func restoreRemoteWorkspaceSnapshotV1(
        _ snapshot: RemoteWorkspaceSnapshotV1,
        remote: SessionRemoteWorkspaceSnapshot
    ) -> RemoteWorkspaceRestoreResult {
        let session = Self.sessionSnapshot(from: snapshot, remote: remote)
        let panelIdMap = restoreSessionSnapshot(session)
        let restored = snapshot.panes.reduce(0) { count, pane in
            count + (panelIdMap[Self.panelId(from: pane)] == nil ? 0 : 1)
        }
        return RemoteWorkspaceRestoreResult(
            panelIdMap: panelIdMap,
            panesRestored: restored,
            panesLost: max(0, snapshot.panes.count - restored)
        )
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
                    agentKind: panel.terminal?.agent?.kind.rawValue ?? panel.terminal?.resumeBinding?.kind
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
                    title: panel.title
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
                    path: path
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
        SessionWorkspaceSnapshot(
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
            logEntries: [],
            progress: nil,
            gitBranch: nil,
            remote: remote
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
