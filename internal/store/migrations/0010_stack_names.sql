-- Stack groups: docs/design/03-grouping.md#structural-signals.

-- A group made for a forge's stack is named after the stack's oldest pull request, a name source of its own.
ALTER TABLE pr_group DROP CONSTRAINT pr_group_name_source_check;
ALTER TABLE pr_group ADD CONSTRAINT pr_group_name_source_check
    CHECK (name_source IN ('manual', 'external', 'stack', 'issue', 'branch', 'refs'));
