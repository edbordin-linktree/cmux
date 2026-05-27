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

    private static func iso8601Formatter() -> ISO8601DateFormatter {
        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        formatter.timeZone = TimeZone(secondsFromGMT: 0)
        return formatter
    }
}
