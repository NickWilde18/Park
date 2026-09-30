-- Chains and groups: docs/design/03-grouping.md. Chains are derived on read and not stored.

-- A group of pull requests that form one piece of work. Its identity does not depend on its members (G8).
CREATE TABLE pr_group (
    id          bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name        text        NOT NULL,
    name_source text        NOT NULL CHECK (name_source IN ('manual', 'external', 'issue', 'branch', 'refs')),
    state       text        NOT NULL DEFAULT 'active' CHECK (state IN ('active', 'archived')),
    created_at  timestamptz NOT NULL DEFAULT now(),
    archived_at timestamptz
);

-- An absorbed group, and the group it now resolves to.
CREATE TABLE group_alias (
    group_id  bigint      PRIMARY KEY REFERENCES pr_group (id),
    alias_of  bigint      NOT NULL REFERENCES pr_group (id),
    merged_at timestamptz NOT NULL DEFAULT now()
);

-- A program outside Park, registered by a member, that posts grouping assertions. The link template renders its
-- anchors and evidence as links when they are shown; nothing it posts is stored as a link.
CREATE TABLE bridge (
    id            bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name          text        NOT NULL,
    owner_id      bigint      NOT NULL REFERENCES member (id),
    link_template text        NOT NULL DEFAULT '',
    created_at    timestamptz NOT NULL DEFAULT now()
);

-- A bridge's tokens are its credentials; the bridge, not the token, is the source of what they post.
ALTER TABLE api_token ADD COLUMN bridge_id bigint REFERENCES bridge (id);

-- A claim about grouping, append-only (G13). A co-membership names an anchor and its members, as repository and
-- number; a placement names one pull request and a group, and a negative placement is an exclusion. Seen is when
-- the claim under this anchor was first seen: a repost keeps it.
CREATE TABLE assertion (
    id           bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    source       text        NOT NULL,
    authority    integer     NOT NULL,
    anchor       text,
    kind         text,
    members      jsonb,
    pr_id        bigint      REFERENCES pr_fact (id),
    group_id     bigint      REFERENCES pr_group (id),
    negative     boolean     NOT NULL DEFAULT false,
    title        text,
    evidence     text[]      NOT NULL DEFAULT '{}',
    applied      boolean     NOT NULL DEFAULT true,
    actor_member bigint      REFERENCES member (id),
    token_id     text,
    seen_at      timestamptz NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    retracted_at timestamptz,
    CHECK ((anchor IS NULL) = (pr_id IS NOT NULL))
);

CREATE UNIQUE INDEX assertion_live_anchor ON assertion (source, anchor) WHERE retracted_at IS NULL AND anchor IS NOT NULL;
CREATE INDEX assertion_live_placement ON assertion (pr_id) WHERE retracted_at IS NULL AND pr_id IS NOT NULL;

-- Where an anchor points, fixed when first used (G7); a released anchor points nowhere, and its next claim starts a
-- new group.
CREATE TABLE anchor (
    source   text   NOT NULL,
    key      text   NOT NULL,
    group_id bigint REFERENCES pr_group (id),
    PRIMARY KEY (source, key)
);

-- The group each pull request is in, and the assertion that placed it (G12). A closed pull request keeps its row.
CREATE TABLE membership (
    pr_id        bigint      PRIMARY KEY REFERENCES pr_fact (id),
    group_id     bigint      NOT NULL REFERENCES pr_group (id),
    assertion_id bigint      NOT NULL REFERENCES assertion (id),
    since        timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX membership_group ON membership (group_id);

-- A grouping change Park proposes, applied only when a member accepts it. The key says what makes two suggestions
-- the same, so a dismissed one is not opened again.
CREATE TABLE suggestion (
    id         bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    pr_id      bigint      NOT NULL REFERENCES pr_fact (id),
    group_id   bigint      REFERENCES pr_group (id),
    reason     text        NOT NULL,
    source     text        NOT NULL,
    key        text        NOT NULL,
    state      text        NOT NULL DEFAULT 'open' CHECK (state IN ('open', 'accepted', 'dismissed')),
    created_at timestamptz NOT NULL DEFAULT now(),
    decided_at timestamptz,
    decided_by bigint      REFERENCES member (id),
    UNIQUE (pr_id, key)
);
