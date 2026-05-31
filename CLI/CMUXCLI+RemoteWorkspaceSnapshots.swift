import Foundation
import Darwin

/// Declarative spec for a remote-workspace-snapshot subcommand that follows
/// the "value flags + boolean flags, no positional args" shape. Centralizing
/// the flag whitelist + usage string lets every handler delegate the
/// repetitive "unknown flag" / "unexpected positional" / "Known flags:"
/// boilerplate to parseSnapshotSubcommand.
struct CMUXSnapshotSubcommand {
    /// A flag that takes a value (e.g. `--workspace <id>`). The descriptor is
    /// the literal angle-bracket text appended after the flag name in error
    /// messages (e.g. "<workspace>", "<h>", "<id|ref|index>").
    struct ValueFlag {
        let name: String
        let descriptor: String
    }

    let name: String
    let usage: String
    let valueFlags: [ValueFlag]
    let boolFlags: [String]

    init(name: String, usage: String, valueFlags: [ValueFlag] = [], boolFlags: [String] = []) {
        self.name = name
        self.usage = usage
        self.valueFlags = valueFlags
        self.boolFlags = boolFlags
    }

    fileprivate var knownFlagsDescription: String {
        let parts = valueFlags.map { "\($0.name) \($0.descriptor)" } + boolFlags
        return parts.joined(separator: ", ")
    }
}

struct CMUXSnapshotSubcommandArgs {
    private let values: [String: String]
    private let bools: Set<String>

    fileprivate init(values: [String: String], bools: Set<String>) {
        self.values = values
        self.bools = bools
    }

    func value(_ flag: String) -> String? { values[flag] }
    func bool(_ flag: String) -> Bool { bools.contains(flag) }
}

private struct DetachedWorkspaceHostRegistryFile: Codable {
    var version: Int = 1
    var hosts: [DetachedWorkspaceHostRecord] = []
}

private struct DetachedWorkspaceHostRecord: Codable {
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

private struct DetachedWorkspaceListAllResponse: Decodable {
    var version: Int
    var hostID: String?
    var scannedAt: Date?
    var snapshots: [DetachedWorkspaceSnapshotEntry]

    enum CodingKeys: String, CodingKey {
        case version
        case hostID = "host_id"
        case scannedAt = "scanned_at"
        case snapshots
    }
}

private struct DetachedWorkspaceSnapshotEntry: Codable {
    var slot: String
    var workspaceID: String?
    var title: String?
    var status: String?
    var detachedAt: Date?
    var updatedAt: Date?
    var schemaVersion: Int?
    var snapshotSHA256: String?
    var bodyByteLength: Int?
    var bodyPresent: Bool?
    var error: String?

    enum CodingKeys: String, CodingKey {
        case slot
        case workspaceID = "workspace_id"
        case title
        case status
        case detachedAt = "detached_at"
        case updatedAt = "updated_at"
        case schemaVersion = "schema_version"
        case snapshotSHA256 = "snapshot_sha256"
        case bodyByteLength = "body_byte_length"
        case bodyPresent = "body_present"
        case error
    }
}

private struct DetachedWorkspaceHostListResult {
    var host: String
    var scannedAt: Date?
    var snapshots: [DetachedWorkspaceSnapshotEntry]
    var error: String?
}

private struct DetachedWorkspaceResolvedTarget {
    var host: DetachedWorkspaceHostRecord
    var slot: String
    var snapshot: DetachedWorkspaceSnapshotEntry?
}

private enum DetachedWorkspaceRegistry {
    private static let lock = NSLock()
    private static let version = 1

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

    static func remove(host: String) throws -> Bool {
        lock.lock()
        defer { lock.unlock() }
        var registry = try loadUnlocked()
        let before = registry.hosts.count
        registry.hosts.removeAll { $0.host == host }
        guard registry.hosts.count != before else {
            return false
        }
        try saveUnlocked(registry)
        return true
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
        let tmpURL = fileURL.deletingLastPathComponent()
            .appendingPathComponent(fileURL.lastPathComponent + ".tmp.\(ProcessInfo.processInfo.processIdentifier)")
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
        FileHandle.standardError.write(Data("detached-hosts.json has an unsupported schema version; moved it to \(backupURL.path)\n".utf8))
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

private enum DetachedWorkspaceDates {
    static func decoder() -> JSONDecoder {
        let decoder = JSONDecoder()
        decoder.dateDecodingStrategy = .custom { decoder in
            let container = try decoder.singleValueContainer()
            let value = try container.decode(String.self)
            if let date = fractionalFormatter.date(from: value) ?? internetFormatter.date(from: value) {
                return date
            }
            throw DecodingError.dataCorruptedError(in: container, debugDescription: "Invalid RFC3339 date: \(value)")
        }
        return decoder
    }

    static func string(_ date: Date?) -> String {
        guard let date else { return "-" }
        return internetFormatter.string(from: date)
    }

    private static let internetFormatter: ISO8601DateFormatter = {
        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withInternetDateTime]
        return formatter
    }()

    private static let fractionalFormatter: ISO8601DateFormatter = {
        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        return formatter
    }()
}

extension CMUXCLI {
    /// Parse a snapshot subcommand following the standard shape declared by
    /// `spec`. Returns the parsed value/boolean flags or throws the standard
    /// "unknown flag" / "Usage: ..." CLIErrors. Required-value validation is
    /// left to each handler so it can format command-specific error text and
    /// pick a non-default exit code.
    func parseSnapshotSubcommand(
        _ args: [String],
        spec: CMUXSnapshotSubcommand
    ) throws -> CMUXSnapshotSubcommandArgs {
        var remaining = args
        var values: [String: String] = [:]
        for flag in spec.valueFlags {
            let (parsed, rest) = parseOption(remaining, name: flag.name)
            if let parsed { values[flag.name] = parsed }
            remaining = rest
        }
        var bools = Set<String>()
        var leftover: [String] = []
        for token in remaining {
            if spec.boolFlags.contains(token) {
                bools.insert(token)
            } else {
                leftover.append(token)
            }
        }
        if let unknown = leftover.first(where: { localIsFlagToken($0) }) {
            throw CLIError(message: "\(spec.name): unknown flag '\(unknown)'. Known flags: \(spec.knownFlagsDescription)")
        }
        guard leftover.isEmpty else {
            throw CLIError(message: "Usage: \(spec.usage)")
        }
        return CMUXSnapshotSubcommandArgs(values: values, bools: bools)
    }

    func runSSHWorkspaceDetach(
        commandArgs: [String],
        client: SocketClient,
        jsonOutput: Bool,
        idFormat: CLIIDFormat
    ) throws {
        let spec = CMUXSnapshotSubcommand(
            name: "ssh-workspace-detach",
            usage: "cmux ssh-workspace-detach --workspace <id|ref|index> [--json]",
            valueFlags: [.init(name: "--workspace", descriptor: "<workspace>")]
        )
        let parsed = try parseSnapshotSubcommand(commandArgs, spec: spec)
        guard let workspaceRaw = nonEmpty(parsed.value("--workspace")) else {
            throw CLIError(message: "ssh-workspace-detach requires --workspace <id|ref|index>", exitCode: 1)
        }
        let workspaceID = try normalizeWorkspaceHandle(workspaceRaw, client: client)
        guard let workspaceID else {
            throw CLIError(message: "Workspace not found: \(workspaceRaw)", exitCode: 1)
        }
        let payload = try client.sendV2(method: "workspace.remote.snapshot_detach", params: ["workspace_id": workspaceID])
        if jsonOutput {
            print(jsonString(payload))
            return
        }
        let title = (payload["title"] as? String) ?? "workspace"
        let host = (payload["host"] as? String) ?? "remote"
        let slot = (payload["persistent_daemon_slot"] as? String) ?? "slot"
        let detachedWorkspaceID = (payload["workspace_id"] as? String) ?? workspaceID
        print("Detached workspace \(detachedWorkspaceID) (\(title)) to \(host):\(slot)")
    }

    func runSSHWorkspaceAttach(
        commandArgs: [String],
        client: SocketClient,
        jsonOutput: Bool,
        idFormat: CLIIDFormat,
        windowOverride: String?
    ) throws {
        let spec = CMUXSnapshotSubcommand(
            name: "ssh-workspace-attach",
            usage: "cmux ssh-workspace-attach --workspace-id <uuid> [--host <h>] [--slot <s>] [--window <id|ref|index>] [--json]",
            valueFlags: [
                .init(name: "--workspace-id", descriptor: "<uuid>"),
                .init(name: "--host", descriptor: "<h>"),
                .init(name: "--slot", descriptor: "<s>"),
                .init(name: "--window", descriptor: "<id|ref|index>"),
            ]
        )
        let parsed = try parseSnapshotSubcommand(commandArgs, spec: spec)
        let workspaceIDOpt = parsed.value("--workspace-id")
        let hostOpt = parsed.value("--host")
        let slotOpt = parsed.value("--slot")
        let windowOpt = parsed.value("--window")
        guard let workspaceID = canonicalDetachedWorkspaceID(workspaceIDOpt) else {
            throw CLIError(message: "ssh-workspace-attach requires --workspace-id <uuid>", exitCode: 1)
        }

        let target = try resolveDetachedWorkspaceTarget(
            workspaceID: workspaceID,
            hostOpt: hostOpt,
            slotOpt: slotOpt,
            timeout: 5
        )
        if let attached = try findAttachedWorkspaceForSnapshot(target: target, client: client) {
            let payload = alreadyAttachedWorkspacePayload(
                requestedWorkspaceID: workspaceID,
                target: target,
                attached: attached
            )
            if jsonOutput {
                print(jsonString(payload))
            } else {
                let title = (payload["title"] as? String) ?? "workspace"
                print("Workspace \(workspaceID) (\(title)) is already attached from \(target.host.host):\(target.slot)")
            }
            return
        }
        let title = nonEmpty(target.snapshot?.title) ?? "Detached \(String(workspaceID.prefix(8)))"
        let localWorkspaceID = try createConfiguredWorkspaceForSnapshot(
            title: title,
            target: target,
            preferredWorkspaceID: workspaceID,
            windowRaw: windowOpt ?? windowOverride,
            client: client
        )
        try waitForRemoteDaemonReady(workspaceID: localWorkspaceID, client: client, timeout: 45)
        let restored = try client.sendV2(
            method: "workspace.remote.snapshot_restore",
            params: ["workspace_id": localWorkspaceID],
            responseTimeout: 60
        )
        if jsonOutput {
            print(jsonString(restored))
            return
        }
        let panesRestored = (restored["panes_restored"] as? Int) ?? 0
        let panesLost = (restored["panes_lost"] as? Int) ?? 0
        let restoredTitle = (restored["title"] as? String) ?? title
        print("Attached workspace \(workspaceID) (\(restoredTitle)) from \(target.host.host):\(target.slot); \(panesRestored) panes restored, \(panesLost) sessions lost")
    }

    func runSSHWorkspaceSnapshotClear(
        commandArgs: [String],
        client: SocketClient,
        jsonOutput: Bool,
        idFormat: CLIIDFormat
    ) throws {
        let spec = CMUXSnapshotSubcommand(
            name: "ssh-workspace-snapshot-clear",
            usage: "cmux ssh-workspace-snapshot-clear (--workspace-id <uuid> | --host <h> --slot <s>) [--force]",
            valueFlags: [
                .init(name: "--workspace-id", descriptor: "<uuid>"),
                .init(name: "--host", descriptor: "<h>"),
                .init(name: "--slot", descriptor: "<s>"),
            ],
            boolFlags: ["--force"]
        )
        let parsed = try parseSnapshotSubcommand(commandArgs, spec: spec)
        let workspaceIDOpt = parsed.value("--workspace-id")
        let hostOpt = parsed.value("--host")
        let slotOpt = parsed.value("--slot")
        let force = parsed.bool("--force")
        let target: DetachedWorkspaceResolvedTarget
        if let workspaceID = canonicalDetachedWorkspaceID(workspaceIDOpt) {
            target = try resolveDetachedWorkspaceTarget(
                workspaceID: workspaceID,
                hostOpt: hostOpt,
                slotOpt: slotOpt,
                timeout: 5
            )
        } else if nonEmpty(workspaceIDOpt) != nil {
            throw CLIError(message: "ssh-workspace-snapshot-clear requires --workspace-id <uuid> or --host <h> --slot <s>")
        } else if nonEmpty(hostOpt) != nil, nonEmpty(slotOpt) != nil {
            target = try resolveDetachedWorkspaceTarget(
                workspaceID: nil,
                hostOpt: hostOpt,
                slotOpt: slotOpt,
                timeout: 5
            )
        } else {
            throw CLIError(message: "ssh-workspace-snapshot-clear requires --workspace-id <uuid> or --host <h> --slot <s>")
        }

        let localWorkspaceID = try createConfiguredWorkspaceForSnapshot(
            title: "snapshot-clear \(target.host.host):\(target.slot)",
            target: target,
            windowRaw: nil,
            client: client
        )
        do {
            try waitForRemoteDaemonReady(workspaceID: localWorkspaceID, client: client, timeout: force ? 20 : 45)
            let cleared = try client.sendV2(
                method: "workspace.remote.snapshot_clear",
                params: ["workspace_id": localWorkspaceID],
                responseTimeout: 30
            )
            _ = try? client.sendV2(method: "workspace.close", params: ["workspace_id": localWorkspaceID])
            if jsonOutput {
                print(jsonString(cleared))
            } else {
                let didClear = (cleared["cleared"] as? Bool) == true
                print(didClear ? "Cleared snapshot \(target.host.host):\(target.slot)" : "No snapshot to clear at \(target.host.host):\(target.slot)")
            }
        } catch {
            _ = try? client.sendV2(method: "workspace.close", params: ["workspace_id": localWorkspaceID])
            throw error
        }
    }

    func runSSHHostList(commandArgs: [String], jsonOutput: Bool) throws {
        let spec = CMUXSnapshotSubcommand(
            name: "ssh-host-list",
            usage: "cmux ssh-host-list [--json]"
        )
        _ = try parseSnapshotSubcommand(commandArgs, spec: spec)
        let registry = try DetachedWorkspaceRegistry.load()
        if jsonOutput {
            print(jsonString(try registryJSONObject(registry)))
            return
        }
        if registry.hosts.isEmpty {
            print("No detached workspace hosts")
            return
        }
        printTable(
            headers: ["HOST", "PORT", "DAEMON", "LAST SEEN"],
            rows: registry.hosts.sorted { $0.host < $1.host }.map { host in
                [
                    host.host,
                    host.port.map(String.init) ?? "",
                    host.daemonBinPath,
                    DetachedWorkspaceDates.string(host.lastSeenAt),
                ]
            }
        )
    }

    func runSSHHostForget(commandArgs: [String], jsonOutput: Bool) throws {
        let spec = CMUXSnapshotSubcommand(
            name: "ssh-host-forget",
            usage: "cmux ssh-host-forget --host <h> [--force]",
            valueFlags: [.init(name: "--host", descriptor: "<h>")],
            boolFlags: ["--force"]
        )
        let parsed = try parseSnapshotSubcommand(commandArgs, spec: spec)
        let force = parsed.bool("--force")
        guard let host = nonEmpty(parsed.value("--host")) else {
            throw CLIError(message: "Usage: cmux ssh-host-forget --host <h> [--force]")
        }

        let registry = try DetachedWorkspaceRegistry.load()
        guard let record = registry.hosts.first(where: { $0.host == host }) else {
            throw CLIError(message: "ssh-host-forget: host not found in registry: \(host)")
        }

        if !force {
            let result = listDetachedWorkspaces(on: record, timeout: 5)
            if result.error == nil, !result.snapshots.isEmpty {
                throw CLIError(message: "ssh-host-forget: \(host) still has \(result.snapshots.count) detached workspace snapshot\(result.snapshots.count == 1 ? "" : "s"). Re-run with --force to forget the host anyway.")
            }
        }

        let removed = try DetachedWorkspaceRegistry.remove(host: host)
        let payload: [String: Any] = ["host": host, "removed": removed]
        if jsonOutput {
            print(jsonString(payload))
        } else if removed {
            print("Forgot \(host)")
        } else {
            print("Host was already absent: \(host)")
        }
    }

    func runSSHWorkspaceListDetached(commandArgs: [String], jsonOutput: Bool) throws {
        let spec = CMUXSnapshotSubcommand(
            name: "ssh-workspace-list-detached",
            usage: "cmux ssh-workspace-list-detached [--host <h>] [--json] [--timeout <secs>]",
            valueFlags: [
                .init(name: "--host", descriptor: "<h>"),
                .init(name: "--timeout", descriptor: "<secs>"),
            ]
        )
        let parsed = try parseSnapshotSubcommand(commandArgs, spec: spec)
        let hostOpt = parsed.value("--host")
        let timeout = try parseTimeout(parsed.value("--timeout"), defaultValue: 5)
        let registry = try DetachedWorkspaceRegistry.load()
        let hosts: [DetachedWorkspaceHostRecord]
        if let host = nonEmpty(hostOpt) {
            if let existing = registry.hosts.first(where: { $0.host == host }) {
                hosts = [existing]
            } else {
                hosts = [DetachedWorkspaceHostRecord(
                    host: host,
                    port: nil,
                    identityFile: nil,
                    sshOptions: [],
                    daemonBinPath: "~/.cmux/bin/cmuxd-remote",
                    addedAt: Date(),
                    lastSeenAt: Date()
                )]
            }
        } else {
            hosts = registry.hosts
            if hosts.isEmpty {
                throw CLIError(message: "ssh-workspace-list-detached: host registry is empty", exitCode: 1)
            }
        }

        let results = listDetachedWorkspaces(on: hosts, timeout: timeout)
        let reachable = results.filter { $0.error == nil }.count
        if jsonOutput {
            print(jsonString(listDetachedJSONObject(results)))
        } else {
            printDetachedWorkspaceTable(results)
        }
        if reachable == 0, !results.isEmpty {
            throw CLIError(message: "ssh-workspace-list-detached: all hosts unreachable", exitCode: 2)
        }
    }

    private func registryJSONObject(_ registry: DetachedWorkspaceHostRegistryFile) throws -> [String: Any] {
        [
            "version": registry.version,
            "hosts": registry.hosts.sorted { $0.host < $1.host }.map { host in
                [
                    "host": host.host,
                    "port": host.port as Any? ?? NSNull(),
                    "identity_file": host.identityFile as Any? ?? NSNull(),
                    "ssh_options": host.sshOptions,
                    "daemon_bin_path": host.daemonBinPath,
                    "added_at": DetachedWorkspaceDates.string(host.addedAt),
                    "last_seen_at": DetachedWorkspaceDates.string(host.lastSeenAt),
                ] as [String: Any]
            },
        ]
    }

    private func listDetachedJSONObject(_ results: [DetachedWorkspaceHostListResult]) -> [String: Any] {
        [
            "hosts": results.map { result in
                var hostPayload: [String: Any] = ["host": result.host]
                if let error = result.error {
                    hostPayload["error"] = error
                    return hostPayload
                }
                hostPayload["scanned_at"] = DetachedWorkspaceDates.string(result.scannedAt)
                hostPayload["snapshots"] = result.snapshots.map(snapshotJSONObject)
                return hostPayload
            },
        ]
    }

    private func snapshotJSONObject(_ snapshot: DetachedWorkspaceSnapshotEntry) -> [String: Any] {
        var payload: [String: Any] = ["slot": snapshot.slot]
        payload["workspace_id"] = snapshot.workspaceID ?? NSNull()
        payload["title"] = snapshot.title ?? NSNull()
        payload["status"] = snapshotStatus(snapshot)
        payload["detached_at"] = DetachedWorkspaceDates.string(snapshot.detachedAt)
        payload["updated_at"] = DetachedWorkspaceDates.string(snapshot.updatedAt)
        payload["schema_version"] = snapshot.schemaVersion ?? NSNull()
        payload["snapshot_sha256"] = snapshot.snapshotSHA256 ?? NSNull()
        payload["body_byte_length"] = snapshot.bodyByteLength ?? NSNull()
        payload["body_present"] = snapshot.bodyPresent ?? false
        if let error = snapshot.error {
            payload["error"] = error
        }
        return payload
    }

    private func listDetachedWorkspaces(on hosts: [DetachedWorkspaceHostRecord], timeout: TimeInterval) -> [DetachedWorkspaceHostListResult] {
        let queue = DispatchQueue(label: "cmux.remote-workspace-snapshots.list", attributes: .concurrent)
        let group = DispatchGroup()
        let lock = NSLock()
        var results: [DetachedWorkspaceHostListResult] = []

        for host in hosts {
            group.enter()
            queue.async {
                let result = self.listDetachedWorkspaces(on: host, timeout: timeout)
                lock.lock()
                results.append(result)
                lock.unlock()
                group.leave()
            }
        }
        group.wait()
        return results.sorted { $0.host < $1.host }
    }

    private func resolveDetachedWorkspaceTarget(
        workspaceID: String?,
        hostOpt: String?,
        slotOpt: String?,
        timeout: TimeInterval
    ) throws -> DetachedWorkspaceResolvedTarget {
        let registry = try DetachedWorkspaceRegistry.load()
        let hostValue = nonEmpty(hostOpt)
        let slotValue = nonEmpty(slotOpt)
        if let hostValue, let slotValue {
            let host = registry.hosts.first(where: { $0.host == hostValue }) ?? DetachedWorkspaceHostRecord(
                host: hostValue,
                port: nil,
                identityFile: nil,
                sshOptions: [],
                daemonBinPath: "~/.cmux/bin/cmuxd-remote",
                addedAt: Date(),
                lastSeenAt: Date()
            )
            return DetachedWorkspaceResolvedTarget(host: host, slot: slotValue, snapshot: nil)
        }

        let hosts: [DetachedWorkspaceHostRecord]
        if let hostValue {
            if let host = registry.hosts.first(where: { $0.host == hostValue }) {
                hosts = [host]
            } else {
                hosts = [DetachedWorkspaceHostRecord(
                    host: hostValue,
                    port: nil,
                    identityFile: nil,
                    sshOptions: [],
                    daemonBinPath: "~/.cmux/bin/cmuxd-remote",
                    addedAt: Date(),
                    lastSeenAt: Date()
                )]
            }
        } else {
            guard !registry.hosts.isEmpty else {
                throw CLIError(message: "ssh-workspace-attach: host registry is empty", exitCode: 1)
            }
            hosts = registry.hosts
        }

        guard let workspaceID else {
            throw CLIError(message: "Resolving by --host requires --slot when --workspace-id is omitted")
        }
        let results = listDetachedWorkspaces(on: hosts, timeout: timeout)
        var matches: [(DetachedWorkspaceHostRecord, DetachedWorkspaceSnapshotEntry)] = []
        for result in results where result.error == nil {
            guard let host = hosts.first(where: { $0.host == result.host }) else {
                continue
            }
            for snapshot in result.snapshots where detachedWorkspaceID(snapshot.workspaceID, matches: workspaceID) {
                matches.append((host, snapshot))
            }
        }
        if matches.isEmpty {
            throw CLIError(message: "Detached workspace not found: \(workspaceID)", exitCode: 1)
        }
        if matches.count > 1, hostValue == nil || slotValue == nil {
            throw CLIError(message: "Detached workspace \(workspaceID) is ambiguous; pass --host and --slot", exitCode: 1)
        }
        let match = matches[0]
        if let slotValue, match.1.slot != slotValue {
            throw CLIError(message: "Detached workspace \(workspaceID) was found on slot \(match.1.slot), not \(slotValue)", exitCode: 1)
        }
        return DetachedWorkspaceResolvedTarget(host: match.0, slot: match.1.slot, snapshot: match.1)
    }

    private func createConfiguredWorkspaceForSnapshot(
        title: String,
        target: DetachedWorkspaceResolvedTarget,
        preferredWorkspaceID: String? = nil,
        windowRaw: String?,
        client: SocketClient
    ) throws -> String {
        var createParams: [String: Any] = [
            "title": title,
            "focus": true,
        ]
        if let preferredWorkspaceID = nonEmpty(preferredWorkspaceID) {
            createParams["preferred_workspace_id"] = preferredWorkspaceID
        } else if let snapshotWorkspaceID = target.snapshot?.workspaceID {
            createParams["preferred_workspace_id"] = snapshotWorkspaceID
        }
        if let windowRaw = nonEmpty(windowRaw),
           let windowID = try validatedWindowHandle(windowRaw, client: client) {
            createParams["window_id"] = windowID
        }
        let created = try client.sendV2(method: "workspace.create", params: createParams)
        guard let workspaceID = created["workspace_id"] as? String, !workspaceID.isEmpty else {
            throw CLIError(message: "workspace.create did not return workspace_id")
        }
        var configureParams: [String: Any] = [
            "workspace_id": workspaceID,
            "destination": target.host.host,
            "auto_connect": true,
            "preserve_after_terminal_exit": true,
            "persistent_daemon_slot": target.slot,
        ]
        if let port = target.host.port {
            configureParams["port"] = port
        }
        if let identityFile = nonEmpty(target.host.identityFile) {
            configureParams["identity_file"] = identityFile
        }
        let relayPort = Int.random(in: 49152...65535)
        let relayID = UUID().uuidString.lowercased()
        let relayToken = detachedWorkspaceRelayTokenHex()
        let sshOptions = sshOptionsWithDetachedWorkspaceRestoreDefaults(
            target.host.sshOptions,
            relayPort: relayPort
        )
        if !sshOptions.isEmpty {
            configureParams["ssh_options"] = sshOptions
        }
        configureParams["relay_port"] = relayPort
        configureParams["relay_id"] = relayID
        configureParams["relay_token"] = relayToken
        configureParams["local_socket_path"] = client.socketPath
        configureParams["foreground_auth_token"] = UUID().uuidString.lowercased()
        do {
            _ = try client.sendV2(method: "workspace.remote.configure", params: configureParams)
            return workspaceID
        } catch {
            _ = try? client.sendV2(method: "workspace.close", params: ["workspace_id": workspaceID])
            throw error
        }
    }

    private func findAttachedWorkspaceForSnapshot(
        target: DetachedWorkspaceResolvedTarget,
        client: SocketClient
    ) throws -> [String: Any]? {
        let payload = try client.sendV2(method: "workspace.remote.snapshot_find_attached", params: [
            "host": target.host.host,
            "persistent_daemon_slot": target.slot,
            "focus": true,
        ])
        return (payload["exists"] as? Bool) == true ? payload : nil
    }

    private func alreadyAttachedWorkspacePayload(
        requestedWorkspaceID: String,
        target: DetachedWorkspaceResolvedTarget,
        attached: [String: Any]
    ) -> [String: Any] {
        var payload: [String: Any] = [
            "workspace_id": requestedWorkspaceID,
            "host": target.host.host,
            "persistent_daemon_slot": target.slot,
            "panes_restored": 0,
            "panes_lost": 0,
            "already_attached": true,
        ]
        if let title = attached["title"] as? String {
            payload["title"] = title
        } else if let title = target.snapshot?.title {
            payload["title"] = title
        }
        if let windowID = attached["window_id"] {
            payload["window_id"] = windowID
        }
        if let remote = attached["remote"] {
            payload["remote"] = remote
        }
        return payload
    }

    private func detachedWorkspaceRelayTokenHex() -> String {
        (UUID().uuidString + UUID().uuidString)
            .replacingOccurrences(of: "-", with: "")
            .lowercased()
    }

    private func sshOptionsWithDetachedWorkspaceRestoreDefaults(
        _ options: [String],
        relayPort: Int
    ) -> [String] {
        var merged = options
            .map { $0.trimmingCharacters(in: .whitespacesAndNewlines) }
            .filter { !$0.isEmpty }
            .filter { option in
                guard let key = detachedWorkspaceSSHOptionKey(option) else { return true }
                return !["controlmaster", "controlpersist", "controlpath"].contains(key)
            }
        if !hasDetachedWorkspaceSSHOptionKey(merged, key: "StrictHostKeyChecking") {
            merged.append("StrictHostKeyChecking=accept-new")
        }
        let controlMaster = detachedWorkspaceSSHOptionValue(named: "ControlMaster", in: merged)
        let controlMasterDisabled = detachedWorkspaceSSHOptionValueIsDisabled(controlMaster)
        if controlMaster == nil {
            merged.append("ControlMaster=auto")
        }
        if !controlMasterDisabled {
            if !hasDetachedWorkspaceSSHOptionKey(merged, key: "ControlPersist") {
                merged.append("ControlPersist=600")
            }
            if !hasDetachedWorkspaceSSHOptionKey(merged, key: "ControlPath") {
                merged.append("ControlPath=/tmp/cmux-ssh-\(getuid())-\(relayPort)-%C")
            }
        }
        return merged
    }

    private func detachedWorkspaceSSHOptionKey(_ option: String) -> String? {
        let trimmed = option.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return nil }
        if let equals = trimmed.firstIndex(of: "=") {
            return String(trimmed[..<equals])
                .trimmingCharacters(in: .whitespacesAndNewlines)
                .lowercased()
        }
        return trimmed
            .split(maxSplits: 1, whereSeparator: { $0.isWhitespace })
            .first
            .map(String.init)?
            .lowercased()
    }

    private func hasDetachedWorkspaceSSHOptionKey(_ options: [String], key: String) -> Bool {
        detachedWorkspaceSSHOptionValue(named: key, in: options) != nil
    }

    private func detachedWorkspaceSSHOptionValue(named name: String, in options: [String]) -> String? {
        let loweredName = name.lowercased()
        for option in options {
            let trimmed = option.trimmingCharacters(in: .whitespacesAndNewlines)
            guard !trimmed.isEmpty else { continue }
            if let equals = trimmed.firstIndex(of: "=") {
                let key = String(trimmed[..<equals]).trimmingCharacters(in: .whitespacesAndNewlines)
                guard key.lowercased() == loweredName else { continue }
                let value = String(trimmed[trimmed.index(after: equals)...]).trimmingCharacters(in: .whitespacesAndNewlines)
                return value.isEmpty ? nil : value
            }
            let parts = trimmed.split(maxSplits: 1, whereSeparator: { $0.isWhitespace })
            guard parts.first.map({ String($0).lowercased() }) == loweredName else { continue }
            guard parts.count > 1 else { return nil }
            let value = String(parts[1]).trimmingCharacters(in: .whitespacesAndNewlines)
            return value.isEmpty ? nil : value
        }
        return nil
    }

    private func detachedWorkspaceSSHOptionValueIsDisabled(
        _ rawValue: String?,
        zeroIsDisabled: Bool = true
    ) -> Bool {
        guard let normalized = rawValue?.trimmingCharacters(in: .whitespacesAndNewlines).lowercased() else {
            return false
        }
        return ["no", "false", "off"].contains(normalized) || (zeroIsDisabled && normalized == "0")
    }

    private func waitForRemoteDaemonReady(
        workspaceID: String,
        client: SocketClient,
        timeout: TimeInterval
    ) throws {
        let deadline = Date().addingTimeInterval(timeout)
        var lastState = "unknown"
        var lastDetail = ""
        while Date() < deadline {
            let status = try client.sendV2(method: "workspace.remote.status", params: ["workspace_id": workspaceID])
            if let remote = status["remote"] as? [String: Any],
               let daemon = remote["daemon"] as? [String: Any] {
                lastState = (daemon["state"] as? String) ?? lastState
                lastDetail = (daemon["detail"] as? String) ?? lastDetail
                if lastState == "ready" {
                    return
                }
            }
            Thread.sleep(forTimeInterval: 0.25)
        }
        throw CLIError(message: "remote daemon did not become ready before timeout (state=\(lastState)\(lastDetail.isEmpty ? "" : " detail=\(lastDetail)"))", exitCode: 2)
    }

    private func listDetachedWorkspaces(on host: DetachedWorkspaceHostRecord, timeout: TimeInterval) -> DetachedWorkspaceHostListResult {
        do {
            let data = try runSnapshotListAllSSH(host: host, timeout: timeout)
            let response = try DetachedWorkspaceDates.decoder().decode(DetachedWorkspaceListAllResponse.self, from: data)
            return DetachedWorkspaceHostListResult(
                host: host.host,
                scannedAt: response.scannedAt,
                snapshots: response.snapshots
                    .filter { snapshotStatus($0) == "detached" }
                    .sorted { $0.slot < $1.slot },
                error: nil
            )
        } catch {
            return DetachedWorkspaceHostListResult(host: host.host, scannedAt: nil, snapshots: [], error: String(describing: error))
        }
    }

    private func runSnapshotListAllSSH(host: DetachedWorkspaceHostRecord, timeout: TimeInterval) throws -> Data {
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

        let timeoutResult = waitForProcess(process, timeout: timeout + 1)
        if !timeoutResult {
            process.terminate()
            _ = waitForProcess(process, timeout: 1)
            throw CLIError(message: "ssh timed out after \(formatTimeout(timeout))s")
        }

        let output = stdout.fileHandleForReading.readDataToEndOfFile()
        let errorData = stderr.fileHandleForReading.readDataToEndOfFile()
        guard process.terminationStatus == 0 else {
            let stderrText = String(data: errorData, encoding: .utf8)?
                .trimmingCharacters(in: .whitespacesAndNewlines)
            throw CLIError(message: stderrText?.isEmpty == false ? stderrText! : "ssh exited \(process.terminationStatus)")
        }
        return output
    }

    private func sshArguments(for host: DetachedWorkspaceHostRecord, timeout: TimeInterval) -> [String] {
        var args: [String] = [
            "-o", "BatchMode=yes",
            "-o", "ConnectTimeout=\(max(1, Int(timeout.rounded(.up))))",
        ]
        if let port = host.port, port > 0 {
            args += ["-p", String(port)]
        }
        if let identity = nonEmpty(host.identityFile) {
            args += ["-i", identity]
        }
        for option in host.sshOptions where !option.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
            args += ["-o", option]
        }
        return args
    }

    private func waitForProcess(_ process: Process, timeout: TimeInterval) -> Bool {
        let deadline = Date().addingTimeInterval(max(0.1, timeout))
        while process.isRunning {
            if Date() >= deadline {
                return false
            }
            Thread.sleep(forTimeInterval: 0.05)
        }
        return true
    }

    private func printDetachedWorkspaceTable(_ results: [DetachedWorkspaceHostListResult]) {
        var rows: [[String]] = []
        var errors: [String] = []
        for result in results {
            if let error = result.error {
                errors.append("\(result.host): \(error)")
                continue
            }
            for snapshot in result.snapshots {
                rows.append([
                    result.host,
                    snapshot.slot,
                    shortWorkspaceID(snapshot.workspaceID),
                    quotedTitle(snapshot.title),
                    DetachedWorkspaceDates.string(snapshot.detachedAt),
                ])
            }
        }

        if rows.isEmpty {
            print("No detached workspaces")
        } else {
            printTable(headers: ["HOST", "SLOT", "WORKSPACE", "TITLE", "DETACHED"], rows: rows)
        }
        if !errors.isEmpty {
            FileHandle.standardError.write(Data(("\nUnreachable hosts:\n" + errors.map { "  \($0)" }.joined(separator: "\n") + "\n").utf8))
        }
    }

    private func printTable(headers: [String], rows: [[String]]) {
        let widths = headers.enumerated().map { index, header in
            max(header.count, rows.map { index < $0.count ? $0[index].count : 0 }.max() ?? 0)
        }
        let headerLine = headers.enumerated().map { index, value in
            value.padding(toLength: widths[index], withPad: " ", startingAt: 0)
        }.joined(separator: "  ")
        print(headerLine)
        for row in rows {
            print(row.enumerated().map { index, value in
                value.padding(toLength: widths[index], withPad: " ", startingAt: 0)
            }.joined(separator: "  "))
        }
    }

    private func parseTimeout(_ raw: String?, defaultValue: TimeInterval) throws -> TimeInterval {
        guard let raw = nonEmpty(raw) else { return defaultValue }
        guard let timeout = TimeInterval(raw), timeout > 0 else {
            throw CLIError(message: "timeout must be a positive number of seconds")
        }
        return timeout
    }

    private func formatTimeout(_ timeout: TimeInterval) -> String {
        let rounded = timeout.rounded()
        if abs(timeout - rounded) < 0.001 {
            return String(Int(rounded))
        }
        return String(format: "%.1f", timeout)
    }

    private func shortWorkspaceID(_ workspaceID: String?) -> String {
        guard let workspaceID = nonEmpty(workspaceID) else { return "-" }
        if workspaceID.count <= 8 { return workspaceID }
        return "\(workspaceID.prefix(4))-..."
    }

    private func quotedTitle(_ title: String?) -> String {
        guard let title = nonEmpty(title) else { return "\"\"" }
        return "\"\(title.replacingOccurrences(of: "\"", with: "\\\""))\""
    }

    private func snapshotStatus(_ snapshot: DetachedWorkspaceSnapshotEntry) -> String {
        let value = snapshot.status?
            .trimmingCharacters(in: .whitespacesAndNewlines)
            .lowercased()
        if value == "live" || value == "detached" {
            return value!
        }
        return "detached"
    }

    private func canonicalDetachedWorkspaceID(_ value: String?) -> String? {
        guard let raw = nonEmpty(value), let uuid = UUID(uuidString: raw) else { return nil }
        return uuid.uuidString
    }

    private func detachedWorkspaceID(_ candidate: String?, matches requested: String) -> Bool {
        guard let candidate = canonicalDetachedWorkspaceID(candidate),
              let requested = canonicalDetachedWorkspaceID(requested) else {
            return false
        }
        return candidate == requested
    }

    private func nonEmpty(_ value: String?) -> String? {
        let trimmed = value?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        return trimmed.isEmpty ? nil : trimmed
    }

    private func localIsFlagToken(_ value: String) -> Bool {
        value.hasPrefix("-") && value != "-"
    }
}
