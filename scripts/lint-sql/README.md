# lint-sql

Flags any SQL query whose string argument is built with `+` concatenation or
`fmt.Sprintf` — the usual way SQL injection creeps in. Every value should reach
the driver through a `$N` placeholder instead.

Run from the module root:

    go run ./scripts/lint-sql ./...

Exit 1 on any unsuppressed finding, 0 otherwise.

## Suppressing a finding

Only for dynamic IDENTIFIERS (schema/table/column names, `ORDER BY` direction)
from a hard-coded allowlist — NEVER user input. Put on the call line or above:

    // sqllint:allow <reason>
