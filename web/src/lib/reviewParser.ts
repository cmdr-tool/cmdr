/**
 * Parses a review markdown result into structured sections.
 *
 * Expected format from the review prompt:
 *   ### [N. Category] Finding Title
 *   **Lines:** ...
 *   **Issue:** ...
 *   **Why it matters:** ...
 *   **Suggestion:** ...
 *
 * Sections may also contain a user response blockquote:
 *   > User response:
 *   > some guidance here
 */

export interface ReviewSection {
	/** Original markdown for this section (header + body), for reconstruction */
	raw: string;
	/** Priority number (1-5) */
	number: number;
	/** Category label, e.g. "Architectural", "Consistency" */
	category: string;
	/** Finding title after the category bracket */
	title: string;
	/** Body markdown after the H3 header line */
	body: string;
	/** Extracted user note if a "> User response:" block exists */
	userNote: string | null;
}

export interface ParsedReview {
	/** Everything before the first numbered section */
	preamble: string;
	/** Parsed numbered sections */
	sections: ReviewSection[];
}

// Matches lines like:
//   ### [1. Architectural] `truncate` called in both layers
//   ### [1 — Architectural] `editMenu` defined but never registered
//   ### [P1] `updateSocialPost` handler passes raw `req.body`
//   ## [P1] finding title
//   ### 5. DRY] finding title (missing opening bracket)
//   ### 1. Architectural Soundness — finding title (no brackets at all)
//   ### 4. Consistency: finding title (colon separator)
const SECTION_RE_BRACKET = /^#{2,3} \[?(?:P)?(\d+)(?:[\.\s—\-–]+([^\]]*))?\]\s*(.+)$/;
const SECTION_RE_PLAIN = /^#{2,3} (?:P)?(\d+)[\.\s]+(\w[\w\s/]*?)\s*[—\-–:]\s*(.+)$/;

/**
 * Parse review markdown into preamble + sections.
 * Returns null if no numbered sections are found (caller should fall back to raw render).
 */
export function parseReviewSections(md: string): ParsedReview | null {
	const lines = md.split('\n');

	// Find all section start indices
	const sectionStarts: { index: number; number: number; category: string; title: string }[] = [];
	for (let i = 0; i < lines.length; i++) {
		const m = lines[i].match(SECTION_RE_BRACKET) || lines[i].match(SECTION_RE_PLAIN);
		if (m) {
			sectionStarts.push({ index: i, number: parseInt(m[1]), category: (m[2] || '').trim(), title: m[3].trim() });
		}
	}

	if (sectionStarts.length === 0) return null;

	const preamble = stripHrLines(lines.slice(0, sectionStarts[0].index).join('\n'));

	const sections: ReviewSection[] = sectionStarts.map((start, i) => {
		const endIndex = i < sectionStarts.length - 1 ? sectionStarts[i + 1].index : lines.length;
		const sectionLines = lines.slice(start.index, endIndex);
		const raw = stripHrLines(sectionLines.join('\n'));
		const body = stripHrLines(sectionLines.slice(1).join('\n'));
		const userNote = extractUserNote(body);

		return {
			raw,
			number: start.number,
			category: start.category,
			title: start.title,
			body,
			userNote
		};
	});

	return { preamble, sections };
}

/** Strip markdown horizontal rules (---, ***, ___) and trim surrounding blank lines */
function stripHrLines(text: string): string {
	return text
		.replace(/^\s*[-*_]{3,}\s*$/gm, '')
		.replace(/\n{3,}/g, '\n\n')
		.trim();
}

const USER_NOTE_RE = /^> User response:\s*\n((?:> .*(?:\n|$))*)/m;

function extractUserNote(body: string): string | null {
	const m = body.match(USER_NOTE_RE);
	if (!m) return null;
	// Strip leading "> " from each line
	return m[1]
		.split('\n')
		.map((l) => l.replace(/^> ?/, ''))
		.join('\n')
		.trim();
}

/**
 * Reconstruct the full markdown from a ParsedReview.
 * Used after removing sections or adding user notes.
 */
export function reconstructMarkdown(review: ParsedReview): string {
	const parts = [review.preamble];
	for (const section of review.sections) {
		parts.push(section.raw);
	}
	return parts.join('\n\n');
}

/**
 * Remove the user note blockquote from a section's raw markdown and body.
 */
function stripUserNote(text: string): string {
	// Remove the "> User response:" block and any trailing blank lines it leaves
	return text.replace(/\n*> User response:\s*\n((?:> .*(?:\n|$))*)/, '').trimEnd();
}

/**
 * Add or replace a user note in a section.
 * Returns an updated section with the note embedded in both raw and body.
 */
export function setSectionUserNote(section: ReviewSection, note: string | null): ReviewSection {
	// Strip any existing note first
	let cleanRaw = stripUserNote(section.raw);
	let cleanBody = stripUserNote(section.body);

	if (!note) {
		return { ...section, raw: cleanRaw, body: cleanBody, userNote: null };
	}

	const noteBlock = '\n\n> User response:\n' + note.split('\n').map((l) => `> ${l}`).join('\n');
	return {
		...section,
		raw: cleanRaw + noteBlock,
		body: cleanBody + noteBlock,
		userNote: note
	};
}
