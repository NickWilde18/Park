# Migrations

Park's schema, built by forward-only migrations. `park serve` and `park migrate` apply the pending ones in order, each in its own transaction.

- Name a migration `NNNN_name.sql`: the next unused number and a lower-case name, such as `0001_work_queue.sql`. Any other `.sql` name fails the tests, and so do two migrations with the same number — check the open pull requests before choosing one.
- A merged migration is never edited; change the schema with a new one.
- A binary refuses a database that has a migration it does not know: the database was migrated by a newer Park.
