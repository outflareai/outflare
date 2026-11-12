// The tracker's Report page on its own, for web/tests/tracker-report.mjs.
import { createRoot } from 'react-dom/client';
import { TrackerReport } from '../../src/pages/TrackerReport';
import { DialogHost, ToastHost } from '../../src/ui';
import '../../src/index.css';
(window as any).gone = [];
function Harness() {
  const params = new URLSearchParams(location.search);
  return (
    <div className="px-4 py-6 sm:px-6 lg:px-8">
      <TrackerReport ws="workspace-a" canManage={!params.has('readonly')} go={(tab, it) => (window as any).gone.push([tab, it ?? null])} />
      <DialogHost />
      <ToastHost />
    </div>
  );
}
createRoot(document.getElementById('root')!).render(<Harness />);
