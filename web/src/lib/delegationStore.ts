import { writable, derived } from 'svelte/store';
import { getDelegationSummary, type DelegationSummary } from '$lib/api';
import { events, connection } from '$lib/events';

export const delegationSummaries = writable<DelegationSummary[]>([]);

export const hasActiveDelegations = derived(delegationSummaries, (s) =>
	s.length > 0
);

export async function fetchSummaries() {
	try {
		const s = await getDelegationSummary();
		delegationSummaries.set(s);
	} catch { /* silent */ }
}

let initialized = false;

export function initDelegationStore() {
	if (initialized) return;
	initialized = true;

	events.on('delegation:update', () => {
		fetchSummaries();
	});

	connection.subscribe((c) => {
		if (c.connected) fetchSummaries();
	});
}
