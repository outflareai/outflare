// The tracker's Logs page on its own, for web/tests/tracker-logs.mjs.
import { createRoot } from 'react-dom/client';
import { TrackerLogs } from '../../src/pages/TrackerLogs';
import { DialogHost, ToastHost } from '../../src/ui';
import '../../src/index.css';
(window as any).gone = [];
createRoot(document.getElementById('root')!).render(
  <div className="px-4 py-6 sm:px-6 lg:px-8">
    <TrackerLogs ws="workspace-a" go={(tab, it) => (window as any).gone.push([tab, it ?? null])} />
    <DialogHost />
    <ToastHost />
  </div>,
);
