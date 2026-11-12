// The tracker's Offers, Landers and Flows pages on their own, for
// web/tests/tracker-library.mjs: ?page=offers|landers|flows.
import { createRoot } from 'react-dom/client';
import { TrackerFlows } from '../../src/pages/TrackerFlows';
import { TrackerLanders, TrackerOffers } from '../../src/pages/TrackerOffers';
import { DialogHost, ToastHost } from '../../src/ui';
import '../../src/index.css';
(window as any).gone = [];
(window as any).opened = [];
window.open = ((u: string) => { (window as any).opened.push(u); return null; }) as typeof window.open;
function Harness() {
  const params = new URLSearchParams(location.search);
  const canManage = !params.has('readonly');
  const page = params.get('page');
  return (
    <div className="px-4 py-6 sm:px-6 lg:px-8">
      {page === 'offers' && <TrackerOffers ws="workspace-a" canManage={canManage} />}
      {page === 'landers' && <TrackerLanders ws="workspace-a" canManage={canManage} />}
      {page === 'flows' && <TrackerFlows ws="workspace-a" canManage={canManage} item={params.get('item') ?? undefined} go={(tab, it) => (window as any).gone.push([tab, it ?? null])} />}
      <DialogHost />
      <ToastHost />
    </div>
  );
}
createRoot(document.getElementById('root')!).render(<Harness />);
