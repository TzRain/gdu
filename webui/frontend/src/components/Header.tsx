import { useGduModel } from '../model';
import { formatSize } from '../format';
import { Breadcrumbs } from './Breadcrumbs';
import { useState } from 'react';
import { BlobTargetsModal } from './BlobTargetsModal';

export function Header() {
  const [showBlobTargets, setShowBlobTargets] = useState(false);
  const [rescanPending, setRescanPending] = useState(false);
  const { status, nodeResp, total, useSIPrefix, view, toggleView, effectiveApparent, toggleApparent, handleSelect, requestRescan, setLoadError } =
    useGduModel();

  const onRescan = async () => {
    setRescanPending(true);
    setLoadError(null);
    try {
      await requestRescan();
    } catch (err) {
      setLoadError(err instanceof Error ? err.message : String(err));
    } finally {
      setRescanPending(false);
    }
  };

  return (
    <header className="app-header">
      <div className="app-title">
        <span className="logo">gdu</span>
        {nodeResp && <Breadcrumbs breadcrumbs={nodeResp.breadcrumbs} onNavigate={handleSelect} />}
      </div>
      <div className="app-actions">
        <span className="total">{formatSize(total, useSIPrefix)}</span>
        {status.rescanAvailable && (
          <button type="button" className="toggle" onClick={() => void onRescan()} disabled={status.state === 'scanning' || rescanPending}>
            {rescanPending ? '正在启动扫描…' : '重新扫描'}
          </button>
        )}
        <button type="button" className="toggle" onClick={() => setShowBlobTargets(true)}>Blob targets</button>
        <button
          type="button"
          className="toggle"
          onClick={toggleView}
          title="Toggle between donut and treemap charts"
        >
          {view === 'donut' ? 'Treemap' : 'Donut'}
        </button>
        <button
          type="button"
          className="toggle"
          onClick={toggleApparent}
          title="Toggle between disk usage and apparent size"
        >
          {effectiveApparent ? 'Apparent size' : 'Disk usage'}
        </button>
        {status.state === 'scanning' && <span className="scanning-badge">scanning…</span>}
      </div>
      {showBlobTargets && <BlobTargetsModal onClose={() => setShowBlobTargets(false)} />}
    </header>
  );
}
