-- Native checkout restrictions and scheduling for partner coupon codes.
ALTER TABLE partner_promo_codes ADD COLUMN rules jsonb NOT NULL DEFAULT '{}';
