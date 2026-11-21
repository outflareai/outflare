// A smart list's own page, for web/tests/list-members.mjs.
import { createRoot } from 'react-dom/client';
import { SmartListDetail } from '../../src/pages/SmartLists';
import { DialogHost, ToastHost } from '../../src/ui';
import '../../src/index.css';
(window as any).gone = [];
const list = (window as any).__list ?? JSON.parse(new URLSearchParams(location.search).get('list')!);
function Harness() {
  return (
    <div className="px-4 py-6 sm:px-6 lg:px-8">
      <SmartListDetail ws="workspace-a" list={list} canManage onBack={() => {}} onEdit={() => {}} onChanged={() => {}} go={(tab, item) => (window as any).gone.push([tab, item])} />
      <DialogHost />
      <ToastHost />
    </div>
  );
}
createRoot(document.getElementById('root')!).render(<Harness />);
