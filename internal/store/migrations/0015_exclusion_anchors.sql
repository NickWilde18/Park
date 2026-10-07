-- An exclusion names, besides its group, the anchors whose claims had placed its pull request in that group, as
-- [{"source", "key"}], and holds against them wherever they point later (G6, docs/design/03-grouping.md#rules): a
-- group left with no open pull request is archived and its anchors released, and the claims that placed a pull
-- request there must not put it back in a group of their own.
ALTER TABLE assertion ADD COLUMN excluded_anchors jsonb;
ALTER TABLE assertion ADD CONSTRAINT assertion_excluded_anchors_check CHECK (excluded_anchors IS NULL OR negative);
