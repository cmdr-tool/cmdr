import Foundation
import FoundationModels

@main struct Summarize {
    struct Input: Decodable {
        let content: String
        let hint: String?
    }

    static func main() async throws {
        let data = FileHandle.standardInput.readDataToEndOfFile()

        let input: Input
        do {
            input = try JSONDecoder().decode(Input.self, from: data)
        } catch {
            print("{\"error\":\"invalid input: \(error.localizedDescription)\"}")
            exit(1)
        }

        guard case .available = SystemLanguageModel.default.availability else {
            print("{\"error\":\"Apple Intelligence not available\"}")
            exit(1)
        }

        var prompt = "Generate a concise title (max 80 chars) for this task or content. Return only the title, no explanation."
        if let hint = input.hint, !hint.isEmpty {
            prompt += " " + hint
        }
        prompt += " Content: \(input.content)"

        let session = LanguageModelSession()
        let response = try await session.respond(to: prompt)

        let title = response.content
            .trimmingCharacters(in: .whitespacesAndNewlines)
            .replacingOccurrences(of: "\"", with: "\\\"")

        print("{\"title\":\"\(title)\"}")
    }
}
