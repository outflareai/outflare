// The tracker's Setup page on its own, for web/tests/tracker-setup.mjs.
import { createRoot } from 'react-dom/client';
import { TrackerSetup } from '../../src/pages/TrackerSetup';
import { DialogHost, ToastHost } from '../../src/ui';
import '../../src/index.css';
(window as any).gone = [];
function Harness() {
  const params = new URLSearchParams(location.search);
  return (
    <div className="px-4 py-6 sm:px-6 lg:px-8">
      <TrackerSetup ws="workspace-a" canManage={!params.has('readonly')} go={(tab, it) => (window as any).gone.push([tab, it ?? null])} />
      <DialogHost />
      <ToastHost />
    </div>
  );
}
createRoot(document.getElementById('root')!).render(<Harness />);
