import { useLayoutEffect, useState } from 'react';
import { createRoot } from 'react-dom/client';
import { ConnectComputer } from '../../src/pages/ConnectComputer';
import { AIAccounts } from '../../src/pages/AIAccounts';
import '../../src/index.css';
function Harness() {
 const [ws,setWs]=useState('workspace-a');
 const readonly = new URLSearchParams(location.search).has('readonly');
 (window as any).switchWorkspace=setWs;
 useLayoutEffect(()=>{ ((window as any).firstFrame??={})[ws]=document.body.textContent; },[ws]);
 return <AIAccounts key={ws} ws={ws} me="user-a" canAdd={!readonly} seeAll={!readonly} manageAll={!readonly} seeSpend={false} canSetUp={false} go={()=>{}} />;
}
const linking = new URLSearchParams(location.search).has('connect');
createRoot(document.getElementById('root')!).render(linking ? <ConnectComputer workspaces={[{id:'workspace-a',slug:'acme',name:'Acme',features:['ai_accounts'],scopes:['my_ai_accounts.manage']}]} onDone={()=>{}} /> : <Harness/>);
