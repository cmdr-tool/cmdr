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
	active: boolean;
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
	tmuxTarget?: string;
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

export function focusTmuxSession(name: string): Promise<{ focused: string }> {
	return request('/tmux/sessions/focus', {
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

export function openFolder(path: string): Promise<{ opened: string }> {
	return request('/open', {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify({ path })
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
	flagged: boolean;
	reviewCount: number;
	repoName: string;
	repoPath: string;
	local: boolean;
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

export function getCommitDiff(repoPath: string, sha: string): Promise<{ diff: string; format: 'delta' | 'unified'; files: string[] }> {
	return request(`/commits/diff?repo=${encodeURIComponent(repoPath)}&sha=${encodeURIComponent(sha)}`);
}

export function markCommitsSeen(ids: number[]): Promise<{ marked: number }> {
	return request('/commits/seen', {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify({ ids })
	});
}

export function toggleCommitFlag(id: number, flagged: boolean): Promise<{ flagged: boolean }> {
	return request('/commits/flag', {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify({ id, flagged })
	});
}

export function syncRepos(): Promise<{ status: string }> {
	return request('/repos/sync', { method: 'POST' });
}

// Analytics

export interface ActivityBucket {
	bucket: number;
	samples: number;
	nvim: number;
	claude: number;
	other: number;
	inactive: number;
	away: number;
	claudeTotal: number;
	claudeWorking: number;
	claudeWaiting: number;
	claudeIdle: number;
	claudeUnknown: number;
}

export interface ActivityDay {
	date: string;
	currentBucket?: number;
	buckets: ActivityBucket[];
}

export interface ActivityResponse {
	resolution: string;
	samplesPerBar: number;
	today: ActivityDay;
	yesterday: ActivityDay;
}

export function getActivity(resolution: '5s' | '1m' | '5m' = '1m'): Promise<ActivityResponse> {
	return request(`/analytics/activity?resolution=${resolution}`);
}

// Brew

export interface BrewFormula {
	name: string;
	installed_versions: string[];
	current_version: string;
	pinned: boolean;
}

export interface BrewOutdated {
	formulae: BrewFormula[];
	casks: BrewFormula[];
}

export function getBrewOutdated(): Promise<BrewOutdated> {
	return request('/brew/outdated');
}

export function brewUpgrade(formula?: string): Promise<{ status: string; output: string }> {
	return request('/brew/upgrade', {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify({ formula: formula ?? '' })
	});
}

// Review

export interface ReviewComment {
	id: number;
	repoPath: string;
	sha: string;
	lineStart: number;
	lineEnd: number;
	comment: string;
	createdAt: string;
}

export function getReviewComments(repoPath: string, sha: string): Promise<ReviewComment[]> {
	return request(`/review/comments?repo=${encodeURIComponent(repoPath)}&sha=${encodeURIComponent(sha)}`);
}

export function saveReviewComment(body: { repoPath: string; sha: string; lineStart: number; lineEnd: number; comment: string }): Promise<{ id: number }> {
	return request('/review/comments', {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify(body)
	});
}

export function deleteReviewComment(id: number): Promise<{ status: string }> {
	return request('/review/comments/delete', {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify({ id })
	});
}

export function submitReview(repoPath: string, sha: string): Promise<{ id: number; status: string }> {
	return request('/review/submit', {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify({ repoPath, sha })
	});
}

// Claude Tasks

export interface ClaudeTask {
	id: number;
	type: string;
	status: 'pending' | 'running' | 'completed' | 'failed' | 'refactoring' | 'resolved';
	repoPath: string;
	commitSha: string;
	title?: string;
	prUrl?: string;
	errorMsg?: string;
	createdAt: string;
	startedAt: string | null;
	completedAt: string | null;
	refactored: boolean;
}

export interface ClaudeTaskResult {
	result: string;
	status: string;
	errorMsg: string;
}

export function getClaudeTasks(): Promise<ClaudeTask[]> {
	return request('/claude/tasks');
}

export function getClaudeTaskResult(id: number): Promise<ClaudeTaskResult> {
	return request(`/claude/tasks/result?id=${id}`);
}

export function dismissClaudeTask(id: number): Promise<{ dismissed: number }> {
	return request('/claude/tasks/dismiss', {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify({ id })
	});
}

export function updateClaudeTaskResult(id: number, result: string): Promise<{ status: string }> {
	return request('/claude/tasks/update', {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify({ id, result })
	});
}

export function startRefactor(taskId: number): Promise<{ target: string; session: string; window: string }> {
	return request('/review/refactor', {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify({ taskId })
	});
}

export function dismissAllClaudeTasks(): Promise<{ dismissed: number }> {
	return request('/claude/tasks/dismiss', {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify({ all: 'completed' })
	});
}

export function openInEditor(repoPath: string, file: string, line: number): Promise<{ status: string }> {
	return request('/editor/open', {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify({ repoPath, file, line })
	});
}

export function pullRepo(repoPath: string): Promise<{ status: string; message: string }> {
	return request('/repos/pull', {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify({ repoPath })
	});
}
