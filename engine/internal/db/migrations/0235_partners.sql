-- Partners: one platform for customer referrals, affiliates and
-- influencers, replacing tools such as Tolt. A partner is a customer
-- record. A program holds the rules (credit, rewards, holds, payouts) and
-- each partner joins it with a membership that has their ref code and
-- campaign. Conversions come from the workspace's own backend (the
-- conversion API or an incoming postback) or a billing connection, and
-- each one may earn rewards: cash, a fixed fee or an action the
-- workspace's own system carries out (such as granting credit). Rewards
-- are an append-only ledger: amounts never change, corrections are new
-- entries. Outflare never holds or sends money: payouts are marked paid.

CREATE TABLE partner_programs (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id       uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id           uuid REFERENCES brands (id) ON DELETE SET NULL,
    name               text NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
    slug               text NOT NULL CHECK (slug ~ '^[a-z0-9][a-z0-9-]{0,59}$'),
    -- The preset it started from; every setting stays editable.
    kind               text NOT NULL CHECK (kind IN ('referral', 'affiliate', 'influencer')),
    status             text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'paused', 'archived')),
    -- Where partner links land, without tags.
    landing_url        text NOT NULL DEFAULT '' CHECK (length(landing_url) <= 2000),
    currency           text NOT NULL DEFAULT 'USD' CHECK (currency ~ '^[A-Z]{3}$'),
    -- The credit rule, pinned to the program: it never follows the
    -- workspace's attribution settings.
    credit_window_days integer NOT NULL DEFAULT 30 CHECK (credit_window_days BETWEEN 1 AND 365),
    credit_model       text NOT NULL DEFAULT 'last' CHECK (credit_model IN ('first', 'last')),
    -- A promo code beats a link when a conversion carries both.
    codes_win          boolean NOT NULL DEFAULT true,
    -- Rewards wait this long before they can be approved, so refunds
    -- inside it cancel them.
    hold_days          integer NOT NULL DEFAULT 30 CHECK (hold_days BETWEEN 0 AND 365),
    -- Approve rewards on their own once the hold ends; otherwise a person
    -- approves each one.
    auto_approve       boolean NOT NULL DEFAULT true,
    -- Partners who sign up wait for approval unless this is on.
    auto_accept        boolean NOT NULL DEFAULT false,
    min_payout_cents   bigint NOT NULL DEFAULT 0 CHECK (min_payout_cents >= 0),
    terms_url          text NOT NULL DEFAULT '' CHECK (length(terms_url) <= 2000),
    -- A connected billing account (a payment connector's id, such as
    -- stripe) whose payments and refunds are also partner sales: optional,
    -- beside the workspace's own backend.
    sales_source       text NOT NULL DEFAULT '' CHECK (sales_source ~ '^[a-z0-9_]{0,40}$'),
    -- The incoming postback URL's secret (ofb_), hashed. Shown once.
    postback_hash      bytea,
    postback_hint      text NOT NULL DEFAULT '',
    -- The hosted partner portal: off until turned on. It is served at
    -- <portal_subdomain>.<the operator's partners domain>, on a tracking
    -- domain tied to the program (its own CNAME), or at /p/<id>/ on this
    -- server. portal holds its look, pages, sign-up form and sender.
    portal_enabled     boolean NOT NULL DEFAULT false,
    portal_subdomain   text UNIQUE CHECK (portal_subdomain ~ '^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$'),
    portal             jsonb NOT NULL DEFAULT '{}',
    created_by         uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, slug)
);
CREATE UNIQUE INDEX partner_programs_postback_idx ON partner_programs (postback_hash) WHERE postback_hash IS NOT NULL;

-- Groups (tiers) let some partners earn different rates.
CREATE TABLE partner_groups (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    program_id   uuid NOT NULL REFERENCES partner_programs (id) ON DELETE CASCADE,
    name         text NOT NULL CHECK (length(name) BETWEEN 1 AND 80),
    created_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (program_id, name)
);

-- Reward rules stack: every rule that matches a conversion gives a reward,
-- except that among cash rules for the same trigger only the best match
-- counts (a group's own rule, then the highest tier reached).
CREATE TABLE partner_reward_rules (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id     uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    program_id       uuid NOT NULL REFERENCES partner_programs (id) ON DELETE CASCADE,
    -- Only partners in this group; null is everyone.
    group_id         uuid REFERENCES partner_groups (id) ON DELETE CASCADE,
    -- The conversion that earns it: signup, sale, or a custom conversion's
    -- event name.
    trigger          text NOT NULL CHECK (trigger ~ '^[a-z0-9][a-z0-9_.]{0,79}$' AND trigger <> 'refund'),
    -- partner: the person who referred. referred: the customer they
    -- brought (two-sided rewards), always an action.
    side             text NOT NULL DEFAULT 'partner' CHECK (side IN ('partner', 'referred')),
    kind             text NOT NULL CHECK (kind IN ('percent', 'fixed', 'action')),
    -- percent: basis points of the sale (2500 is 25%).
    percent_bp       integer CHECK (percent_bp BETWEEN 1 AND 10000),
    amount_cents     bigint CHECK (amount_cents > 0),
    -- action: what the workspace's own system should do, delivered by the
    -- reward.approved webhook.
    action           text NOT NULL DEFAULT '' CHECK (action = '' OR action ~ '^[a-z0-9][a-z0-9_.-]{0,59}$'),
    action_data      jsonb NOT NULL DEFAULT '{}',
    -- Sales only: first sale, every sale, or sales in the first N months.
    recurrence       text NOT NULL DEFAULT 'every' CHECK (recurrence IN ('first', 'every', 'months')),
    months           integer CHECK (months BETWEEN 1 AND 120),
    -- A tier: applies once the partner has this many earlier sales.
    min_sales        integer NOT NULL DEFAULT 0 CHECK (min_sales >= 0),
    -- Only these products; empty is all.
    product_ids      text[] NOT NULL DEFAULT '{}' CHECK (cardinality(product_ids) <= 50),
    created_at       timestamptz NOT NULL DEFAULT now(),
    CHECK (kind <> 'percent' OR (percent_bp IS NOT NULL AND trigger <> 'signup')),
    CHECK (kind <> 'fixed' OR amount_cents IS NOT NULL),
    CHECK (kind <> 'action' OR action <> ''),
    CHECK (side = 'partner' OR kind = 'action'),
    CHECK ((recurrence = 'months') = (months IS NOT NULL))
);
CREATE INDEX partner_reward_rules_program_idx ON partner_reward_rules (program_id);

-- A partner in a program. record_id is the partner's customer record.
CREATE TABLE partner_memberships (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id     uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    program_id       uuid NOT NULL REFERENCES partner_programs (id) ON DELETE CASCADE,
    record_id        uuid NOT NULL REFERENCES records (id) ON DELETE CASCADE,
    -- The ref code links carry. Kept as imported, so old links keep working.
    code             text NOT NULL CHECK (code ~ '^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$'),
    status           text NOT NULL DEFAULT 'active' CHECK (status IN ('pending', 'active', 'suspended', 'rejected')),
    group_id         uuid REFERENCES partner_groups (id) ON DELETE SET NULL,
    campaign_id      uuid REFERENCES tracking_campaigns (id) ON DELETE SET NULL,
    -- Free text such as "PayPal: ana@example.com", sealed like connector
    -- keys (bound to the row's id). No structured bank details.
    payout_method    bytea,
    note             text NOT NULL DEFAULT '' CHECK (length(note) <= 2000),
    source           text NOT NULL DEFAULT 'manual' CHECK (source IN ('manual', 'api', 'signup', 'import')),
    -- What they answered on the portal's sign-up form, and when they
    -- accepted the program's terms.
    application      jsonb NOT NULL DEFAULT '{}',
    terms_accepted_at timestamptz,
    -- Which emails they want: {"conversions": true, "payouts": true}.
    notify           jsonb NOT NULL DEFAULT '{"conversions": true, "payouts": true}',
    created_by       uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    UNIQUE (program_id, record_id)
);
CREATE UNIQUE INDEX partner_memberships_code_idx ON partner_memberships (workspace_id, lower(code));
CREATE INDEX partner_memberships_record_idx ON partner_memberships (workspace_id, record_id);

-- Promo codes tied to a partner (the code itself, as the customer types it).
CREATE TABLE partner_promo_codes (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    membership_id uuid NOT NULL REFERENCES partner_memberships (id) ON DELETE CASCADE,
    code          text NOT NULL CHECK (code ~ '^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$'),
    -- Made in the workspace's billing account too: the coupon it is for,
    -- the connection, the code's id there, and how that went.
    coupon        text NOT NULL DEFAULT '' CHECK (length(coupon) <= 200),
    integration_id uuid REFERENCES integrations (id) ON DELETE SET NULL,
    external_id   text NOT NULL DEFAULT '',
    sync_status   text NOT NULL DEFAULT 'local' CHECK (sync_status IN ('local', 'pending', 'created', 'failed')),
    sync_error    text NOT NULL DEFAULT '',
    created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX partner_promo_codes_code_idx ON partner_promo_codes (workspace_id, lower(code));

-- Clicks on a partner's short link (/r/<code>). The address is kept only
-- as a hash, for fraud checks.
CREATE TABLE partner_clicks (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    membership_id uuid NOT NULL REFERENCES partner_memberships (id) ON DELETE CASCADE,
    sub_id        text NOT NULL DEFAULT '' CHECK (length(sub_id) <= 100),
    referrer_host text NOT NULL DEFAULT '' CHECK (length(referrer_host) <= 255),
    ip_hash       bytea,
    created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX partner_clicks_membership_idx ON partner_clicks (membership_id, created_at);

-- A customer a partner brought, bound once per program by a conversion
-- from the server: a browser visit only proposes it.
CREATE TABLE partner_referrals (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    program_id    uuid NOT NULL REFERENCES partner_programs (id) ON DELETE CASCADE,
    membership_id uuid NOT NULL REFERENCES partner_memberships (id) ON DELETE CASCADE,
    record_id     uuid NOT NULL REFERENCES records (id) ON DELETE CASCADE,
    -- What credited it: ref, click, promo_code or visit (a site visit with
    -- the partner's ref in the credit window).
    via           text NOT NULL CHECK (via IN ('ref', 'click', 'promo_code', 'visit', 'manual', 'import')),
    click_id      uuid REFERENCES partner_clicks (id) ON DELETE SET NULL,
    sub_id        text NOT NULL DEFAULT '' CHECK (length(sub_id) <= 100),
    status        text NOT NULL DEFAULT 'lead' CHECK (status IN ('lead', 'customer', 'refunded', 'rejected')),
    first_sale_at timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (program_id, record_id)
);
CREATE INDEX partner_referrals_membership_idx ON partner_referrals (membership_id, created_at);
CREATE INDEX partner_referrals_record_idx ON partner_referrals (workspace_id, record_id);

-- Sign-ups, sales, refunds and custom conversions reported for referred
-- customers. Every reported conversion is also a customer event (so
-- attribution sees it); these rows are the ones credited to a partner.
CREATE TABLE partner_conversions (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    program_id    uuid NOT NULL REFERENCES partner_programs (id) ON DELETE CASCADE,
    referral_id   uuid NOT NULL REFERENCES partner_referrals (id) ON DELETE CASCADE,
    record_id     uuid NOT NULL REFERENCES records (id) ON DELETE CASCADE,
    type          text NOT NULL CHECK (type IN ('signup', 'sale', 'refund', 'custom')),
    -- A custom conversion's event name.
    event_name    text NOT NULL DEFAULT '' CHECK ((type = 'custom') = (event_name <> '')),
    recurring     boolean NOT NULL DEFAULT false,
    -- The sender's own id (a charge or order id), so a retry is stored once.
    external_id   text NOT NULL DEFAULT '' CHECK (length(external_id) <= 200),
    -- A refund names the sale it reverses.
    refund_of     uuid REFERENCES partner_conversions (id) ON DELETE SET NULL,
    amount_cents  bigint NOT NULL DEFAULT 0 CHECK (amount_cents >= 0),
    currency      text NOT NULL DEFAULT 'USD' CHECK (currency ~ '^[A-Z]{3}$'),
    product_id    text NOT NULL DEFAULT '' CHECK (length(product_id) <= 200),
    -- billing: a payment from a connected billing account (sales_source).
    source        text NOT NULL CHECK (source IN ('api', 'postback', 'billing', 'manual', 'import')),
    occurred_at   timestamptz NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX partner_conversions_external_idx ON partner_conversions (program_id, type, external_id) WHERE external_id <> '';
CREATE INDEX partner_conversions_referral_idx ON partner_conversions (referral_id, occurred_at);

-- Payouts to a partner: a batch of approved cash rewards, marked paid by
-- a person or the API once the workspace has paid from its own account.
CREATE TABLE partner_payouts (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    membership_id uuid NOT NULL REFERENCES partner_memberships (id) ON DELETE CASCADE,
    amount_cents  bigint NOT NULL CHECK (amount_cents > 0),
    currency      text NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    status        text NOT NULL DEFAULT 'due' CHECK (status IN ('due', 'paid', 'canceled')),
    note          text NOT NULL DEFAULT '' CHECK (length(note) <= 2000),
    reference     text NOT NULL DEFAULT '' CHECK (length(reference) <= 200),
    paid_at       timestamptz,
    created_by    uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX partner_payouts_membership_idx ON partner_payouts (membership_id, created_at);
CREATE INDEX partner_payouts_status_idx ON partner_payouts (workspace_id, status, created_at);

-- The reward ledger. amount_cents never changes once written; a clawback
-- or adjustment is a new entry (negative for cash) that names the one it
-- corrects. Status moves pending, then approved or rejected, then paid
-- (cash and fees, through a payout) or fulfilled (actions).
CREATE TABLE partner_rewards (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    program_id    uuid NOT NULL REFERENCES partner_programs (id) ON DELETE CASCADE,
    membership_id uuid NOT NULL REFERENCES partner_memberships (id) ON DELETE CASCADE,
    -- Who receives it: the partner, or the referred customer (side referred).
    side          text NOT NULL DEFAULT 'partner' CHECK (side IN ('partner', 'referred')),
    recipient_id  uuid NOT NULL REFERENCES records (id) ON DELETE CASCADE,
    kind          text NOT NULL CHECK (kind IN ('cash', 'fee', 'action', 'adjustment')),
    conversion_id uuid REFERENCES partner_conversions (id) ON DELETE SET NULL,
    rule_id       uuid REFERENCES partner_reward_rules (id) ON DELETE SET NULL,
    -- The entry this one corrects (a clawback or adjustment).
    reverses_id   uuid REFERENCES partner_rewards (id) ON DELETE SET NULL,
    amount_cents  bigint NOT NULL DEFAULT 0,
    currency      text NOT NULL DEFAULT 'USD' CHECK (currency ~ '^[A-Z]{3}$'),
    action        text NOT NULL DEFAULT '',
    action_data   jsonb NOT NULL DEFAULT '{}',
    status        text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'rejected', 'paid', 'fulfilled')),
    hold_until    timestamptz NOT NULL DEFAULT now(),
    payout_id     uuid REFERENCES partner_payouts (id) ON DELETE SET NULL,
    note          text NOT NULL DEFAULT '' CHECK (length(note) <= 2000),
    approved_at   timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    CHECK (kind = 'adjustment' OR amount_cents >= 0),
    CHECK (kind <> 'action' OR action <> '')
);
CREATE INDEX partner_rewards_membership_idx ON partner_rewards (membership_id, created_at);
CREATE INDEX partner_rewards_due_idx ON partner_rewards (hold_until) WHERE status = 'pending';
CREATE INDEX partner_rewards_conversion_idx ON partner_rewards (conversion_id);
CREATE INDEX partner_rewards_payout_idx ON partner_rewards (payout_id) WHERE payout_id IS NOT NULL;

-- Portal sign-in. A partner signs in to one program's portal with a link
-- sent to their email through the workspace's own email provider. Both
-- secrets are kept only as hashes.
CREATE TABLE partner_sign_in_links (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    program_id   uuid NOT NULL REFERENCES partner_programs (id) ON DELETE CASCADE,
    record_id    uuid NOT NULL REFERENCES records (id) ON DELETE CASCADE,
    token_hash   bytea NOT NULL UNIQUE,
    -- Set when someone on the team made it to see the portal as the
    -- partner (read-only).
    impersonated_by uuid REFERENCES actors (id) ON DELETE CASCADE,
    expires_at   timestamptz NOT NULL,
    used_at      timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX partner_sign_in_links_expires_idx ON partner_sign_in_links (expires_at);

CREATE TABLE partner_sessions (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id    uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    program_id      uuid NOT NULL REFERENCES partner_programs (id) ON DELETE CASCADE,
    record_id       uuid NOT NULL REFERENCES records (id) ON DELETE CASCADE,
    token_hash      bytea NOT NULL UNIQUE,
    -- Someone on the workspace's team seeing the portal as the partner
    -- does: read-only, and recorded in the audit log.
    impersonated_by uuid REFERENCES actors (id) ON DELETE CASCADE,
    -- Opened by the workspace's backend for the referral widget in its own
    -- app, which works while the portal is closed.
    widget          boolean NOT NULL DEFAULT false,
    expires_at      timestamptz NOT NULL,
    last_seen_at    timestamptz NOT NULL DEFAULT now(),
    created_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX partner_sessions_expires_idx ON partner_sessions (expires_at);

-- Banners, copy and email swipes for partners, shown in the portal.
CREATE TABLE partner_assets (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    program_id   uuid NOT NULL REFERENCES partner_programs (id) ON DELETE CASCADE,
    kind         text NOT NULL CHECK (kind IN ('image', 'text', 'link')),
    title        text NOT NULL CHECK (length(title) BETWEEN 1 AND 200),
    body         text NOT NULL DEFAULT '' CHECK (length(body) <= 10000),
    url          text NOT NULL DEFAULT '' CHECK (length(url) <= 2000),
    position     integer NOT NULL DEFAULT 0,
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX partner_assets_program_idx ON partner_assets (program_id, position);

-- A program's own portal domain is a tracking domain it points at, so it is
-- verified, one-click DNS'd and given HTTPS like any tracking domain. The
-- link lives here, not on tracking_domains, so the version before this one
-- reads tracking domains exactly as it did.
ALTER TABLE partner_programs ADD COLUMN portal_domain_id uuid UNIQUE REFERENCES tracking_domains (id) ON DELETE SET NULL;
