import { writable, derived } from 'svelte/store';
import {
	getAgentTasks,
	dismissAgentTask,
	dismissAllAgentTasks,
	createDirective,
	isTerminalTask,
	type AgentTask
} from '$lib/api';
import { events, connection } from '$lib/events';

// --- Core state ---

export const tasks = writable<AgentTask[]>([]);
export const loaded = writable(false);

// --- Derived views ---

export const visibleTasks = derived(tasks, (t) =>
	t.filter((task) => task.type !== 'delegation')
);
export const activeCount = derived(visibleTasks, (t) =>
	t.filter((task) => task.status === 'running' || task.status === 'pending').length
);
export const dismissableCount = derived(visibleTasks, (t) =>
	t.filter(isTerminalTask).length
);

// --- Actions ---

export async function fetchTasks() {
	try {
		const t = await getAgentTasks();
		tasks.set(t);
	} catch { /* silent */ }
	loaded.set(true);
}

export async function dismiss(id: number) {
	tasks.update((t) => t.filter((task) => task.id !== id));
	await dismissAgentTask(id);
}

export async function clearAllCompleted() {
	tasks.update((t) => t.filter((task) => task.type === 'delegation' || !isTerminalTask(task)));
	await dismissAllAgentTasks();
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

	events.on('agent:task', (evt) => {
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
