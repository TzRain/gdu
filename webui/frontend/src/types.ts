// Wire types shared with the Go backend (see webui/handlers.go and webui/tree.go).

export interface Node {
  name: string;
  path: string;
  isDir: boolean;
  isSymlink?: boolean;
  size: number;
  usage: number;
  itemCount: number;
  mtime: number;
  flag?: string;
}

export interface TreeNode extends Node {
  children: TreeNode[];
}

export interface NodeResponse {
  node: Node;
  breadcrumbs: Node[];
  children: Node[];
}

export interface Progress {
  currentItem: string;
  itemCount: number;
  totalUsage: number;
}

export type ScanState = 'scanning' | 'done' | 'error';

export interface Status {
  state: ScanState;
  error?: string;
  rootPath: string;
  progress: Progress;
  showApparentSize: boolean;
  showRelativeSize: boolean;
  useSIPrefix: boolean;
  deleteAllowed: boolean;
  rescanAvailable: boolean;
  scanVersion: number;
}

export interface BlobTarget {
  name: string;
  account: string;
  container: string;
  defaultPrefix: string;
  azureConfigDir: string;
  mountPath: string;
}

export interface BlobTransferResult {
  destination: string;
  notePath: string;
  bytes: number;
  mode: 'direct' | 'symlink';
}

export interface BlobJob {
  id: string;
  stage: 'copying' | 'copy_failed' | 'review_required' | 'uploaded' | 'cleaning' | 'completed';
  error?: string;
  source: string;
  destination: string;
  notePath: string;
  bytes: number;
  mode: 'direct' | 'symlink';
  azcopyJobId?: string;
  updatedAt: string;
}

export type SortKey = 'size' | 'name' | 'itemCount' | 'mtime';
export type SortOrder = 'asc' | 'desc';
