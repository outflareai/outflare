// The Creatives library and the Skills and prompts pages on their own, for
// web/tests/creatives.mjs: ?page=skills shows the skills; opening an item
// and going back change it here the way the app shell does.
import { useState } from 'react';
import { createRoot } from 'react-dom/client';
import type { Brand } from '@outflare/sdk';
import { CreativeLibrary } from '../../src/pages/ads/CreativeLibrary';
import { CreativeSkills } from '../../src/pages/ads/CreativeSkills';
import { DialogHost, ToastHost } from '../../src/ui';
import '../../src/index.css';
function Harness() {
  const params = new URLSearchParams(location.search);
  const [page, setPage] = useState(params.get('page') === 'skills' ? 'skills' : 'library');
  const [item, setItem] = useState('');
  const brand = { id: '10000000-0000-0000-0000-000000000001', name: 'Acme', handle: 'acme' } as Brand;
  const canManage = !params.has('readonly');
  return (
    <div className="px-4 py-6 sm:px-6 lg:px-8">
      {page === 'skills'
        ? <CreativeSkills brand={brand} canManage={canManage} item={item} open={setItem} back={() => setItem('')} openCreative={(id) => { setPage('library'); setItem(id); }} onConnectors={() => { (window as any).wentToConnectors = true; }} />
        : <CreativeLibrary brand={brand} canManage={canManage} item={item} open={setItem} back={() => setItem('')} />}
      <DialogHost />
      <ToastHost />
    </div>
  );
}
createRoot(document.getElementById('root')!).render(<Harness />);
