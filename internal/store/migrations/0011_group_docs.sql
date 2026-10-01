-- Groups' descriptions: docs/design/03-grouping.md#descriptions.

-- A group's description: a static site a member uploaded as a zip, served under a random token to anyone who has its
-- link (G17). A group has one at most; uploading again replaces it under a new token, and a group that absorbs another
-- takes the other's, when it has none, by moving the row.
CREATE TABLE group_doc (
    id          bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    group_id    bigint      NOT NULL UNIQUE REFERENCES pr_group (id),
    token       text        NOT NULL UNIQUE,
    files       integer     NOT NULL,
    size        bigint      NOT NULL,
    uploaded_by bigint      NOT NULL REFERENCES member (id),
    uploaded_at timestamptz NOT NULL DEFAULT now()
);

-- One file of a description, by its path within the site.
CREATE TABLE group_doc_file (
    doc_id       bigint NOT NULL REFERENCES group_doc (id) ON DELETE CASCADE,
    path         text   NOT NULL,
    content_type text   NOT NULL,
    content      bytea  NOT NULL,
    PRIMARY KEY (doc_id, path)
);

-- A description changes what a group's page and the board show.
CREATE CONSTRAINT TRIGGER group_doc_notice AFTER INSERT OR UPDATE OR DELETE ON group_doc DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION notice('group', 'group_id');
