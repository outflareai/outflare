# Browser workers: tasks in a real browser on sites with no API

Some channels have no API: a community that only has a web form, a review site profile, a partner portal, a listing. Browser workers (`engine/internal/browser`, routes under `/v1/brands/{brand}/browser-tasks`) carry out bounded tasks on them in headless Chrome, driven by the workspace's own AI model, with the brand's account from the vault ([accounts.md](accounts.md)). They are the "Browser automation" section of the brand agent plan. Prefer an API, a feed, a registry or a connector wherever a site offers one; the browser is the fallback.

## A task is a capability

`POST .../browser-tasks` queues one with:

| Field | What it bounds |
| --- | --- |
| `goal` | What the task is for, from the agent or a person. |
| `hosts` | The sites the browser may reach, each with its subdomains. The account's own site and the sites of the identities it signs in through (its `depends_on` chain) are added. |
| `account_id` | The brand's account the task may sign in as; none for a task that signs in as nobody. |
| `operations` | `read` (open and read pages); `act` (click, type, submit); `sign_in` (from the vault). Default: read and act, plus sign_in with an account. |
| `content` | Approved texts by name. The worker types them exactly as given; the model never composes content to publish. |
| `max_steps`, `max_minutes`, `max_cents` | Ceilings on tool calls, wall time and AI spend (defaults 40, 10 and 100). |

An agent's task is a change like any other on the action layer: it runs within the agent's grants or waits for a person. Scopes `browser.read` and `browser.manage`.

## The boundary is enforced by the worker

- **Network.** Every request the browser makes (navigations, redirects, scripts, images, XHR) is intercepted and checked against the task's hosts, over https, on public addresses only (private networks, loopback and cloud metadata addresses are refused, after resolving the name). A request outside fails as blocked by the client. The model's instructions are not the control; the worker is.
- **Secrets never reach the model.** The model sees pages as text with numbered elements, never screenshots. To sign in it says `fill_secret(ref, kind)`; the worker opens the vault for the task's account (`accounts.Vault.Open`), types the value (or the current TOTP code from a `totp_seed`) and scrubs the value out of everything the model sees afterwards, including a page that echoes it. The step log keeps the secret's kind, never its value. Text the model types itself is limited to 200 characters and refused when it looks like a code, a key or a password.
- **Sign-ins are tracked.** After each attempt the model reports the outcome (`report_sign_in`), which records an attempt on the account and keeps its health and incidents like any other managed use. A refused credential, a lock or a wrong identity stops the task.
- **A person for MFA, CAPTCHA, passkeys and consent.** The model calls `need_person(reason, note)`; the worker opens a takeover request on the account and the task waits (`waiting_person`). When the person hands the account back, the task is queued again with a fresh browser session (the model is told to sign in again). Codes are never typed by the model, never stored and never shown.
- **Ceilings and cancelling.** A task ends at its step, time or AI spend ceiling, and a cancel (`POST .../cancel`) stops a queued or waiting task at once and a running one at its next step.
- **One fresh profile per task.** No cookies, storage or history from any other task; the session is forgotten when the task ends.

## Running the workers

Browser tasks run only where there is a browser:

- `OUTFLARE_ROLE=browser` runs browser tasks and nothing else, on a host with Chrome or Chromium ([scaling.md](scaling.md)); it refuses to start without one. `OUTFLARE_BROWSER` names the executable (`auto`, the default, finds one on PATH); `OUTFLARE_BROWSER_WORKERS` is how many tasks run at once (default 2). [`deploy/browser/Dockerfile`](../deploy/browser/Dockerfile) builds the engine image with Chromium for this role.
- An `all` or `worker` process with Chrome on its host runs them too; `OUTFLARE_BROWSER=off` keeps it from doing so. Without a browser anywhere, tasks wait in the queue: `outflare_browser_tasks_queued` and `OutflareBrowserTasksWaiting` say so.
- `OUTFLARE_BROWSER_ALLOW_PRIVATE=true` lets tasks reach private addresses and plain http, for local development only.

The browser runs on the operator's own compute. There is no hosted browser vendor and no third-party key: the AI is the workspace's own, like every other AI call.

## Reading a task

`GET .../browser-tasks/{id}` has the task's state, the worker's summary, the page it ended on, the evidence it reported (a URL, a confirmation text, the sign-in outcome) and the log: every step of every run, with the tool, what it was given and what came back, cut short. `GET .../browser-tasks?status=open` lists what is queued, running or waiting for a person.

## Scopes, events, monitoring

- Audit actions `browser_task.created`, `.cancelled`, and per run `.done`, `.failed`, `.waiting_person`; AI spend is booked under the `browser_tasks` task.
- Metrics `outflare_browser_tasks_total{status}`, `outflare_browser_steps_total{tool}`, `outflare_browser_tasks_queued{status}`, `outflare_browser_tasks_oldest_seconds`; the **Browser workers** dashboard row; alerts `OutflareBrowserTasksWaiting` and `OutflareBrowserTasksFailing` ([monitoring.md](monitoring.md)).
- MCP tools `run_browser_task`, `list_browser_tasks`, `get_browser_task`, `cancel_browser_task`.

## Not here yet

Keeping a signed-in session across runs (the `session` secret kind in the vault), a channel manifest entry that marks sites as browser-only, and the managed fleet (capacity, isolation and cost) that Cloud runs.
