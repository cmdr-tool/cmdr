const BASE = '/api';

export interface DaemonStatus {
	pid: number;
	version: string;
	tasks: number;
}

export interface Task {
	name: string;
	description: string;
	schedule: string;
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
	const res = await fetch(`${BASE}${path}`, init);
	if (!res.ok) {
		throw new Error(`${res.status} ${res.statusText}`);
	}
	return res.json();
}

export function getStatus(): Promise<DaemonStatus> {
	return request('/status');
}

export function getTasks(): Promise<Task[]> {
	return request('/tasks');
}

export function runTask(name: string): Promise<{ output: string }> {
	return request(`/run?task=${encodeURIComponent(name)}`, { method: 'POST' });
}

// Tmux

export interface TmuxPane {
	index: number;
	cwd: string;
	command: string;
}

export interface TmuxWindow {
	index: number;
	name: string;
	active: boolean;
	panes: TmuxPane[];
}

export interface TmuxSession {
	name: string;
	attached: boolean;
	windows: TmuxWindow[];
}

export function getTmuxSessions(): Promise<TmuxSession[]> {
	return request('/tmux/sessions');
}

// Claude

export interface ClaudeSession {
	pid: number;
	sessionId: string;
	cwd: string;
	project: string;
	startedAt: number;
	uptime: string;
	status: 'working' | 'waiting' | 'idle' | 'unknown';
}

export function getClaudeSessions(): Promise<ClaudeSession[]> {
	return request('/claude/sessions');
}

export function createTmuxSession(dir: string): Promise<{ name: string }> {
	return request('/tmux/sessions/create', {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify({ dir })
	});
}

export function killTmuxSession(name: string): Promise<{ killed: string }> {
	return request('/tmux/sessions/kill', {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify({ name })
	});
}

export function switchTmuxSession(name: string): Promise<{ switched: string }> {
	return request('/tmux/sessions/switch', {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify({ name })
	});
}

// Git monitoring

export interface MonitoredRepo {
	id: number;
	name: string;
	path: string;
	remoteUrl: string;
	defaultBranch: string;
	lastSyncedAt: string | null;
	createdAt: string;
}

export interface DiscoveredRepo {
	name: string;
	path: string;
	remoteUrl: string;
	defaultBranch: string;
}

export interface GitCommit {
	id: number;
	sha: string;
	author: string;
	message: string;
	committedAt: string;
	url: string;
	seen: boolean;
	repoName: string;
	repoPath: string;
}

export function getRepos(): Promise<MonitoredRepo[]> {
	return request('/repos');
}

export function discoverRepos(): Promise<DiscoveredRepo[]> {
	return request('/repos/discover');
}

export function addRepo(repo: DiscoveredRepo): Promise<{ id: number; name: string }> {
	return request('/repos/add', {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify(repo)
	});
}

export function removeRepo(id: number): Promise<{ removed: number }> {
	return request('/repos/remove', {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify({ id })
	});
}

export function getCommits(opts?: { repo?: string; unseen?: boolean; limit?: number }): Promise<GitCommit[]> {
	const params = new URLSearchParams();
	if (opts?.repo) params.set('repo', opts.repo);
	if (opts?.unseen) params.set('unseen', 'true');
	if (opts?.limit) params.set('limit', String(opts.limit));
	const qs = params.toString();
	return request(`/commits${qs ? '?' + qs : ''}`);
}

export interface CommitFile {
	filename: string;
	status: 'added' | 'modified' | 'removed' | 'renamed';
	additions: number;
	deletions: number;
}

export function getCommitFiles(repoPath: string, sha: string): Promise<CommitFile[]> {
	return request(`/commits/files?repo=${encodeURIComponent(repoPath)}&sha=${encodeURIComponent(sha)}`);
}

export function getCommitDiff(repoPath: string, sha: string): Promise<{ diff: string; format: 'delta' | 'unified' }> {
	return request(`/commits/diff?repo=${encodeURIComponent(repoPath)}&sha=${encodeURIComponent(sha)}`);
}

export function markCommitsSeen(ids: number[]): Promise<{ marked: number }> {
	return request('/commits/seen', {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify({ ids })
	});
}

export function syncRepos(): Promise<{ status: string }> {
	return request('/sync', { method: 'POST' });
}
