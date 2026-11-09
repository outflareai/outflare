import { createRoot } from 'react-dom/client';
import { RecordPartners } from '../../src/pages/Partners';
import '../../src/index.css';
(window as any).opened = [];
createRoot(document.getElementById('root')!).render(<div className="p-4 text-sm"><RecordPartners ws="acme" record="60000000-0000-4000-8000-000000000001" go={(t, item) => (window as any).opened.push([t, item])} /></div>);
