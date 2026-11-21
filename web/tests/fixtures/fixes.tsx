// The Fixes page on its own, for web/tests/fixes.mjs. The open ticket is
// kept here, as the app keeps it in the URL.
import { useState } from 'react';
import { createRoot } from 'react-dom/client';
import { Fixes } from '../../src/pages/Fixes';
import { DialogHost, ToastHost } from '../../src/ui';
import '../../src/index.css';
(window as any).gone = [];
function Page() {
  const [open, setOpen] = useState('');
  return <Fixes ws="workspace-a" canManage go={(tab, it) => (window as any).gone.push([tab, it ?? null])} open={open} onOpen={setOpen} />;
}
createRoot(document.getElementById('root')!).render(
  <div className="px-4 py-6 sm:px-6 lg:px-8">
    <Page />
    <DialogHost />
    <ToastHost />
  </div>,
);
