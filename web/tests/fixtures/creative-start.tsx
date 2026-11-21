// The Start here page on its own, for web/tests/creative-start.mjs.
import { createRoot } from 'react-dom/client';
import type { Brand } from '@outflare/sdk';
import { CreativeStart } from '../../src/pages/ads/CreativeStart';
import { DialogHost, ToastHost } from '../../src/ui';
import '../../src/index.css';
function Harness() {
  const params = new URLSearchParams(location.search);
  const brand = { id: '10000000-0000-0000-0000-000000000001', name: 'Acme', handle: 'acme' } as Brand;
  const w = window as any;
  return (
    <div className="px-4 py-6 sm:px-6 lg:px-8">
      <CreativeStart brand={brand} canManage={!params.has('readonly')} open={(v) => { w.opened = v; }} onConnectors={() => { w.wentToConnectors = true; }} openProfile={() => { w.wentToProfile = true; }} />
      <DialogHost />
      <ToastHost />
    </div>
  );
}
createRoot(document.getElementById('root')!).render(<Harness />);
