/**
 * Sound effects using Web Audio API for low-latency, overlapping playback.
 * Audio is decoded once into a buffer, then each play() creates a lightweight
 * buffer source — no DOM elements, no decoding delay.
 */

let ctx: AudioContext | null = null;
const buffers = new Map<string, AudioBuffer>();
const rawData = new Map<string, ArrayBuffer>();
const fetching = new Map<string, Promise<ArrayBuffer>>();

function getContext(): AudioContext {
	if (!ctx || ctx.state === 'closed') {
		ctx = new AudioContext();
		buffers.clear(); // buffers are tied to the old context
	}
	return ctx;
}

async function ensureResumed(): Promise<AudioContext> {
	const audioCtx = getContext();
	if (audioCtx.state === 'suspended') {
		await audioCtx.resume();
	}
	return audioCtx;
}

// After macOS sleep/wake, WKWebView silently kills the audio session —
// resume() resolves but produces no output. Since this is a native app,
// visibilitychange only fires on app show/hide, so always rebuild.
let hiddenAt = 0;
if (typeof document !== 'undefined') {
	document.addEventListener('visibilitychange', () => {
		if (document.visibilityState === 'hidden') {
			hiddenAt = Date.now();
		} else if (ctx && Date.now() - hiddenAt > 3_000) {
			ctx.close().catch(() => {});
			ctx = null;
			buffers.clear();
			// Re-decode all cached raw data on a fresh context
			for (const src of rawData.keys()) {
				getBuffer(src).catch(() => {});
			}
		}
	});
}

// Prefetch raw audio data without creating AudioContext
async function fetchRaw(src: string): Promise<ArrayBuffer> {
	const cached = rawData.get(src);
	if (cached) return cached;

	const inflight = fetching.get(src);
	if (inflight) return inflight;

	const promise = fetch(src)
		.then((r) => r.arrayBuffer())
		.then((data) => {
			rawData.set(src, data);
			fetching.delete(src);
			return data;
		});

	fetching.set(src, promise);
	return promise;
}

async function getBuffer(src: string): Promise<AudioBuffer> {
	const cached = buffers.get(src);
	if (cached) return cached;

	const data = await fetchRaw(src);
	const audioCtx = getContext();
	const buffer = await audioCtx.decodeAudioData(data.slice(0));
	buffers.set(src, buffer);
	return buffer;
}

export function playSound(src: string, volume = 0.5) {
	const audioCtx = getContext();

	// Fast path: context is running and buffer is decoded — fire synchronously
	if (audioCtx.state === 'running') {
		const cached = buffers.get(src);
		if (cached) {
			fire(audioCtx, cached, volume);
			return;
		}
	}

	// Slow path: need to resume context or decode buffer
	ensureResumed().then((ctx) =>
		getBuffer(src).then((buf) => fire(ctx, buf, volume))
	);
}

function fire(audioCtx: AudioContext, buffer: AudioBuffer, volume: number) {
	const gain = audioCtx.createGain();
	gain.gain.value = volume;
	gain.connect(audioCtx.destination);

	const source = audioCtx.createBufferSource();
	source.buffer = buffer;
	source.connect(gain);
	source.start();
}

// Preload and eagerly decode audio data so first playback is instant
export function preload(...srcs: string[]) {
	srcs.forEach((src) => getBuffer(src).catch(() => {}));
}

export const SFX = {
	newCommits: '/nba-draft-sound.mp3',
	hover: '/sfx-hover.mp3',
	click: '/sfx-click.mp3',
	dispatch: '/sfx-magic.mp3'
} as const;
