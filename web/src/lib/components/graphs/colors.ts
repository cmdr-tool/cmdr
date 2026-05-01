// Hierarchical community coloring. Super-community drives the hue;
// tier-2 community within that super gets a subtle shade variation
// so you can still see fine-grained structure without losing the
// neighborhood signal. Designed for the dark theme.

// Super-palette: distinct hues, easy to scan at a glance. Cycles by
// modulo for codebases with more super-communities than entries.
export const superPalette = [
	'#7F77DD', // cmd-purple
	'#FAC775', // run-amber
	'#85D7B5', // mint
	'#F38BA8', // pink
	'#94A3F4', // periwinkle
	'#FF8A65', // peach
	'#80DEEA', // cyan
	'#B39DDB', // lavender
	'#A5D6A7', // sage
	'#CE93D8', // orchid
	'#90CAF9', // sky
	'#E5C07B' // gold
];

// Legacy flat palette — kept for callers that don't have a super-id.
export const palette = superPalette;

export function superCommunityColor(superId: number): string {
	const i = ((superId % superPalette.length) + superPalette.length) % superPalette.length;
	return superPalette[i];
}

// communityColor returns the hierarchical color for a node. When
// superId is provided, returns the super-color shifted by a small
// amount based on the tier-2 community (lightness variation), so
// nodes in the same neighborhood share a hue family with sub-cluster
// detail visible at close inspection. When superId is omitted, falls
// back to the legacy hue-by-community-id behavior.
export function communityColor(communityId: number, superId?: number): string {
	if (superId === undefined) {
		const i = ((communityId % palette.length) + palette.length) % palette.length;
		return palette[i];
	}
	const base = superCommunityColor(superId);
	// Shift lightness in HSL space within ±12% based on community-id parity
	// and modulo. Keeps the hue stable per super, varies value subtly.
	const shift = (((communityId * 37) % 25) - 12) / 100; // -0.12..+0.12
	return shiftLightness(base, shift);
}

// shiftLightness adjusts a hex color's HSL lightness by delta
// (clamped to [0,1]). Returns hex.
function shiftLightness(hex: string, delta: number): string {
	const { h, s, l } = hexToHsl(hex);
	const l2 = Math.max(0.1, Math.min(0.9, l + delta));
	return hslToHex(h, s, l2);
}

function hexToHsl(hex: string): { h: number; s: number; l: number } {
	const m = hex.replace('#', '').match(/^([0-9a-f]{2})([0-9a-f]{2})([0-9a-f]{2})$/i);
	if (!m) return { h: 0, s: 0, l: 0.5 };
	const r = parseInt(m[1], 16) / 255;
	const g = parseInt(m[2], 16) / 255;
	const b = parseInt(m[3], 16) / 255;
	const max = Math.max(r, g, b);
	const min = Math.min(r, g, b);
	const l = (max + min) / 2;
	let h = 0;
	let s = 0;
	if (max !== min) {
		const d = max - min;
		s = l > 0.5 ? d / (2 - max - min) : d / (max + min);
		switch (max) {
			case r:
				h = (g - b) / d + (g < b ? 6 : 0);
				break;
			case g:
				h = (b - r) / d + 2;
				break;
			case b:
				h = (r - g) / d + 4;
				break;
		}
		h /= 6;
	}
	return { h, s, l };
}

function hslToHex(h: number, s: number, l: number): string {
	let r: number, g: number, b: number;
	if (s === 0) {
		r = g = b = l;
	} else {
		const hue2rgb = (p: number, q: number, t: number) => {
			if (t < 0) t += 1;
			if (t > 1) t -= 1;
			if (t < 1 / 6) return p + (q - p) * 6 * t;
			if (t < 1 / 2) return q;
			if (t < 2 / 3) return p + (q - p) * (2 / 3 - t) * 6;
			return p;
		};
		const q = l < 0.5 ? l * (1 + s) : l + s - l * s;
		const p = 2 * l - q;
		r = hue2rgb(p, q, h + 1 / 3);
		g = hue2rgb(p, q, h);
		b = hue2rgb(p, q, h - 1 / 3);
	}
	const toHex = (v: number) =>
		Math.round(v * 255)
			.toString(16)
			.padStart(2, '0');
	return `#${toHex(r)}${toHex(g)}${toHex(b)}`;
}
