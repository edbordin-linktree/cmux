import XCTest

#if canImport(cmux_DEV)
@testable import cmux_DEV
#elseif canImport(cmux)
@testable import cmux
#endif

final class RemoteWorkspaceSnapshotTests: XCTestCase {
    func testRemoteWorkspaceSnapshotV1RoundTrips() throws {
        let original = makeSnapshot()
        let data = try RemoteWorkspaceSnapshotCodec.encode(original)
        let decoded = try RemoteWorkspaceSnapshotCodec.decode(data)
        XCTAssertEqual(decoded, original)
    }

    func testVersionProbeRejectsUnknownVersions() throws {
        let body = #"{"version":2,"workspaceId":"3F4A8D21-6A8F-4EF9-A979-7D712F2A8D9E"}"#
        XCTAssertThrowsError(try RemoteWorkspaceSnapshotCodec.decode(Data(body.utf8))) { error in
            XCTAssertEqual(
                error as? RemoteWorkspaceSnapshotCodec.SnapshotError,
                .unknownVersion(2)
            )
            XCTAssertTrue(error.localizedDescription.contains("ssh-workspace-snapshot-clear"))
        }
    }

    func testSHA256IsStableAcrossReencodes() throws {
        let snapshot = makeSnapshot()
        let firstData = try RemoteWorkspaceSnapshotCodec.encode(snapshot)
        let decoded = try RemoteWorkspaceSnapshotCodec.decode(firstData)
        let secondData = try RemoteWorkspaceSnapshotCodec.encode(decoded)

        XCTAssertEqual(firstData, secondData)
        XCTAssertEqual(
            RemoteWorkspaceSnapshotCodec.sha256Hex(for: firstData),
            RemoteWorkspaceSnapshotCodec.sha256Hex(for: secondData)
        )
    }

    func testEncoderSortsKeysAndDoesNotEscapeSlashes() throws {
        let snapshot = makeSnapshot(browserURL: "https://example.com/path?q=1")
        let body = try RemoteWorkspaceSnapshotCodec.encodeString(snapshot)

        XCTAssertTrue(body.contains(#""currentURL":"https://example.com/path?q=1""#))
        XCTAssertFalse(body.contains(#"https:\/\/example.com"#))
        XCTAssertLessThan(
            try XCTUnwrap(body.range(of: #""activePaneId""#)?.lowerBound),
            try XCTUnwrap(body.range(of: #""detachedAt""#)?.lowerBound)
        )
    }

    func testBrowserPaneSnapshotCapturesURLAndTitleOnly() throws {
        let paneId = UUID(uuidString: "AAAAAAAA-AAAA-4AAA-AAAA-AAAAAAAAAAAA")!
        let snapshot = BrowserPaneSnapshot(
            paneId: paneId,
            currentURL: "https://example.com/work",
            title: "Example"
        )
        let data = try RemoteWorkspaceSnapshotCodec.encoder().encode(snapshot)
        let object = try XCTUnwrap(
            JSONSerialization.jsonObject(with: data) as? [String: Any]
        )

        XCTAssertEqual(Set(object.keys), ["paneId", "currentURL", "title"])
        XCTAssertEqual(object["currentURL"] as? String, "https://example.com/work")
        XCTAssertEqual(object["title"] as? String, "Example")
    }

    func testTerminalPaneAgentKindRoundTrips() throws {
        let pane = TerminalPaneSnapshot(
            paneId: UUID(uuidString: "BBBBBBBB-BBBB-4BBB-BBBB-BBBBBBBBBBBB")!,
            remotePTYSessionId: "pty-1",
            title: "Claude",
            cwdHint: "~/project",
            agentKind: "claude"
        )
        let data = try RemoteWorkspaceSnapshotCodec.encoder().encode(pane)
        let decoded = try RemoteWorkspaceSnapshotCodec.decoder().decode(TerminalPaneSnapshot.self, from: data)

        XCTAssertEqual(decoded, pane)
        XCTAssertEqual(decoded.agentKind, "claude")
    }

    func testPaneSnapshotUsesExplicitTypeDiscriminator() throws {
        let snapshot = PaneSnapshot.markdownViewer(
            MarkdownViewerPaneSnapshot(
                paneId: UUID(uuidString: "CCCCCCCC-CCCC-4CCC-CCCC-CCCCCCCCCCCC")!,
                path: "/tmp/readme.md"
            )
        )
        let data = try RemoteWorkspaceSnapshotCodec.encoder().encode(snapshot)
        let object = try XCTUnwrap(
            JSONSerialization.jsonObject(with: data) as? [String: Any]
        )

        XCTAssertEqual(object["type"] as? String, "markdownViewer")
        XCTAssertNotNil(object["markdownViewer"])
        XCTAssertEqual(
            try RemoteWorkspaceSnapshotCodec.decoder().decode(PaneSnapshot.self, from: data),
            snapshot
        )
    }

    private func makeSnapshot(browserURL: String = "https://example.com/work") -> RemoteWorkspaceSnapshotV1 {
        let terminalPaneId = UUID(uuidString: "11111111-1111-4111-8111-111111111111")!
        let browserPaneId = UUID(uuidString: "22222222-2222-4222-8222-222222222222")!
        return RemoteWorkspaceSnapshotV1(
            workspaceId: UUID(uuidString: "3F4A8D21-6A8F-4EF9-A979-7D712F2A8D9E")!,
            title: "training run",
            detachedAt: Date(timeIntervalSince1970: 1_779_854_523.123),
            splitTree: .split(
                SessionSplitLayoutSnapshot(
                    orientation: .horizontal,
                    dividerPosition: 0.42,
                    first: .pane(
                        SessionPaneLayoutSnapshot(
                            panelIds: [terminalPaneId],
                            selectedPanelId: terminalPaneId
                        )
                    ),
                    second: .pane(
                        SessionPaneLayoutSnapshot(
                            panelIds: [browserPaneId],
                            selectedPanelId: browserPaneId
                        )
                    )
                )
            ),
            panes: [
                .terminal(
                    TerminalPaneSnapshot(
                        paneId: terminalPaneId,
                        remotePTYSessionId: "pty-terminal-1",
                        title: "Claude",
                        cwdHint: "~/cmux",
                        agentKind: "claude"
                    )
                ),
                .browser(
                    BrowserPaneSnapshot(
                        paneId: browserPaneId,
                        currentURL: browserURL,
                        title: "Example"
                    )
                ),
            ],
            activePaneId: terminalPaneId,
            displayTarget: "user@host:ws-1738027182"
        )
    }
}
