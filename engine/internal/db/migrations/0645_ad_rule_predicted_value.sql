-- Qualified sign-ups with a predicted value: a conversion rule can send
-- each event with what people like the person went on to pay
-- (predicted_value), and only the ones at least as likely to pay as the
-- brand's average (qualified_only). Both come from the brand's own history
-- of who paid after signing up (adconv/predict.go).
--
-- Expand only (docs/migrations.md): new columns with defaults, which the
-- previous version never reads.
ALTER TABLE ad_conversion_rules ADD COLUMN predicted_value boolean NOT NULL DEFAULT false;
ALTER TABLE ad_conversion_rules ADD COLUMN qualified_only boolean NOT NULL DEFAULT false;
