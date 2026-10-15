import { useEffect, useState } from 'react';
import { fetchBlobJobs, fetchBlobTargets, resumeBlobJob, saveBlobTargets } from '../api';
import { useGduModel } from '../model';
import type { BlobJob, BlobTarget } from '../types';
import { Modal } from './Modal';

const emptyTarget = (): BlobTarget => ({
  name: '', account: '', container: '', defaultPrefix: '', azureConfigDir: '', mountPath: '',
});

export function BlobTargetsModal({ onClose }: { onClose: () => void }) {
  const { actionToken } = useGduModel();
  const [targets, setTargets] = useState<BlobTarget[]>([]);
  const [jobs, setJobs] = useState<BlobJob[]>([]);
  const [busy, setBusy] = useState(true);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    fetchBlobTargets()
      .then(setTargets)
      .catch((err: unknown) => setError(err instanceof Error ? err.message : String(err)))
      .finally(() => setBusy(false));
    fetchBlobJobs().then(setJobs).catch((err: unknown) => setError(err instanceof Error ? err.message : String(err)));
  }, []);

  const refreshJobs = async () => setJobs(await fetchBlobJobs());

  const resume = async (job: BlobJob) => {
    if (!actionToken) { setError('Open the URL printed by gdu to enable actions.'); return; }
    if (!window.confirm(`Resume job ${job.id}?\nSource: ${job.source}\nBlob: ${job.destination}\nAfter upload verification, remaining local files will be deleted.`)) return;
    setBusy(true);
    setError(null);
    try {
      await resumeBlobJob(job.id, actionToken);
      await refreshJobs();
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : String(err));
      await refreshJobs().catch(() => {});
    } finally { setBusy(false); }
  };

  const update = (index: number, field: keyof BlobTarget, value: string) => {
    setTargets((old) => old.map((target, i) => i === index ? { ...target, [field]: value } : target));
  };

  const save = async () => {
    if (!actionToken) {
      setError('Open the URL printed by gdu to enable actions.');
      return;
    }
    setBusy(true);
    setError(null);
    try {
      await saveBlobTargets(targets, actionToken);
      onClose();
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal titleId="blob-targets-title" title="Blob targets" onClose={onClose} className="blob-modal">
      <p>Each target writes directly to Azure Blob with AzCopy. Set a default folder inside the container for transfers.</p>
      {targets.map((target, index) => (
        <fieldset key={index} className="blob-target-fields">
          <legend>{target.name || `Target ${index + 1}`}</legend>
          <label>Name<input value={target.name} onChange={(e) => update(index, 'name', e.target.value)} placeholder="Research archive" /></label>
          <label>Storage account<input value={target.account} onChange={(e) => update(index, 'account', e.target.value)} placeholder="mystorageaccount" /></label>
          <label>Container<input value={target.container} onChange={(e) => update(index, 'container', e.target.value)} placeholder="deepresearch" /></label>
          <label>Default transfer folder<input value={target.defaultPrefix} onChange={(e) => update(index, 'defaultPrefix', e.target.value)} placeholder="archive/from-gdu" /></label>
          <label>Azure CLI config directory<input value={target.azureConfigDir} onChange={(e) => update(index, 'azureConfigDir', e.target.value)} placeholder="Leave blank to use the current CLI login" /></label>
          <label>Blobfuse mount path (for symlink mode)<input value={target.mountPath || ''} onChange={(e) => update(index, 'mountPath', e.target.value)} placeholder="/mnt/deepresearch" /></label>
          <button type="button" onClick={() => setTargets((old) => old.filter((_, i) => i !== index))}>Remove target</button>
        </fieldset>
      ))}
      <section>
        <h3>Transfer jobs</h3>
        <button type="button" onClick={() => void refreshJobs()} disabled={busy}>Refresh jobs</button>
        {jobs.length === 0 && <p className="muted">No recorded transfers.</p>}
        {jobs.map((job) => <div key={job.id} className="blob-target-fields">
          <strong>{job.stage}: {job.source}</strong>
          <code className="modal-path">{job.destination}</code>
          <span className="muted">Job {job.id}{job.azcopyJobId ? ` · AzCopy ${job.azcopyJobId}` : ''}</span>
          {job.error && <span className="blob-error">{job.error}</span>}
          {job.stage !== 'completed' && job.stage !== 'review_required' &&
            <button type="button" disabled={busy} onClick={() => void resume(job)}>Resume and confirm deletion</button>}
        </div>)}
      </section>
      {error && <p className="blob-error" role="alert">{error}</p>}
      <div className="modal-actions">
        <button type="button" onClick={() => setTargets((old) => [...old, emptyTarget()])} disabled={busy}>Add target</button>
        <button type="button" onClick={onClose}>Cancel</button>
        <button type="button" onClick={() => void save()} disabled={busy}>Save targets</button>
      </div>
    </Modal>
  );
}
