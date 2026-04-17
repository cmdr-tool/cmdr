import Foundation
import FoundationModels

@main struct Summarize {
    static func main() async throws {
        let input = String(data: FileHandle.standardInput.readDataToEndOfFile(), encoding: .utf8) ?? ""

        guard case .available = SystemLanguageModel.default.availability else {
            print("{\"error\":\"Apple Intelligence not available\"}")
            exit(1)
        }

        let session = LanguageModelSession()
        let response = try await session.respond(
            to: "Generate a concise title (max 80 chars) for this task or content. Return only the title, no explanation: \(input)"
        )

        let title = response.content
            .trimmingCharacters(in: .whitespacesAndNewlines)
            .replacingOccurrences(of: "\"", with: "\\\"")

        print("{\"title\":\"\(title)\"}")
    }
}
