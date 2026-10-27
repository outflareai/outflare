-- What a brand does and how its customers pay, picked in brand setup.
-- business_model is an id from engine/internal/brands/business_models.json,
-- '' until picked; revenue_models holds ids of the ways customers pay.
ALTER TABLE brands ADD COLUMN business_model text NOT NULL DEFAULT '';
ALTER TABLE brands ADD COLUMN revenue_models text[] NOT NULL DEFAULT '{}';
