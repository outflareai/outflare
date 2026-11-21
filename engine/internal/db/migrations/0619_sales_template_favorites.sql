-- Personal templates already belong to a workspace, brand and owner.
-- Constant default keeps this additive migration cheap for existing rows.
ALTER TABLE sales_templates ADD COLUMN favorite boolean NOT NULL DEFAULT false;
