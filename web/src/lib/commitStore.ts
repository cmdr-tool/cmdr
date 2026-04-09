import { writable, derived, get } from 'svelte/store';
import {
	getCommits,
	markCommitsSeen,
	toggleCommitFlag as apiToggleFlag,
	type GitCommit
} from '$lib/api';
import { events, connection } from '$lib/events';
import { playSound, SFX } from '$lib/sounds';

// --- Core state ---

export const commits = writable<GitCommit[]>([]);
export const commitsLoaded = writable(false);

let knownLatestId = 0;

// --- Derived views ---

export const unseenCount = derived(commits, (c) => c.filter((x) => !x.seen).length);

// --- Actions ---

export async function fetchCommits() {
	try {
		const c = await getCommits();
		knownLatestId = Math.max(0, ...c.map((x) => x.id));
		commits.set(c);
	} catch { /* silent */ }
	commitsLoaded.set(true);
}

export async function markSeen(ids: number[]) {
	if (ids.length === 0) return;
	await markCommitsSeen(ids);
	commits.update((c) => c.map((x) => ids.includes(x.id) ? { ...x, seen: true } : x));
}

export function toggleFlag(id: number) {
	commits.update((c) =>
		c.map((x) => x.id === id ? { ...x, flagged: !x.flagged } : x)
	);
	const commit = get(commits).find((x) => x.id === id);
	if (commit) apiToggleFlag(id, commit.flagged);
}

export function updateCommit(id: number, patch: Partial<GitCommit>) {
	commits.update((c) => c.map((x) => x.id === id ? { ...x, ...patch } : x));
}

// --- SSE sync ---

let initialized = false;

export function initCommitStore() {
	if (initialized) return;
	initialized = true;

	fetchCommits();

	events.on('commits:sync', async () => {
		await refreshWithNotification();
	});

	events.on('commits:watermark', (data: { latestId: number }) => {
		if (get(commitsLoaded) && data.latestId > knownLatestId) {
			refreshWithNotification();
		}
	});

	connection.subscribe((c) => {
		if (c.connected && get(commitsLoaded)) fetchCommits();
	});
}

let refreshing = false;

async function refreshWithNotification() {
	if (refreshing) return;
	refreshing = true;

	const prev = get(commits);
	const loaded = get(commitsLoaded);

	const c = await getCommits();
	knownLatestId = Math.max(knownLatestId, ...c.map((x) => x.id));
	commits.set(c);
	refreshing = false;

	const newUnseen = c.filter((x) => !x.seen && !prev.find((p) => p.id === x.id));
	if (loaded && newUnseen.length > 0) {
		playSound(SFX.newCommits, 0.5);

		if (!document.hasFocus() && (window as any).webkit?.messageHandlers?.notify) {
			const repos = [...new Set(newUnseen.map((x) => x.repoName))];
			(window as any).webkit.messageHandlers.notify.postMessage({
				title: `${newUnseen.length} new commit${newUnseen.length > 1 ? 's' : ''}`,
				body: repos.join(', ')
			});
		}
	}
}
