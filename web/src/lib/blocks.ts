/**
 * Block-based editor model for the directive composer.
 *
 * The underlying data is always a markdown string. Blocks are parsed from
 * markdown on load, edited individually, and serialized back on save.
 *
 * Block types:
 *   - text:    Free-form markdown content
 *   - coderef: @file:L10-L25 or @file (code reference)
 *   - image:   ![caption](path) or ![caption|strokes:...](path)
 */

export interface TextBlock {
	type: 'text';
	id: string;
	content: string;
}

export interface CodeRefBlock {
	type: 'coderef';
	id: string;
	ref: string; // e.g. "src/lib/api.ts:L10-L25" (without the @)
}

export interface ImageBlock {
	type: 'image';
	id: string;
	path: string;
	caption: string;
	meta: string; // future: annotation data (strokes)
}

export type Block = TextBlock | CodeRefBlock | ImageBlock;

// --- Stroke serialization ---
// Stored in ImageBlock.meta as: "strokes:<base64 JSON>"
// The JSON is an array of { points: number[][], color: string, size: number }

export interface StrokeData {
	points: number[][];
	color: string;
	size: number;
}

export function parseStrokes(meta: string): StrokeData[] {
	if (!meta || !meta.startsWith('strokes:')) return [];
	try {
		const json = atob(meta.slice('strokes:'.length));
		return JSON.parse(json);
	} catch {
		return [];
	}
}

export function serializeStrokes(strokes: StrokeData[]): string {
	if (strokes.length === 0) return '';
	return 'strokes:' + btoa(JSON.stringify(strokes));
}

function uid(): string {
	return crypto.randomUUID();
}

// --- Parsing ---

const IMAGE_RE = /^!\[([^\]]*)\]\(([^)]+)\)$/;
const CODEREF_RE = /^@([\w./_\-\\][\w./_\-\\:L0-9]*)$/;

/**
 * Check if a string contains lines that would parse as non-text blocks
 * (code refs or images). Used to detect when pasted text needs re-parsing.
 */
export function containsBlockSyntax(text: string): boolean {
	return text.split('\n').some(line => {
		const trimmed = line.trim();
		return CODEREF_RE.test(trimmed) || IMAGE_RE.test(trimmed);
	});
}

/**
 * Parse a markdown string into an array of blocks.
 *
 * Rules:
 * - Lines matching `![caption](path)` become ImageBlock
 * - Lines matching `@path/to/file` or `@path:L10-L25` become CodeRefBlock
 * - Consecutive non-special lines are grouped into a single TextBlock
 * - Blank lines between groups are consumed as separators
 */
export function parseBlocks(markdown: string): Block[] {
	if (!markdown.trim()) {
		return [{ type: 'text', id: uid(), content: '' }];
	}

	const blocks: Block[] = [];
	const lines = markdown.split('\n');
	let textBuffer: string[] = [];

	function flushText() {
		if (textBuffer.length > 0) {
			const content = textBuffer.join('\n').trim();
			if (content) {
				blocks.push({ type: 'text', id: uid(), content });
			}
			textBuffer = [];
		}
	}

	for (let i = 0; i < lines.length; i++) {
		const line = lines[i];
		const trimmed = line.trim();

		// Skip blank lines between blocks
		if (trimmed === '' && textBuffer.length > 0 && textBuffer.every(l => l.trim() === '')) {
			continue;
		}

		// Image block
		const imgMatch = trimmed.match(IMAGE_RE);
		if (imgMatch) {
			flushText();
			const rawCaption = imgMatch[1];
			const path = imgMatch[2];
			let caption = rawCaption;
			let meta = '';
			// Extract meta from caption: "alt|strokes:data"
			const metaIdx = rawCaption.indexOf('|');
			if (metaIdx >= 0) {
				caption = rawCaption.slice(0, metaIdx);
				meta = rawCaption.slice(metaIdx + 1);
			}
			blocks.push({ type: 'image', id: uid(), path, caption, meta });
			continue;
		}

		// Code reference block
		const refMatch = trimmed.match(CODEREF_RE);
		if (refMatch) {
			flushText();
			blocks.push({ type: 'coderef', id: uid(), ref: refMatch[1] });
			continue;
		}

		// Regular text line
		textBuffer.push(line);
	}

	flushText();

	if (blocks.length === 0) {
		blocks.push({ type: 'text', id: uid(), content: '' });
	}

	return blocks;
}

// --- Serialization ---

/**
 * Serialize an array of blocks back to markdown.
 */
export function serializeBlocks(blocks: Block[]): string {
	const parts: string[] = [];

	for (const block of blocks) {
		switch (block.type) {
			case 'text':
				if (block.content.trim()) {
					parts.push(block.content);
				}
				break;
			case 'coderef':
				if (block.ref.trim()) {
					parts.push(`@${block.ref}`);
				}
				break;
			case 'image': {
				if (!block.path) break; // skip placeholder blocks
				const caption = block.meta
					? `${block.caption}|${block.meta}`
					: block.caption;
				parts.push(`![${caption}](${block.path})`);
				break;
			}
		}
	}

	return parts.join('\n\n');
}

// --- Factories ---

export function createTextBlock(content = ''): TextBlock {
	return { type: 'text', id: uid(), content };
}

export function createCodeRefBlock(ref = ''): CodeRefBlock {
	return { type: 'coderef', id: uid(), ref };
}

export function createImageBlock(path: string, caption = ''): ImageBlock {
	return { type: 'image', id: uid(), path, caption, meta: '' };
}

/**
 * Ensure the block list ends with a text block for continued typing.
 */
export function ensureTrailingTextBlock(blocks: Block[]): Block[] {
	if (blocks.length === 0 || blocks[blocks.length - 1].type !== 'text') {
		return [...blocks, createTextBlock()];
	}
	return blocks;
}
