import Foundation
import Darwin

struct DetachedWorkspaceHostRegistryRecord: Codable, Equatable {
    var host: String
    var port: Int?
    var identityFile: String?
    var sshOptions: [String]
    var daemonBinPath: String
    var addedAt: Date
    var lastSeenAt: Date

    enum CodingKeys: String, CodingKey {
        case host
        case port
        case identityFile = "identity_file"
        case sshOptions = "ssh_options"
        case daemonBinPath = "daemon_bin_path"
        case addedAt = "added_at"
        case lastSeenAt = "last_seen_at"
    }
}

struct DetachedWorkspaceHostRegistryFile: Codable, Equatable {
    var version: Int = 1
    var hosts: [DetachedWorkspaceHostRegistryRecord] = []
}

enum DetachedWorkspaceHostRegistry {
    private static let version = 1
    private static let lock = NSLock()

    static var url: URL {
        FileManager.default.homeDirectoryForCurrentUser
            .appendingPathComponent("Library", isDirectory: true)
            .appendingPathComponent("Application Support", isDirectory: true)
            .appendingPathComponent("cmux", isDirectory: true)
            .appendingPathComponent("detached-hosts.json", isDirectory: false)
    }

    static func load() throws -> DetachedWorkspaceHostRegistryFile {
        lock.lock()
        defer { lock.unlock() }
        return try loadUnlocked()
    }

    static func upsert(_ record: DetachedWorkspaceHostRegistryRecord) throws {
        lock.lock()
        defer { lock.unlock() }
        var registry = try loadUnlocked()
        if let index = registry.hosts.firstIndex(where: { $0.host == record.host }) {
            let addedAt = registry.hosts[index].addedAt
            let daemonBinPath = Self.isFallbackDaemonBinPath(record.daemonBinPath)
                ? registry.hosts[index].daemonBinPath
                : record.daemonBinPath
            registry.hosts[index] = DetachedWorkspaceHostRegistryRecord(
                host: record.host,
                port: record.port,
                identityFile: record.identityFile,
                sshOptions: record.sshOptions,
                daemonBinPath: daemonBinPath,
                addedAt: addedAt,
                lastSeenAt: record.lastSeenAt
            )
        } else {
            registry.hosts.append(record)
        }
        registry.hosts.sort { $0.host < $1.host }
        try saveUnlocked(registry)
    }

    private static func isFallbackDaemonBinPath(_ value: String) -> Bool {
        value.trimmingCharacters(in: .whitespacesAndNewlines) == "~/.cmux/bin/cmuxd-remote"
    }

    private static func loadUnlocked() throws -> DetachedWorkspaceHostRegistryFile {
        let fileURL = url
        guard FileManager.default.fileExists(atPath: fileURL.path) else {
            return DetachedWorkspaceHostRegistryFile()
        }
        let data = try Data(contentsOf: fileURL)
        let decoder = JSONDecoder()
        decoder.dateDecodingStrategy = .iso8601
        let registry = try decoder.decode(DetachedWorkspaceHostRegistryFile.self, from: data)
        guard registry.version == version else {
            try backupUnknownVersionFile(at: fileURL)
            return DetachedWorkspaceHostRegistryFile()
        }
        return registry
    }

    private static func saveUnlocked(_ registry: DetachedWorkspaceHostRegistryFile) throws {
        let fileURL = url
        let directory = fileURL.deletingLastPathComponent()
        let fileManager = FileManager.default
        try fileManager.createDirectory(at: directory, withIntermediateDirectories: true)
        try? fileManager.setAttributes([.posixPermissions: 0o700], ofItemAtPath: directory.path)

        let encoder = JSONEncoder()
        encoder.outputFormatting = [.prettyPrinted, .sortedKeys, .withoutEscapingSlashes]
        encoder.dateEncodingStrategy = .iso8601
        let data = try encoder.encode(registry)
        let tmpURL = directory.appendingPathComponent(fileURL.lastPathComponent + ".tmp.\(ProcessInfo.processInfo.processIdentifier)")
        try? fileManager.removeItem(at: tmpURL)
        try data.write(to: tmpURL, options: [])
        try? fileManager.setAttributes([.posixPermissions: 0o600], ofItemAtPath: tmpURL.path)
        try fsyncFile(at: tmpURL)
        guard rename(tmpURL.path, fileURL.path) == 0 else {
            let error = POSIXError(POSIXErrorCode(rawValue: errno) ?? .EIO)
            try? fileManager.removeItem(at: tmpURL)
            throw error
        }
        try? fsyncDirectory(at: directory)
    }

    private static func backupUnknownVersionFile(at fileURL: URL) throws {
        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        let stamp = formatter.string(from: Date())
            .replacingOccurrences(of: ":", with: "")
            .replacingOccurrences(of: ".", with: "")
        let backupURL = fileURL.deletingLastPathComponent()
            .appendingPathComponent("detached-hosts.json.bak.\(stamp)")
        try FileManager.default.moveItem(at: fileURL, to: backupURL)
        cmuxDebugLog("detached-hosts unsupported version; moved to \(backupURL.path)")
    }

    private static func fsyncFile(at url: URL) throws {
        let fd = open(url.path, O_RDONLY)
        guard fd >= 0 else { throw POSIXError(POSIXErrorCode(rawValue: errno) ?? .EIO) }
        defer { close(fd) }
        guard fsync(fd) == 0 else { throw POSIXError(POSIXErrorCode(rawValue: errno) ?? .EIO) }
    }

    private static func fsyncDirectory(at url: URL) throws {
        let fd = open(url.path, O_RDONLY)
        guard fd >= 0 else { throw POSIXError(POSIXErrorCode(rawValue: errno) ?? .EIO) }
        defer { close(fd) }
        guard fsync(fd) == 0 else { throw POSIXError(POSIXErrorCode(rawValue: errno) ?? .EIO) }
    }
}
