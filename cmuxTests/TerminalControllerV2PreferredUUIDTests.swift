import Foundation
import Testing

#if canImport(cmux_DEV)
@testable import cmux_DEV
#elseif canImport(cmux)
@testable import cmux
#endif

@Suite("v2 preferred UUID param validation")
@MainActor
struct TerminalControllerV2PreferredUUIDTests {
    private var controller: TerminalController { TerminalController.shared }

    @Test("Absent param is allowed")
    func absentParamIsAllowed() {
        let result = controller.v2ValidatePreferredUUIDParam(
            [:],
            key: "preferred_workspace_id"
        )
        #expect(result == nil)
    }

    @Test("Valid UUID string is allowed")
    func validUUIDIsAllowed() {
        let result = controller.v2ValidatePreferredUUIDParam(
            ["preferred_workspace_id": "550E8400-E29B-41D4-A716-446655440000"],
            key: "preferred_workspace_id"
        )
        #expect(result == nil)
    }

    @Test("Lowercase UUID string is allowed")
    func lowercaseUUIDIsAllowed() {
        let result = controller.v2ValidatePreferredUUIDParam(
            ["preferred_workspace_id": "550e8400-e29b-41d4-a716-446655440000"],
            key: "preferred_workspace_id"
        )
        #expect(result == nil)
    }

    @Test("Unparseable string is rejected with invalid_params")
    func unparseableStringIsRejected() {
        let result = controller.v2ValidatePreferredUUIDParam(
            ["preferred_workspace_id": "not-a-uuid"],
            key: "preferred_workspace_id"
        )
        expectInvalidParamsError(result, paramName: "preferred_workspace_id")
    }

    @Test("Empty string is rejected with invalid_params")
    func emptyStringIsRejected() {
        let result = controller.v2ValidatePreferredUUIDParam(
            ["preferred_workspace_id": ""],
            key: "preferred_workspace_id"
        )
        expectInvalidParamsError(result, paramName: "preferred_workspace_id")
    }

    @Test("Non-string value is rejected with invalid_params")
    func nonStringValueIsRejected() {
        let result = controller.v2ValidatePreferredUUIDParam(
            ["preferred_workspace_id": 42],
            key: "preferred_workspace_id"
        )
        expectInvalidParamsError(result, paramName: "preferred_workspace_id")
    }

    @Test("NSNull is treated as absent")
    func nsNullIsTreatedAsAbsent() {
        let result = controller.v2ValidatePreferredUUIDParam(
            ["preferred_workspace_id": NSNull()],
            key: "preferred_workspace_id"
        )
        #expect(result == nil)
    }
}

private func expectInvalidParamsError(
    _ result: TerminalController.V2CallResult?,
    paramName: String,
    sourceLocation: SourceLocation = #_sourceLocation
) {
    guard let result else {
        Issue.record(
            "expected invalid_params error mentioning \(paramName); got nil (validation is a no-op)",
            sourceLocation: sourceLocation
        )
        return
    }
    switch result {
    case .err(let code, let message, _):
        #expect(code == "invalid_params", sourceLocation: sourceLocation)
        #expect(message.contains(paramName), sourceLocation: sourceLocation)
    case .ok:
        Issue.record(
            "expected invalid_params error mentioning \(paramName); got .ok",
            sourceLocation: sourceLocation
        )
    }
}
