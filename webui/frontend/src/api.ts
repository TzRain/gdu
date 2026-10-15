import type { BlobJob, BlobTarget, BlobTransferResult, NodeResponse, SortKey, SortOrder, Status, TreeNode } from './types';

async function responseError(res: Response): Promise<Error & { status: number }> {
  let message = `request failed: ${res.status}`;
  try {
    const body = (await res.json()) as { error?: string };
    if (body.error) {
      message = body.error;
    }
  } catch {
    // ignore non-JSON error bodies
  }
  return Object.assign(new Error(message), { status: res.status });
}

async function getJSON<T>(url: string): Promise<T> {
  const res = await fetch(url);
  if (!res.ok) {
    throw await responseError(res);
  }
  return (await res.json()) as T;
}

export function fetchStatus(): Promise<Status> {
  return getJSON<Status>('api/v1/status');
}

export async function rescanDisk(token: string): Promise<Status> {
  const res = await fetch('api/v1/rescan', {
    method: 'POST',
    headers: { 'X-GDU-Action': token },
  });
  if (!res.ok) throw await responseError(res);
  return (await res.json()) as Status;
}

export function fetchNode(
  path: string,
  sort: SortKey,
  order: SortOrder,
): Promise<NodeResponse> {
  const params = new URLSearchParams({ path, sort, order });
  return getJSON<NodeResponse>(`api/v1/nodes?${params.toString()}`);
}

export function fetchTree(path: string): Promise<TreeNode> {
  const params = new URLSearchParams({ path });
  return getJSON<TreeNode>(`api/v1/tree?${params.toString()}`);
}

// requestAction sends a mutating request carrying the per-process action
// token the server printed into this page's own URL (see
// GduModel.actionToken in model.tsx). The server rejects the request unless
// this exact token is echoed back, which keeps a page on another origin (or
// another browser tab, or another local user who never saw this URL) from
// triggering a delete/reveal even though it may still be able to make the
// browser send a request: it does not know the token, since the token is
// never served over any API response.
async function requestAction(url: string, token: string, init: RequestInit): Promise<void> {
  const res = await fetch(url, {
    ...init,
    headers: {
      'X-GDU-Action': token,
      ...init.headers,
    },
  });
  if (res.ok) {
    return;
  }
  throw await responseError(res);
}

export function revealNode(path: string, token: string): Promise<void> {
  return requestAction('api/v1/reveal', token, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ path }),
  });
}

export type DeleteMode = 'trash' | 'permanent';

export function deleteNode(path: string, token: string, mode: DeleteMode): Promise<void> {
  const params = new URLSearchParams({ path });
  if (mode === 'trash') {
    params.set('mode', 'trash');
  }
  return requestAction(`api/v1/nodes?${params.toString()}`, token, { method: 'DELETE' });
}

export function fetchBlobTargets(): Promise<BlobTarget[]> {
  return getJSON<BlobTarget[]>('api/v1/blob-targets');
}

export function fetchBlobJobs(): Promise<BlobJob[]> {
  return getJSON<BlobJob[]>('api/v1/blob-jobs');
}

export async function resumeBlobJob(id: string, token: string): Promise<BlobJob> {
  const res = await fetch('api/v1/blob-jobs/resume', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-GDU-Action': token },
    body: JSON.stringify({ id, confirmDelete: true }),
  });
  if (!res.ok) throw await responseError(res);
  return (await res.json()) as BlobJob;
}

export function saveBlobTargets(targets: BlobTarget[], token: string): Promise<void> {
  return requestAction('api/v1/blob-targets', token, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(targets),
  });
}

export async function transferToBlob(path: string, target: string, mode: 'direct' | 'symlink', prefix: string, token: string): Promise<BlobTransferResult> {
  const res = await fetch('api/v1/blob-transfers', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-GDU-Action': token },
    body: JSON.stringify({ path, target, mode, prefix }),
  });
  if (!res.ok) throw await responseError(res);
  return (await res.json()) as BlobTransferResult;
}

// subscribeStatus opens an SSE connection that yields status updates. Returns
// an unsubscribe function.
export function subscribeStatus(onStatus: (status: Status) => void): () => void {
  const source = new EventSource('api/v1/events');
  source.onmessage = (event: MessageEvent<string>) => {
    try {
      onStatus(JSON.parse(event.data) as Status);
    } catch {
      // ignore malformed frames
    }
  };
  return () => source.close();
}
