// The Errors page (or one error with ?item=) and Website tracking, for
// web/tests/error-capture.mjs and web/tests/errors-actions.mjs.
import { createRoot } from 'react-dom/client';
import { Errors } from '../../src/pages/Errors';
import { WebsiteTracking } from '../../src/pages/WebsiteTracking';
import { DialogHost, ToastHost } from '../../src/ui';
import '../../src/index.css';
(window as any).gone = [];
const params = new URLSearchParams(location.search);
const page = params.get('page');
const item = params.get('item') ?? undefined;
function Harness() {
  return (
    <div className="px-4 py-6 sm:px-6 lg:px-8">
      {page === 'tracking'
        ? <WebsiteTracking ws="workspace-a" canManage go={(tab) => (window as any).gone.push(tab)} />
        : <Errors ws="workspace-a" canManage canSwitch={page !== 'viewer'} item={item} go={(tab) => (window as any).gone.push(tab)} />}
      <DialogHost />
      <ToastHost />
    </div>
  );
}
createRoot(document.getElementById('root')!).render(<Harness />);
