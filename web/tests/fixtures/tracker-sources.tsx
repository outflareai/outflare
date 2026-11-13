// The tracker's Traffic sources and Affiliate networks pages on their own,
// for web/tests/tracker-sources.mjs: ?page=sources|networks.
import { createRoot } from 'react-dom/client';
import { TrackerNetworks, TrackerTrafficSources } from '../../src/pages/TrackerSources';
import { DialogHost, ToastHost } from '../../src/ui';
import '../../src/index.css';
(window as any).gone = [];
function Harness() {
  const params = new URLSearchParams(location.search);
  const canManage = !params.has('readonly');
  const go = (tab: string, it?: string) => (window as any).gone.push([tab, it ?? null]);
  return (
    <div className="px-4 py-6 sm:px-6 lg:px-8">
      {params.get('page') === 'sources' && <TrackerTrafficSources ws="workspace-a" canManage={canManage} go={go as never} />}
      {params.get('page') === 'networks' && <TrackerNetworks ws="workspace-a" canManage={canManage} go={go as never} />}
      <DialogHost />
      <ToastHost />
    </div>
  );
}
createRoot(document.getElementById('root')!).render(<Harness />);
