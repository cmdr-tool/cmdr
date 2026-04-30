// Distinct community colors with similar saturation/lightness for the
// dark theme. Cycles by modulo for graphs with more communities than
// palette entries — color is a visual distinguishing aid, not an
// identity, so collisions past index 14 are acceptable.
export const palette = [
	'#7F77DD', // cmd-purple
	'#FAC775', // run-amber
	'#85D7B5', // mint
	'#F38BA8', // pink
	'#94A3F4', // periwinkle
	'#E5C07B', // gold
	'#FF8A65', // peach
	'#80DEEA', // cyan
	'#B39DDB', // lavender
	'#A5D6A7', // sage
	'#FFAB91', // coral
	'#9FA8DA', // dusty blue
	'#CE93D8', // orchid
	'#BCAAA4', // tan
	'#90CAF9' // sky
];

export function communityColor(c: number): string {
	return palette[((c % palette.length) + palette.length) % palette.length];
}
