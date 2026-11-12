-- First-party fraud signals (plans/ad-tracker.md, B7a; ideas/fraud-signals.md):
-- why a tracker click or conversion is suspect (engine/internal/fraud), such
-- as datacenter_network, repeat_click or fast_conversion; empty when it
-- isn't. A suspect click still goes where a person's would, but caps leave
-- it out, and reports and AI weights can. Only adds; the default is
-- constant, so adding it doesn't rewrite the tables.
ALTER TABLE tracking_link_clicks ADD COLUMN fraud_reasons text[] NOT NULL DEFAULT '{}';
ALTER TABLE tracker_conversions ADD COLUMN fraud_reasons text[] NOT NULL DEFAULT '{}';
