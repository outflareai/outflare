-- A product can belong to a category, such as proxies or web_data, so a
-- brand with many products can read usage per family of products. A
-- metric can count only the events of one category's products.
ALTER TABLE products ADD COLUMN category text NOT NULL DEFAULT '';
ALTER TABLE metric_definitions ADD COLUMN product_category text NOT NULL DEFAULT '';
CREATE INDEX products_category_idx ON products (brand_id, category) WHERE category <> '';
