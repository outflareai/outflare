// One dashboard on its own, for web/tests/dashboard-report-tile.mjs.
import { createRoot } from 'react-dom/client';
import { Dashboards } from '../../src/pages/Dashboards';
import { DialogHost, ToastHost } from '../../src/ui';
import '../../src/index.css';
createRoot(document.getElementById('root')!).render(
  <div className="px-4 py-6 sm:px-6 lg:px-8">
    <Dashboards ws="workspace-a" canManage go={() => {}} item="d0000000-0000-0000-0000-000000000001" />
    <DialogHost />
    <ToastHost />
  </div>,
);
