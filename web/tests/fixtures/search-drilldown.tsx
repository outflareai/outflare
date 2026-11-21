// Website analytics on its own, for web/tests/search-drilldown.mjs: the
// page's go() changes the item here the way the app shell does.
import { useState } from 'react';
import { createRoot } from 'react-dom/client';
import { Analytics } from '../../src/pages/Analytics';
import { DialogHost, ToastHost } from '../../src/ui';
import '../../src/index.css';
(window as any).gone = [];
function Harness() {
  const params = new URLSearchParams(location.search);
  const [item, setItem] = useState<string | undefined>(params.get('item') ?? undefined);
  return (
    <div className="px-4 py-6 sm:px-6 lg:px-8">
      <Analytics ws="workspace-a" canManage item={item} go={(tab, it) => { (window as any).gone.push([tab, it ?? null]); if (tab === 'analytics') setItem(it); }} />
      <DialogHost />
      <ToastHost />
    </div>
  );
}
createRoot(document.getElementById('root')!).render(<Harness />);
