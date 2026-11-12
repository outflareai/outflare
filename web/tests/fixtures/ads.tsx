// The Paid ads page on its own, for web/tests/ads.mjs: the page's go()
// changes the item here the way the app shell does, and every other tab it
// opens is recorded for the test to read.
import { useState } from 'react';
import { createRoot } from 'react-dom/client';
import { Ads } from '../../src/pages/Ads';
import { DialogHost, ToastHost } from '../../src/ui';
import '../../src/index.css';
(window as any).gone = [];
function Harness() {
  const params = new URLSearchParams(location.search);
  const [item, setItem] = useState<string | undefined>(params.get('item') ?? undefined);

  return (
    <div className="px-4 py-6 sm:px-6 lg:px-8">
      <Ads ws="workspace-a" canManage={!params.has('readonly')} item={item} go={(tab, it) => { (window as any).gone.push([tab, it ?? null]); if (tab === 'ads') setItem(it); }} />
      <DialogHost />
      <ToastHost />
    </div>
  );
}
createRoot(document.getElementById('root')!).render(<Harness />);
