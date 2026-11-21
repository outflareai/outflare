// The Alerts page on its own, for web/tests/alerts.mjs.
import { createRoot } from 'react-dom/client';
import { Alerts } from '../../src/pages/Alerts';
import { DialogHost, ToastHost } from '../../src/ui';
import '../../src/index.css';
function Harness() {
  const params = new URLSearchParams(location.search);
  return (
    <div className="px-4 py-6 sm:px-6 lg:px-8">
      <Alerts ws="workspace-a" canManage={!params.has('readonly')} />
      <DialogHost />
      <ToastHost />
    </div>
  );
}
createRoot(document.getElementById('root')!).render(<Harness />);
