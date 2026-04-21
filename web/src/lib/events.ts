import { writable } from 'svelte/store';
import type { DaemonStatus, TmuxSession, AgentSession, ActivityResponse, AgentTask, BrewOutdated } from './api';

type EventMap = {
	status: DaemonStatus;
	'tmux:sessions': TmuxSession[];
	'agent:sessions': AgentSession[];
	'analytics:activity': ActivityResponse;
	'agent:task': Partial<AgentTask> & { id: number; status: string };
	'agent:stream': { id: number; type: 'text' | 'tool' | 'done' | 'error'; text?: string; tool?: string; detail?: string; error?: string };
	'brew:outdated': BrewOutdated;
	'commits:sync': boolean;
	'commits:watermark': { latestId: number };
	'delegation:update': { squad: string; taskId: number; status: string };
	'agentic:run': { id: number; status: string; last_run_at?: string };
	'agentic:update': { action: string; id: number };
};

type EventHandler<K extends keyof EventMap> = (data: EventMap[K]) => void;

// If no events arrive for this long, assume connection is stale
const HEARTBEAT_TIMEOUT = 15_000;
const RECONNECT_DELAY = 3_000;

export const connection = writable({ connected: false, reconnecting: false });

class EventClient {
	private source: EventSource | null = null;
	private handlers = new Map<string, Set<(data: any) => void>>();
	private reconnectTimer: ReturnType<typeof setTimeout> | null = null;
	private heartbeatTimer: ReturnType<typeof setTimeout> | null = null;

	connect() {
		if (this.source) {
			this.source.close();
		}
		this.clearTimers();

		this.source = new EventSource('/api/events');

		// Any incoming message resets heartbeat
		this.source.onmessage = () => this.resetHeartbeat();

		// Attach all registered event listeners immediately
		for (const type of this.handlers.keys()) {
			this.attachListener(type);
		}

		this.source.addEventListener('open', () => {
			connection.set({ connected: true, reconnecting: false });
			this.resetHeartbeat();
		});

		this.source.addEventListener('error', () => {
			connection.set({ connected: false, reconnecting: true });
			this.clearTimers();

			// EventSource auto-reconnect can silently fail — schedule manual reconnect
			this.reconnectTimer = setTimeout(() => {
				if (this.source?.readyState === EventSource.CLOSED) {
					console.warn('[cmdr] SSE connection closed — reconnecting');
					this.connect();
				}
			}, RECONNECT_DELAY);
		});
	}

	private resetHeartbeat() {
		if (this.heartbeatTimer) clearTimeout(this.heartbeatTimer);
		this.heartbeatTimer = setTimeout(() => {
			console.warn('[cmdr] SSE heartbeat timeout — reconnecting');
			this.connect();
		}, HEARTBEAT_TIMEOUT);
	}

	private clearTimers() {
		if (this.reconnectTimer) {
			clearTimeout(this.reconnectTimer);
			this.reconnectTimer = null;
		}
		if (this.heartbeatTimer) {
			clearTimeout(this.heartbeatTimer);
			this.heartbeatTimer = null;
		}
	}

	private attachListener(type: string) {
		if (!this.source) return;
		this.source.addEventListener(type, (e: MessageEvent) => {
			this.resetHeartbeat();
			const handlers = this.handlers.get(type);
			if (!handlers) return;
			try {
				const data = JSON.parse(e.data);
				for (const handler of handlers) {
					handler(data);
				}
			} catch {
				// ignore parse errors
			}
		});
	}

	on<K extends keyof EventMap>(type: K, handler: EventHandler<K>): () => void {
		if (!this.handlers.has(type)) {
			this.handlers.set(type, new Set());
		}
		this.handlers.get(type)!.add(handler);

		if (!this.source) {
			this.connect();
		} else {
			// Attach listener whether connecting or already open
			this.attachListener(type);
		}

		return () => {
			const set = this.handlers.get(type);
			if (set) {
				set.delete(handler);
			}
		};
	}

	disconnect() {
		if (this.source) {
			this.source.close();
			this.source = null;
		}
		this.clearTimers();
		this.handlers.clear();
		connection.set({ connected: false, reconnecting: false });
	}
}

export const events = new EventClient();

if (import.meta.hot) {
	import.meta.hot.dispose(() => {
		events.disconnect();
	});
}
