# shellcheck shell=bash
# Helpers for db/migrations files, sourced by scripts/db-migrate.sh (also inside the migrate
# image, which has busybox, not GNU tools) and scripts/db-migrations-between.sh.

# Prints the SQL on stdin with comments (--, # and /* */) and quoted text ('…', "…", `…`)
# blanked out, so keyword checks see only statements. Quotes inside comments and comment
# markers inside quotes are handled; a backslash escapes the next character in quotes.
migrations_code_only() {
  awk '
    {
      out = ""
      n = length($0)
      for (i = 1; i <= n; i++) {
        c = substr($0, i, 1)
        d = substr($0, i, 2)
        if (state == "block") {
          if (d == "*/") { state = ""; i++ }
          continue
        }
        if (state != "") {
          if (c == "\\") { i++; continue }
          if (c == state) state = ""
          continue
        }
        if (d == "/*") { state = "block"; out = out " "; i++; continue }
        if (d == "--" || c == "#") break
        if (c == "\047" || c == "\"" || c == "`") { state = c; out = out " "; continue }
        out = out c
      }
      print out
    }
  '
}

# Succeeds if the migration SQL on stdin contains DROP as a keyword outside comments and quoted
# text: DROP TABLE, DROP INDEX, ALTER TABLE … DROP COLUMN, DROP CONSTRAINT, and so on. Those
# destroy schema or data, so the deploy (bump) PR flags them for the maintainer (design 28,
# "How production migrations run").
migrations_has_drop() {
  migrations_code_only | grep -qiwE 'drop'
}

# Prints the version a migration file name carries, as a decimal: 000012_x.sql → 12.
migrations_version() {
  local name=${1##*/}
  echo $((10#${name%%_*}))
}
