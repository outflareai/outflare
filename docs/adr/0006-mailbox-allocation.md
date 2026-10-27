# 6. Mailbox allocation is rules and scoring, enforced in the schema

Status: Accepted (23 October 2026)

## Decision

Which mailbox sends for which sequence is decided by a deterministic planner (`engine/internal/allocation`): a pure function from the workspace's sequences, mailboxes, senders, teams and locks to a list of changes, each with a plain-language reason. It runs every minute and after relevant changes, under a per-workspace advisory lock. AI is not used to decide assignments; it may later help match senders, forecast demand and explain plans.

The hard rules live in the database where they can:

- One sending home per mailbox: `mailbox_assignments.mailbox_id` is the primary key.
- One lock per mailbox: `mailbox_locks.mailbox_id` is the primary key, with checks tying the lock kind to its target.
- Follow-ups never change mailbox: the sending engine sends a follow-up only from the enrollment's mailbox, and a mailbox with leads in flight drains instead of moving.

Every change records the assignment version it produced, so a suggestion or undo prepared against an old state fails instead of overwriting newer work.

## Why

People need to predict, audit and undo what happened to their mailboxes; a rule-based planner gives the same answer for the same input and can say why. Putting the hard rules in the schema means no code path, including future platform connectors and agents, can break them. Agents are held to the same rules as people, with their limits on the API token.

## Outside sequencers (added 23 October 2026)

EmailBison, Instantly and Smartlead accounts join the same planner as connectors with the `observe.mailboxes` and `control.mailboxes` capabilities. Each live platform campaign is linked to a sequence (`campaign_links`), so it is planned like any other; the platform keeps steps, leads and status. The planner only routes a mailbox to a linked sequence when the campaign's account reports it healthy, and never routes a mailbox Outflare has no login for to a sequence Outflare sends itself.

Changes are pushed to the platform as jobs and count as done only when the next read shows them (`confirmed_at`). Changes made in the platform are accepted as manual ones. Because platforms don't report which leads each mailbox owes follow-ups, a mailbox leaving a platform campaign drains for the campaign's sequence length before it is detached. Nothing moves in a newly connected account until a person reviews its first plan, and an account not read in 30 minutes makes its mailboxes count as busy.
