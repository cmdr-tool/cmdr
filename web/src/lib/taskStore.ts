import { writable, derived } from 'svelte/store';
import {
	getClaudeTasks,
	dismissClaudeTask,
	dismissAllClaudeTasks,
	createDirective,
	type ClaudeTask
} from '$lib/api';
import { events, connection } from '$lib/events';

// --- Core state ---

export const tasks = writable<ClaudeTask[]>([]);
export const loaded = writable(false);

// --- Derived views ---

export const visibleTasks = derived(tasks, (t) => t
);
export const activeCount = derived(visibleTasks, (t) =>
	t.filter((task) => task.status === 'running' || task.status === 'pending' || task.status === 'refactoring' || task.status === 'implementing').length
);
export const dismissableCount = derived(visibleTasks, (t) =>
	t.filter((task) => task.status === 'done' || task.status === 'failed').length
);

// --- Actions ---

export async function fetchTasks() {
	try {
		const t = await getClaudeTasks();
		tasks.set(t);
	} catch { /* silent */ }
	loaded.set(true);
}

export async function dismiss(id: number) {
	tasks.update((t) => t.filter((task) => task.id !== id));
	await dismissClaudeTask(id);
}

export async function clearAllCompleted() {
	tasks.update((t) => t.filter((task) => task.status !== 'done' && task.status !== 'failed'));
	await dismissAllClaudeTasks();
}

export async function create(repoPath: string, content: string = '') {
	const res = await createDirective(repoPath, content);
	return res;
}

// --- SSE sync ---

let initialized = false;

export function initTaskStore() {
	if (initialized) return;
	initialized = true;

	fetchTasks();

	events.on('claude:task', (evt) => {
		if ((evt.status as string) === 'dismissed') {
			if (evt.id) {
				tasks.update((t) => t.filter((task) => task.id !== evt.id));
			} else {
				fetchTasks();
			}
			return;
		}

		tasks.update((t) => {
			const idx = t.findIndex((task) => task.id === evt.id);
			if (idx >= 0) {
				const updated = [...t];
				updated[idx] = { ...updated[idx], ...evt };
				return updated;
			}
			if (evt.status === 'draft' || evt.status === 'pending' || evt.status === 'running') {
				fetchTasks();
			}
			return t;
		});
	});

	connection.subscribe((c) => {
		if (c.connected) fetchTasks();
	});
}
