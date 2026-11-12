// The tracker's Campaigns page on its own, for web/tests/tracker-campaigns.mjs.
import { createRoot } from 'react-dom/client';
import { TrackerCampaigns } from '../../src/pages/TrackerCampaigns';
import { DialogHost, ToastHost } from '../../src/ui';
import '../../src/index.css';
(window as any).gone = [];
function Harness() {
  const params = new URLSearchParams(location.search);
  return (
    <div className="px-4 py-6 sm:px-6 lg:px-8">
      <TrackerCampaigns ws="workspace-a" canManage={!params.has('readonly')} item={params.get('item') ?? undefined} go={(tab, it) => (window as any).gone.push([tab, it ?? null])} />
      <DialogHost />
      <ToastHost />
    </div>
  );
}
createRoot(document.getElementById('root')!).render(<Harness />);
