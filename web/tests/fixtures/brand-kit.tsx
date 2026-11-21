// The Brand kit page on its own, for web/tests/brand-kit.mjs: opening a
// file and going back change the item here the way the app shell does.
import { useState } from 'react';
import { createRoot } from 'react-dom/client';
import type { Brand } from '@outflare/sdk';
import { BrandKit } from '../../src/pages/ads/BrandKit';
import { DialogHost, ToastHost } from '../../src/ui';
import '../../src/index.css';
function Harness() {
  const params = new URLSearchParams(location.search);
  const [item, setItem] = useState('');
  const [colors, setColors] = useState({ primary: '#16a34a', background: '', text: '#0a0a0a' });
  const brand = { id: '10000000-0000-0000-0000-000000000001', name: 'Acme', handle: 'acme', colors } as Brand;
  return (
    <div className="px-4 py-6 sm:px-6 lg:px-8">
      <BrandKit brand={brand} ws="workspace-a" canManage={!params.has('readonly')} item={item} open={setItem} back={() => setItem('')} brandChanged={() => setColors((window as any).savedColors ?? colors)} />
      <DialogHost />
      <ToastHost />
    </div>
  );
}
createRoot(document.getElementById('root')!).render(<Harness />);
