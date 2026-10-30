"""Writes SQL that fills a local workspace with the Northpeak demo data.

Screenshots for outflare.ai and the docs come only from this workspace
(AGENTS.md, "What may go in a public repository"). Every person, company and
domain here is fictional (.example). See README.md for the steps.

Usage: python3 seed.py WORKSPACE_ID USER_ID | psql "$DATABASE_URL"
Prints the ids the screenshots open (sequence, reply) to stderr.
"""
import random
import sys
import uuid

ws, user = sys.argv[1], sys.argv[2]
rnd = random.Random(7)
out = []
q = lambda s: "'" + s.replace("'", "''") + "'"

domains = [("northpeak.example", "pass"), ("northpeakmail.example", "pass"), ("trynorthpeak.example", "none_policy")]
senders = ["alex", "casey", "jordan", "riley", "sam", "taylor"]
limits = {"northpeak.example": 50, "northpeakmail.example": 30, "trynorthpeak.example": 40}

for d, dmarc in domains:
    out.append(f"""INSERT INTO sending_domains (workspace_id, domain, mx_provider, spf, spf_record, dkim, dkim_selector, dmarc, dmarc_record, dmarc_policy, checked_at, status, mx_hosts)
VALUES ('{ws}', {q(d)}, 'google', 'pass', 'v=spf1 include:_spf.google.com ~all', 'pass', 'google', {q(dmarc)},
 {q('v=DMARC1; p=none' if dmarc == 'none_policy' else 'v=DMARC1; p=quarantine; rua=mailto:dmarc@' + d)}, {q('none' if dmarc == 'none_policy' else 'quarantine')},
 now() - interval '2 hours', 'active', ARRAY['aspmx.l.google.com']);""")

mailboxes = []
for d, _ in domains:
    for i, s in enumerate(senders):
        mid = str(uuid.UUID(int=rnd.getrandbits(128)))
        status = "paused" if (d == "northpeak.example" and s == "casey") else "active"
        mailboxes.append((mid, f"{s}@{d}", d, status))
        name = {"alex": "Alex Moreno", "casey": "Casey Okoro", "jordan": "Jordan Pike", "riley": "Riley Haas", "sam": "Sam Ferreira", "taylor": "Taylor Brandt"}[s]
        out.append(f"""INSERT INTO mailboxes (id, workspace_id, email, display_name, provider, auth_method, smtp_host, smtp_port, smtp_username, imap_host, imap_port, imap_username, credentials,
 status, daily_limit, smtp_ok, imap_ok, last_verified_at, last_synced_at, warmup_enabled, warmup_started_at, created_at)
VALUES ('{mid}', '{ws}', {q(s + '@' + d)}, {q(name)}, 'google', 'password', 'smtp.gmail.com', 587, {q(s + '@' + d)}, 'imap.gmail.com', 993, {q(s + '@' + d)}, '\\x00',
 '{status}', {limits[d]}, true, true, now() - interval '1 hour', now() - interval '2 minutes', true, now() - interval '{30 + i} days', now() - interval '40 days');""")

firsts = ["Zoe", "Theo", "Ivy", "Ava", "Owen", "Liam", "Mia", "Noah", "Ella", "Leo", "Nina", "Omar", "Priya", "Ravi", "Sara", "Tom", "Uma", "Vik", "Wren", "Yara"]
lasts = ["Okafor", "Haddad", "Muller", "Patel", "Brooks", "Larsen", "Tanaka", "Novak", "Reyes", "Silva", "Kowalski", "Mensah", "Duarte", "Ahmed", "Lindgren"]
companies = ["Acme", "Brightline", "Calderwood", "Fernway", "Halden", "Kestrel Labs", "Lumen Works", "Marlow", "Oakridge", "Pinecrest", "Quarry", "Redfern", "Saltmarsh", "Tidewater", "Umberly", "Vantage Row", "Willowby", "Yarrow", "Zephyr Data", "Copperline"]
leads = []
for n in range(240):
    f, l, c = rnd.choice(firsts), rnd.choice(lasts), companies[n % len(companies)]
    dom = c.lower().replace(" ", "") + ".example"
    lid = str(uuid.UUID(int=rnd.getrandbits(128)))
    email = f"{f.lower()}.{l.lower()}{n}@{dom}"
    leads.append((lid, email, f, l, c))
    out.append(f"INSERT INTO leads (id, workspace_id, email, first_name, last_name, company, title, website) VALUES ('{lid}', '{ws}', {q(email)}, {q(f)}, {q(l)}, {q(c)}, 'Founder', {q('https://' + dom)});")

seq = str(uuid.UUID(int=rnd.getrandbits(128)))
seq2 = str(uuid.UUID(int=rnd.getrandbits(128)))
out.append(f"INSERT INTO sequences (id, workspace_id, name, status, timezone, send_start, send_end, created_at) VALUES ('{seq}', '{ws}', 'Series A founders, Q4', 'active', 'America/New_York', '09:00', '17:00', now() - interval '20 days');")
out.append(f"INSERT INTO sequences (id, workspace_id, name, status, timezone, created_at) VALUES ('{seq2}', '{ws}', 'Agency partners', 'active', 'Europe/London', now() - interval '9 days');")
steps = [
    (0, "{{first_name}}, quick question about {{company}}", "Hi {{first_name}},\n\nSaw {{company}} just raised. Most teams at your stage spend a week wiring up outbound. Northpeak runs it from one prompt, with your own mailboxes and tools.\n\nWorth a look?\n\n{{sender_first_name}}"),
    (3, "", "Hi {{first_name}}, bumping this in case it got buried. Happy to send a 2-minute walkthrough.\n\n{{sender_first_name}}"),
    (5, "", "Last note from me, {{first_name}}. If outbound isn't a priority this quarter, no problem at all.\n\n{{sender_first_name}}"),
]
for i, (delay, subj, body) in enumerate(steps):
    out.append(f"INSERT INTO sequence_steps (sequence_id, position, delay_days, subject, body) VALUES ('{seq}', {i}, {delay}, {q(subj)}, {q(body)});")
out.append(f"INSERT INTO sequence_steps (sequence_id, position, delay_days, subject, body) VALUES ('{seq2}', 0, 0, 'Partnering with {{{{company}}}}', 'Hi {{{{first_name}}}},\n\nWe work with agencies like {{{{company}}}} on outbound for their clients.\n\n{{{{sender_first_name}}}}');")

sending = [m for m in active]
sent24 = sent7 = 0
for n, (lid, email, f, l, c) in enumerate(leads[:215]):
    m = sending[n % len(sending)]
    status = "completed" if n < 35 else "active"
    eid = str(uuid.uuid4())
    out.append(f"INSERT INTO enrollments (id, workspace_id, sequence_id, lead_id, mailbox_id, status, next_step, next_send_at, thread_subject, created_at) VALUES ('{eid}', '{ws}', '{seq}', '{lid}', '{m[0]}', '{status}', {3 if status == 'completed' else rnd.randint(1, 2)}, {'NULL' if status == 'completed' else "now() + interval '2 days'"}, {q(f + ', quick question about ' + c)}, now() - interval '18 days');")
    for k in range(2 if n % 3 else 3):
        hours = rnd.randint(1, 18 * 24)
        if hours < 24:
            sent24 += 1
        if hours < 24 * 7:
            sent7 += 1
        subj = f"{f}, quick question about {c}" if k == 0 else f"Re: {f}, quick question about {c}"
        out.append(f"INSERT INTO messages (workspace_id, enrollment_id, sequence_id, step_position, lead_id, mailbox_id, from_email, to_email, subject, body, message_id, status, attempts, created_at, sent_at) VALUES ('{ws}', '{eid}', '{seq}', {k}, '{lid}', '{m[0]}', {q(m[1])}, {q(email)}, {q(subj)}, 'Hi {f},', '<m{uuid.uuid4().hex}@demo>', 'sent', 1, now() - interval '{hours} hours', now() - interval '{hours} hours');")

# Replies: the open conversation, two more unread, and older read ones.
convo = leads[0]
zoe_mb = sending[0]
out.append(f"UPDATE leads SET first_name = 'Zoe', last_name = 'Okafor', company = 'Acme', email = 'zoe@acme.example' WHERE id = '{convo[0]}';")
out.append(f"UPDATE messages SET to_email = 'zoe@acme.example', body = CASE step_position WHEN 0 THEN E'Hi Zoe,\n\nSaw Acme just raised. Most teams at your stage spend a week wiring up outbound. Northpeak runs it from one prompt, with your own mailboxes and tools.\n\nWorth a look?\n\nAlex' ELSE E'Hi Zoe, bumping this in case it got buried. Happy to send a 2-minute walkthrough.\n\nAlex' END, subject = CASE step_position WHEN 0 THEN 'Zoe, quick question about Acme' ELSE 'Re: Zoe, quick question about Acme' END WHERE lead_id = '{convo[0]}';")
replies = [
    (convo[0], "zoe@acme.example", "Zoe Okafor", "Re: Zoe, quick question about Acme", "Interesting. Does it work with our existing Google Workspace inboxes, or do we need to move them?", 3, False),
    (leads[1][0], leads[1][1], f"{leads[1][2]} {leads[1][3]}", f"Re: {leads[1][2]}, quick question about {leads[1][4]}", "Yes, send the walkthrough over. Thursday works for a call.", 5, False),
    (leads[2][0], leads[2][1], f"{leads[2][2]} {leads[2][3]}", f"Re: {leads[2][2]}, quick question about {leads[2][4]}", "Who else is using this? Curious how it compares to what we run today.", 7, False),
]
for n in range(3, 14):
    lid, email, f, l, c = leads[n]
    replies.append((lid, email, f"{f} {l}", f"Re: {f}, quick question about {c}", "Thanks, not right now. Check back next quarter.", 20 + n * 9, True))
zoe_reply = str(uuid.UUID(int=rnd.getrandbits(128)))
for n, (lid, email, name, subj, body, hours, read) in enumerate(replies):
    m = sending[n % len(sending)]
    rid = zoe_reply if n == 0 else str(uuid.uuid4())
    out.append(f"INSERT INTO inbound_messages (id, workspace_id, mailbox_id, uidvalidity, uid, message_id, from_email, from_name, to_email, subject, body, kind, lead_id, sequence_id, read, received_at) VALUES ('{rid}', '{ws}', '{m[0]}', 1, {100 + n}, '<r{uuid.uuid4().hex}@demo>', {q(email)}, {q(name)}, {q(m[1])}, {q(subj)}, {q(body)}, 'reply', '{lid}', '{seq}', {str(read).lower()}, now() - interval '{hours} hours');")
    out.append(f"UPDATE enrollments SET status = 'replied', next_send_at = NULL WHERE lead_id = '{lid}' AND sequence_id = '{seq}';")
for n in range(2):
    m = sending[n + 3]
    out.append(f"INSERT INTO inbound_messages (workspace_id, mailbox_id, uidvalidity, uid, message_id, from_email, from_name, to_email, subject, body, kind, read, received_at) VALUES ('{ws}', '{m[0]}', 1, {300 + n}, '<b{uuid.uuid4().hex}@demo>', 'mailer-daemon@googlemail.com', 'Mail Delivery Subsystem', {q(m[1])}, 'Delivery Status Notification (Failure)', 'Address not found', 'bounce', true, now() - interval '{30 + n * 40} hours');")

# A set-up workspace: the owner's email is confirmed, the company is
# described and one AI app is connected, so no set-up prompts cover the page.
out.append(f"UPDATE users SET email_verified_at = now() WHERE id = '{user}';")
out.append(f"""INSERT INTO workspace_context (workspace_id, company, website, about, goals, voice)
VALUES ('{ws}', 'Northpeak', 'https://northpeak.example', 'Northpeak makes scheduling software for field service teams.', 'Book 40 demos a month with Series A founders.', 'Short, plain and friendly.')
ON CONFLICT (workspace_id) DO UPDATE SET company = EXCLUDED.company, website = EXCLUDED.website, about = EXCLUDED.about, goals = EXCLUDED.goals, voice = EXCLUDED.voice;""")
out.append(f"""WITH c AS (INSERT INTO oauth_clients (name, uri, redirect_uris) VALUES ('Claude', 'https://claude.ai', ARRAY['https://claude.ai/api/mcp/auth_callback']) RETURNING id),
t AS (INSERT INTO api_tokens (workspace_id, actor_id, name, prefix, token_hash, scopes) SELECT '{ws}', actor_id, 'Claude', 'oft_demo', '\\x01'::bytea, ARRAY['workspace:read'] FROM users WHERE id = '{user}' RETURNING id)
INSERT INTO oauth_grants (workspace_id, client_id, user_id, api_token_id, refresh_hash, expires_at)
SELECT '{ws}', c.id, '{user}', t.id, '\\x02'::bytea, now() + interval '30 days' FROM c, t;""")

print("BEGIN;")
print("\n".join(out))
print("COMMIT;")
print(f"{seq} {zoe_reply}", file=sys.stderr)
