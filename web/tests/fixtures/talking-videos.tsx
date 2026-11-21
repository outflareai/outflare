// The talking videos pages on their own, for web/tests/talking-videos.mjs:
// opening an item and going back change it here the way the app shell does.
import { useState } from 'react';
import { createRoot } from 'react-dom/client';
import type { Brand } from '@outflare/sdk';
import { TalkingVideos } from '../../src/pages/ads/TalkingVideos';
import { DialogHost, ToastHost } from '../../src/ui';
import '../../src/index.css';
function Harness() {
  const params = new URLSearchParams(location.search);
  const [item, setItem] = useState(params.get('item') ?? '');
  const brand = { id: '10000000-0000-0000-0000-000000000001', name: 'Acme', handle: 'acme' } as Brand;
  return (
    <div className="px-4 py-6 sm:px-6 lg:px-8">
      <TalkingVideos brand={brand} canManage={!params.has('readonly')} item={item} open={setItem} back={() => setItem('')} onConnectors={() => { (window as any).wentToConnectors = true; }} />
      <DialogHost />
      <ToastHost />
    </div>
  );
}
createRoot(document.getElementById('root')!).render(<Harness />);
