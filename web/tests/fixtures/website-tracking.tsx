// The Website tracking page on its own, for web/tests/website-tracking.mjs.
import { createRoot } from 'react-dom/client';
import { WebsiteTracking } from '../../src/pages/WebsiteTracking';
import { DialogHost, ToastHost } from '../../src/ui';
import '../../src/index.css';
(window as any).gone = [];
function Harness() {
  return (
    <div className="px-4 py-6 sm:px-6 lg:px-8">
      <WebsiteTracking ws="workspace-a" canManage go={(tab) => (window as any).gone.push(tab)} />
      <DialogHost />
      <ToastHost />
    </div>
  );
}
createRoot(document.getElementById('root')!).render(<Harness />);
