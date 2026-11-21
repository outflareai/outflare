// The one Inbox on its own, for web/tests/inbox.mjs. The open item is
// kept here, as the app keeps it in the URL.
import { useState } from 'react';
import { createRoot } from 'react-dom/client';
import { Support } from '../../src/pages/Support';
import { DialogHost, ToastHost } from '../../src/ui';
import '../../src/index.css';
function Page() {
  const [open, setOpen] = useState<string | undefined>();
  return <Support ws="workspace-a" canManage canSetUp myId="50000000-0000-0000-0000-000000000001" open={open} setOpen={setOpen} go={() => {}} unified />;
}
createRoot(document.getElementById('root')!).render(
  <div className="px-4 py-6 sm:px-6 lg:px-8">
    <Page />
    <DialogHost />
    <ToastHost />
  </div>,
);
