// Text selection annotations with localStorage persistence.
// Uses prefix/exact/suffix anchoring (W3C Web Annotation Data Model approach)
// to survive minor reformatting of the rendered content.

const CONTEXT_CHARS = 30;
const STORAGE_PREFIX = 'annotations:';

export interface Annotation {
	id: string;
	prefix: string;
	exact: string;
	suffix: string;
	note: string;
}

export interface TextAnchor {
	prefix: string;
	exact: string;
	suffix: string;
}

// --- Text anchoring ---

/**
 * Build an anchor from the current Selection within a container element.
 * Returns null if the selection is empty or not within the container.
 */
export function anchorFromSelection(selection: Selection, container: HTMLElement): TextAnchor | null {
	if (selection.isCollapsed || selection.rangeCount === 0) return null;

	const range = selection.getRangeAt(0);
	if (!container.contains(range.startContainer) || !container.contains(range.endContainer)) return null;

	const exact = selection.toString().trim();
	if (!exact) return null;

	// Walk all text nodes to find the selection's character offset in the container's textContent
	const fullText = container.textContent ?? '';
	const selOffset = textOffsetOfRange(range, container);
	if (selOffset === -1) return null;

	// Find the trimmed exact text within the raw selection range
	const rawSelected = selection.toString();
	const trimStart = rawSelected.indexOf(exact.charAt(0));
	const adjustedOffset = selOffset + trimStart;

	const prefix = fullText.slice(Math.max(0, adjustedOffset - CONTEXT_CHARS), adjustedOffset);
	const suffix = fullText.slice(adjustedOffset + exact.length, adjustedOffset + exact.length + CONTEXT_CHARS);

	return { prefix, exact, suffix };
}

/**
 * Find a Range in the DOM that matches an anchor's text.
 * Tries prefix+exact+suffix first, falls back to exact-only.
 */
export function findAnchorRange(anchor: TextAnchor, container: HTMLElement): Range | null {
	const fullText = container.textContent ?? '';

	// Try full context match first
	let offset = findWithContext(fullText, anchor.prefix, anchor.exact, anchor.suffix);

	// Fall back to exact-only (handles minor reformatting)
	if (offset === -1) {
		offset = fullText.indexOf(anchor.exact);
	}

	if (offset === -1) return null;

	return rangeFromTextOffset(container, offset, anchor.exact.length);
}

// --- localStorage helpers ---

export function loadAnnotations(taskId: number): Annotation[] {
	try {
		const raw = localStorage.getItem(`${STORAGE_PREFIX}${taskId}`);
		return raw ? JSON.parse(raw) : [];
	} catch {
		return [];
	}
}

export function saveAnnotations(taskId: number, annotations: Annotation[]): void {
	if (annotations.length === 0) {
		localStorage.removeItem(`${STORAGE_PREFIX}${taskId}`);
	} else {
		localStorage.setItem(`${STORAGE_PREFIX}${taskId}`, JSON.stringify(annotations));
	}
}

/**
 * Remove annotation entries for tasks not in the active set.
 */
export function pruneAnnotations(activeTaskIds: Set<number>): void {
	const toRemove: string[] = [];
	for (let i = 0; i < localStorage.length; i++) {
		const key = localStorage.key(i);
		if (!key?.startsWith(STORAGE_PREFIX)) continue;
		const id = parseInt(key.slice(STORAGE_PREFIX.length), 10);
		if (!isNaN(id) && !activeTaskIds.has(id)) {
			toRemove.push(key);
		}
	}
	for (const key of toRemove) {
		localStorage.removeItem(key);
	}
}

// --- Internal helpers ---

/**
 * Find the exact text offset within fullText using prefix+suffix context for disambiguation.
 * Returns -1 if not found.
 */
function findWithContext(fullText: string, prefix: string, exact: string, suffix: string): number {
	if (!prefix && !suffix) return fullText.indexOf(exact);

	let searchFrom = 0;
	while (true) {
		const idx = fullText.indexOf(exact, searchFrom);
		if (idx === -1) return -1;

		// Check if prefix matches (allow partial — content may have been trimmed at start)
		const actualPrefix = fullText.slice(Math.max(0, idx - prefix.length), idx);
		const prefixOk = !prefix || actualPrefix.endsWith(prefix) || prefix.endsWith(actualPrefix);

		// Check if suffix matches
		const actualSuffix = fullText.slice(idx + exact.length, idx + exact.length + suffix.length);
		const suffixOk = !suffix || actualSuffix.startsWith(suffix) || suffix.startsWith(actualSuffix);

		if (prefixOk && suffixOk) return idx;
		searchFrom = idx + 1;
	}
}

/**
 * Compute the character offset of a Range's start within a container's textContent.
 */
function textOffsetOfRange(range: Range, container: HTMLElement): number {
	const walker = document.createTreeWalker(container, NodeFilter.SHOW_TEXT);
	let offset = 0;

	while (walker.nextNode()) {
		const node = walker.currentNode as Text;
		if (node === range.startContainer) {
			return offset + range.startOffset;
		}
		offset += node.length;
	}
	return -1;
}

/**
 * Build a DOM Range from a character offset and length within a container's text nodes.
 */
function rangeFromTextOffset(container: HTMLElement, offset: number, length: number): Range | null {
	const walker = document.createTreeWalker(container, NodeFilter.SHOW_TEXT);
	let accumulated = 0;
	let startNode: Text | null = null;
	let startOffset = 0;
	let endNode: Text | null = null;
	let endOffset = 0;

	const targetEnd = offset + length;

	while (walker.nextNode()) {
		const node = walker.currentNode as Text;
		const nodeEnd = accumulated + node.length;

		if (!startNode && nodeEnd > offset) {
			startNode = node;
			startOffset = offset - accumulated;
		}

		if (startNode && nodeEnd >= targetEnd) {
			endNode = node;
			endOffset = targetEnd - accumulated;
			break;
		}

		accumulated = nodeEnd;
	}

	if (!startNode || !endNode) return null;

	const range = document.createRange();
	range.setStart(startNode, startOffset);
	range.setEnd(endNode, endOffset);
	return range;
}
