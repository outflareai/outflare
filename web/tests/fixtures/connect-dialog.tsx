// The connect dialog on its own, for web/tests/connect-dialog.mjs.
import { createRoot } from 'react-dom/client';
import { ConnectDialog } from '../../src/pages/Connectors';
import { ToastHost } from '../../src/ui';
import '../../src/index.css';
const def = {
  id: 'meta_ads', name: 'Meta Ads', category: 'ads', status: 'available', trust: 'outflare', summary: 'Spend, clicks and conversions per campaign.',
  description: 'Reads every ad account a token can see.', capabilities: ['ads'], tags: [], auth: 'api_key',
  auth_options: [{ method: 'api_key', available: true }],
  fields: [{ name: 'api_key', label: 'System user access token', secret: true, help: 'Generate a token for the ad accounts you want to read.' }],
};
function Harness() {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  return <><ConnectDialog ws="workspace-a" def={def as any} onSignIn={() => {}} onClose={() => {}} onDone={() => {}} /><ToastHost /></>;
}
createRoot(document.getElementById('root')!).render(<Harness />);
