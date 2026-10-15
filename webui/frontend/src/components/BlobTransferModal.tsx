import { useEffect, useState } from 'react';
import { fetchBlobTargets } from '../api';
import { useGduModel } from '../model';
import type { BlobTarget } from '../types';
import type { ItemActions } from '../useItemActions';
import { formatSize } from '../format';
import { Modal } from './Modal';

export function BlobTransferModal({ actions }: { actions: ItemActions }) {
  const { useSIPrefix, loadError } = useGduModel();
  const { transferCandidate, transferResult, actionPending, cancelTransfer, performTransfer } = actions;
  const [targets, setTargets] = useState<BlobTarget[]>([]);
  const [targetName, setTargetName] = useState('');
  const [mode, setMode] = useState<'direct' | 'symlink'>('direct');
  const [prefix, setPrefix] = useState('');
  const [fetchError, setFetchError] = useState<string | null>(null);

  useEffect(() => {
    if (!transferCandidate) return;
    let live = true;
    fetchBlobTargets().then((found) => {
      if (!live) return;
      setTargets(found);
      setTargetName(found[0]?.name ?? '');
      setPrefix(found[0]?.defaultPrefix ?? '');
      setMode('direct');
      setFetchError(null);
    }).catch((err: unknown) => {
      if (live) setFetchError(err instanceof Error ? err.message : String(err));
    });
    return () => { live = false; };
  }, [transferCandidate]);

  if (!transferCandidate) return null;
  const selected = targets.find((target) => target.name === targetName);

  return (
    <Modal titleId="blob-transfer-title" title="Transfer to Azure Blob" onClose={cancelTransfer} className="blob-modal">
      {transferResult ? (
        <>
          <p>Transfer complete. {transferResult.mode === 'symlink' ? 'A local symlink now points to the mounted Blob.' : 'The local content was removed.'} A transfer note was saved.</p>
          <p><strong>Azure location</strong><br /><code className="modal-path">{transferResult.destination}</code></p>
          <p><strong>Local note</strong><br /><code className="modal-path">{transferResult.notePath}</code></p>
          <div className="modal-actions"><button type="button" onClick={cancelTransfer}>Close</button></div>
        </>
      ) : (
        <>
          <p>AzCopy uploads the selected {transferCandidate.isDir ? 'directory' : 'file'} directly to Blob. After it confirms completion, gdu removes the local content and writes a transfer note.</p>
          <code className="modal-path">{transferCandidate.path}</code>
          <span className="muted">{formatSize(transferCandidate.size, useSIPrefix)}</span>
          <label className="blob-select-label">Blob target
            <select value={targetName} onChange={(e) => { setTargetName(e.target.value); setPrefix(targets.find((t) => t.name === e.target.value)?.defaultPrefix ?? ''); }} disabled={actionPending}>
              {targets.map((target) => <option key={target.name} value={target.name}>{target.name}</option>)}
            </select>
          </label>
          {selected && <p className="muted">{selected.account}/{selected.container}</p>}
          <label className="blob-select-label">Transfer folder rule
            <input value={prefix} onChange={(e) => setPrefix(e.target.value)} disabled={actionPending} placeholder="archive/{path}" />
          </label>
          <p className="muted">{'{path}'} becomes the selected local absolute path without its first “/”.</p>
          <label className="blob-select-label">Mode
            <select value={mode} onChange={(e) => setMode(e.target.value as 'direct' | 'symlink')} disabled={actionPending}>
              <option value="direct">Direct transfer — keep directory and note</option>
              <option value="symlink">Create symlink — requires Blob mount</option>
            </select>
          </label>
          {mode === 'symlink' && <p className="muted">Mounted Blob path: {selected?.mountPath || 'not configured'}</p>}
          {targets.length === 0 && <p>Add a Blob target using “Blob targets” in the toolbar first.</p>}
          {(fetchError || loadError) && <p className="blob-error" role="alert">{fetchError || loadError}</p>}
          <div className="modal-actions">
            <button type="button" onClick={cancelTransfer} disabled={actionPending}>Cancel</button>
            <button type="button" className="danger" disabled={actionPending || !selected || (mode === 'symlink' && !selected.mountPath)} onClick={() => void performTransfer(transferCandidate, targetName, mode, prefix)}>
              {actionPending ? 'Transferring…' : 'Transfer and remove local copy'}
            </button>
          </div>
        </>
      )}
    </Modal>
  );
}
