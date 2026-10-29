# Demo workspace

Screenshots and demos of Outflare come only from this workspace, never from app.outflare.ai or anyone's real workspace, even blurred (AGENTS.md, "What may go in a public repository"). Northpeak and everyone in it are made up: every person, company and domain ends in `.example`.

1. Start a fresh local engine (`make run`, with an empty database).
2. Create the owner and the workspace:

   ```sh
   curl -s -X POST localhost:8080/v1/setup -H 'content-type: application/json' \
     -d '{"email":"maya@northpeak.example","name":"Maya Lindqvist","password":"demo-password-not-real","workspace_name":"Northpeak","telemetry":false}'
   ```

3. Fill it. The script prints the ids the screenshots open.

   ```sh
   ws=$(psql -Atc "select id from workspaces where slug = 'northpeak'" "$DATABASE_URL")
   user=$(psql -Atc "select id from users where email = 'maya@northpeak.example'" "$DATABASE_URL")
   python3 scripts/demo/seed.py "$ws" "$user" 2> ids | psql -q "$DATABASE_URL"
   ```

   For the People page, add customer records through the API, with a token that has `customers.read` and `customers.manage` (Settings → API tokens):

   ```sh
   python3 scripts/demo/records.py "$ws" "$token"
   ```

4. Take the screenshots (Playwright with Chromium):

   ```sh
   mkdir -p screenshots && node scripts/demo/screenshots.mjs $(cat ids) screenshots
   ```

The website keeps them as WebP in `public/app/<page>-<theme>.webp` (outflareai/website). Look at every one before you commit it: no set-up banners, and nothing that isn't Northpeak.
