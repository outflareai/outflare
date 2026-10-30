-- Extra query parameters that carry a partner's code on the website, besides
-- ref and via (such as aff or fpr, for links made on another platform).
ALTER TABLE partner_programs
    ADD COLUMN url_params text[] NOT NULL DEFAULT '{}';
