let mermaidPromise: Promise<(typeof import('mermaid'))['default']> | null = null;
let mermaidInitialized = false;

async function getMermaid() {
	mermaidPromise ??= import('mermaid').then((mod) => mod.default);
	const mermaid = await mermaidPromise;
	if (!mermaidInitialized) {
		mermaid.initialize({
			startOnLoad: false,
			theme: 'dark',
			themeVariables: {
				primaryColor: '#332a1f',
				primaryTextColor: '#c4b5a2',
				primaryBorderColor: '#4a3d2e',
				lineColor: '#897562',
				secondaryColor: '#26215c',
				tertiaryColor: '#1a1510',
				fontFamily: 'Space Grotesk, sans-serif',
			},
		});
		mermaidInitialized = true;
	}
	return mermaid;
}

/** Render any mermaid placeholder divs within a container element. */
export async function renderMermaidBlocks(container: HTMLElement): Promise<void> {
	const blocks = container.querySelectorAll<HTMLElement>('.mermaid-block');
	if (blocks.length === 0) return;

	const mermaid = await getMermaid();
	for (const block of blocks) {
		const source = decodeURIComponent(block.dataset.mermaid ?? '');
		if (!source) continue;
		const id = `mermaid-${crypto.randomUUID().slice(0, 8)}`;
		try {
			const { svg } = await mermaid.render(id, source);
			block.innerHTML = svg;
			block.classList.add('mermaid-rendered');
		} catch {
			// Leave the raw text visible on parse errors
		}
	}
}
