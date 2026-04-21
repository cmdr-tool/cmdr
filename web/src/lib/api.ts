const BASE = '/api';

export interface Capabilities {
	askSkill: boolean;
}

export interface DaemonStatus {
	pid: number;
	version: string;
	tasks: number;
	user: string;
	capabilities: Capabilities;
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

// Agentic tasks

export interface AgenticTask {
	id: number;
	name: string;
	prompt: string;
	schedule: string;
	enabled: boolean;
	working_dir: string;
	last_run_at: string | null;
	last_result: string;
	last_status: string;
	created_at: string;
}

export function getAgenticTasks(): Promise<AgenticTask[]> {
	return request('/agentic-tasks');
}

export function createAgenticTask(task: { name: string; prompt: string; schedule: string; enabled: boolean; working_dir: string }): Promise<{ id: number; name: string }> {
	return request('/agentic-tasks/create', {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify(task),
	});
}

export function updateAgenticTask(task: { id: number; name: string; prompt: string; schedule: string; enabled: boolean; working_dir: string }): Promise<{ status: string }> {
	return request('/agentic-tasks/update', {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify(task),
	});
}

export function deleteAgenticTask(id: number): Promise<{ status: string }> {
	return request('/agentic-tasks/delete', {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify({ id }),
	});
}

export function runAgenticTask(id: number): Promise<{ status: string }> {
	return request('/agentic-tasks/run', {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify({ id }),
	});
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

// Agent

export interface AgentSession {
	pid: number;
	sessionId: string;
	cwd: string;
	project: string;
	startedAt: number;
	uptime: string;
	status: 'working' | 'waiting' | 'idle' | 'unknown';
	tmuxTarget?: string;
}

export function getAgentSessions(): Promise<AgentSession[]> {
	return request('/agent/sessions');
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
	squad: string;
	squadAlias: string;
	monitor: boolean;
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

export function assignRepoSquad(repoId: number, squad: string, alias: string): Promise<{ status: string }> {
	return request('/repos/squad', {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify({ repoId, squad, alias })
	});
}

export function updateRepoMonitor(repoId: number, monitor: boolean): Promise<{ status: string }> {
	return request('/repos/monitor', {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify({ repoId, monitor })
	});
}

// Squads

export interface Squad {
	name: string;
	createdAt: string;
	repos: SquadMember[];
}

export interface SquadMember {
	id: number;
	name: string;
	path: string;
	alias: string;
}

export function getSquads(): Promise<Squad[]> {
	return request('/squads');
}

export function createSquad(name: string): Promise<{ name: string }> {
	return request('/squads/create', {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify({ name })
	});
}

export function deleteSquad(name: string): Promise<{ deleted: string }> {
	return request('/squads/delete', {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify({ name })
	});
}

// Delegations

export interface DelegationSummary {
	squad: string;
	activeCount: number;
	totalCount: number;
	members: string[];
	latestAt: string;
	latestTitle: string;
}

export interface Delegation {
	id: number;
	status: string;
	squad: string;
	delegationFrom: string;
	delegationTo: string;
	title: string;
	summary: string;
	branch: string;
	repoPath: string;
	result: string;
	createdAt: string;
	completedAt: string;
}

export function getDelegationSummary(): Promise<DelegationSummary[]> {
	return request('/squads/delegation-summary');
}

export function getDelegations(squad: string): Promise<Delegation[]> {
	return request(`/squads/delegations?squad=${encodeURIComponent(squad)}`);
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
	return request('/review/comments/save', {
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

// Agent Tasks

export interface AgentTask {
	id: number;
	type: string;
	status: 'draft' | 'pending' | 'running' | 'completed' | 'failed' | 'resolved';
	repoPath: string;
	commitSha: string;
	title?: string;
	prUrl?: string;
	intent?: string;
	errorMsg?: string;
	createdAt: string;
	startedAt: string | null;
	completedAt: string | null;
	parentId?: number;
	headless?: boolean;
}

// A task is "terminal" when its lifecycle is fully done and there's nothing
// worth referencing: failed, artifact consumed (review→implementation),
// or generic directives. Ask and analysis tasks remain non-terminal even
// when completed because their results are reference material.
export function isTerminalTask(task: AgentTask): boolean {
	return task.status === 'failed' || task.status === 'completed';
}

export interface AgentTaskResult {
	result: string;
	status: string;
	errorMsg: string;
	intent?: string;
}

// Ask (vault Q&A)

export function askAgent(question: string): Promise<{ id: number; status: string }> {
	return request('/ask', {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify({ question })
	});
}

export function continueAsk(id: number): Promise<{ target: string }> {
	return request('/ask/continue', {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify({ id })
	});
}

export function getAgentTasks(): Promise<AgentTask[]> {
	return request('/agent/tasks');
}

export function getAgentTaskResult(id: number): Promise<AgentTaskResult> {
	return request(`/agent/tasks/result?id=${id}`);
}

export function dismissAgentTask(id: number): Promise<{ dismissed: number }> {
	return request('/agent/tasks/dismiss', {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify({ id })
	});
}

export function updateAgentTaskResult(id: number, result: string): Promise<{ status: string }> {
	return request('/agent/tasks/update', {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify({ id, result })
	});
}

export function dismissAllAgentTasks(): Promise<{ dismissed: number }> {
	return request('/agent/tasks/dismiss', {
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

export function pushRepo(repoPath: string): Promise<{ status: string; message: string }> {
	return request('/repos/push', {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify({ repoPath })
	});
}

export function pullRepo(repoPath: string): Promise<{ status: string; message: string }> {
	return request('/repos/pull', {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify({ repoPath })
	});
}

// --- Directives (draft claude tasks) ---

export function createDirective(repoPath: string, content: string = ''): Promise<{ id: number; status: string }> {
	return request('/directives/create', {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify({ repoPath, content })
	});
}

export function saveDirective(id: number, repoPath: string, content: string, intent?: string): Promise<{ status: string }> {
	return request('/directives/save', {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify({ id, repoPath, content, intent })
	});
}

export async function submitDirective(id: number, intent?: string): Promise<{ status: string; target: string; session: string }> {
	const res = await fetch(`${BASE}/directives/submit`, {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify({ id, intent })
	});
	const data = await res.json();
	if (!res.ok) {
		const err = new Error(data.error || `${res.status} ${res.statusText}`) as Error & { unpushed?: number };
		if (data.unpushed) err.unpushed = data.unpushed;
		throw err;
	}
	return data;
}

export async function spawnTask(parentId: number, intent?: string, options?: { commitADR?: boolean }): Promise<{ id: number; target: string; session: string }> {
	const res = await fetch(`${BASE}/agent/tasks/spawn`, {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify({ parentId, intent, commitADR: options?.commitADR })
	});
	const data = await res.json();
	if (!res.ok) {
		const err = new Error(data.error || `${res.status} ${res.statusText}`) as Error & { unpushed?: number };
		if (data.unpushed) err.unpushed = data.unpushed;
		throw err;
	}
	return data;
}

export function cancelTask(id: number): Promise<{ status: string }> {
	return request('/agent/tasks/cancel', {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify({ id })
	});
}

export interface DirectiveIntent {
	id: string;
	name: string;
	producesPR: boolean;
}

export function getDirectiveIntents(): Promise<DirectiveIntent[]> {
	return request('/directives/intents');
}

// --- Code + Images ---

export interface CodeSnippet {
	file: string;
	start: number;
	end: number;
	totalLines: number;
	lines: string[];
}

export function getCodeSnippet(repo: string, file: string, start?: number, end?: number): Promise<CodeSnippet> {
	const params = new URLSearchParams({ repo, file });
	if (start) params.set('start', String(start));
	if (end) params.set('end', String(end));
	return request(`/code/snippet?${params}`);
}

export function getCodeFiles(repo: string, q?: string): Promise<string[]> {
	const params = new URLSearchParams({ repo });
	if (q) params.set('q', q);
	return request(`/code/files?${params}`);
}

export async function uploadImage(blob: Blob): Promise<{ path: string; url: string }> {
	const form = new FormData();
	form.append('image', blob);
	const res = await fetch('/api/images/upload', { method: 'POST', body: form });
	if (!res.ok) throw new Error('Upload failed');
	return res.json();
}
