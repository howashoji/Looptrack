---
name: token-report
description: Build the token usage report (PDF) for a coding AI out of the issue management server's figures, save it on this machine and register it in the ledger. Use it on "make the token report", "make the token report (request #N)" or "the token report since the last one", and when the SessionStart summary shows a token report request. The same in every project.
---
<!-- Created by looptrack:init (once you edit it by hand, looptrack issue init leaves it alone) -->

# Building the token report (token-report)

Out of the token usage the issue management server has collected, build a PDF report for a period.
**Take every figure from the server's aggregation as it stands; the AI writes only the prose — what the figures mean and what to do about them.** Keep the PDF on this machine; never put it on the server.
Once it is built, add one row to the ledger (that row is where the next "since the last one" starts, and a request made from the web UI is completed by it).

Design: `docs/server/DESIGN.md` §5-4 of Looptrack, "Report figures and the ledger" and "Report requests".

## Prerequisites

- `looptrack issue config` shows the server and the project (log in first when no token is registered)
- Writing to the ledger needs editor or above (a viewer can only take the figures)
- The PDF comes from `looptrack report pdf` (check that `looptrack version` runs). No separate runtime and no PDF library are needed.
  A font for Japanese text is looked up in this order: `TOKEN_REPORT_FONT` (a path to a TrueType file) → the BIZ UDGothic embedded in the executable → the machine's usual font directories.
  Only TrueType (.ttf, TTC) can be used (a CFF .otf cannot)

Below, `IM="looptrack issue"` (read it as whatever the CLI is called for your AI).

## Steps

### 0. Decide what to build (do this first)

| What you were asked for | How to select the figures |
| -- | -- |
| "make the token report (request #N)", or the summary shows a request | `--request N` (the period, the target and the note come with the request) |
| "the one since the last report", or no period at all | `--since-last` (from the newest data end in the ledger up to now) |
| a period is given, such as "for September" | `--from 2026-09-01 --to 2026-09-30` (a `--to` that is a date alone includes that day; project local time, Asia/Tokyo by default) |

The open requests: `$IM usage requests`. When there are several, build them one at a time, oldest first (never roll them into one).
When no period is given and there is no request, go ahead with `--since-last`. The state of the ledger: `$IM usage ledger list`.

### 1. Take the figures (no judgement)

```bash
OUT="${TOKEN_REPORT_DIR:-$HOME/Documents/トークンレポート}/<the project's slug>"   # $IM config tells you the slug
mkdir -p "$OUT"; STAMP=$(date +%Y%m%d)
$IM usage report --request N --json > "$OUT/トークンレポート_$STAMP.集計.json"   # or --since-last / --from … --to …
$IM usage report --request N                                                   # the same figures, as a table to read
```

- When `total_tokens` is 0 (an empty interval), report that to the user and stop without building a PDF (right after the previous report, for instance). When it answers a request, tell the user and let them decide
- `excluded_conversations` (conversations kept out of the report) and `inconsistent` (intervals where the running total went down) belong in the "Data limitations" section of the prose
- Per case (`by_case`): for each interval one case is decided from the labels of the issue it is attributed to, or from the branch name when there is none (`CASE-101`, for instance; the shape comes from the per-project rule `usage.case_pattern`).
  The total matches the overall figure (`by_label` counts an issue with several labels under each of them, so it does not match). The regular expression is the per-project rule
  `usage.case_pattern` (it appears as `case_pattern`). An empty one means it is unset and everything falls under "(none)". To read the per-case figures alone, take `by_case` from `usage report … --json`;
  for the table, the API's `group=label` (`group: "label"` with the MCP `usage_report`)
- Even when a request's `target` says something like "the label api", take the figures for the whole project (the server does not narrow them by target).
  Aim the prose at that target and point at the rows that match it in the tables (per issue, per case, per label)

### 2. Read the material (judgement)

- The issues at the top: `$IM show <ID>` (what the work was) and `$IM usage show <ID>` (what each stage consumed)
- The project's git history for the period: `git log --since=<from> --until=<to> --oneline`
- Follow whatever the request's note (`request.note`) asks for

### 3. Write the prose JSON (judgement)

Write it to `$OUT/トークンレポート_$STAMP.本文.json`. `looptrack report pdf` builds the tables out of the figures JSON, so **never copy a table into the prose by hand**
(when you quote a number in the prose, take the value from the figures JSON as it stands).

```json
{
  "title": "Token report, September 2026 (<project name>)",
  "subtitle": "<project name> / <request #N | since the last one | a given period>",
  "author": "Written by <AI name> (<date>)",
  "sections": [
    {"heading": "1. Overall summary", "paragraphs": ["…"], "bullets": ["…"]},
    {"auto": "summary"},
    {"auto": "by_issue", "limit": 15},
    {"auto": "by_case"}, {"auto": "by_label"}, {"auto": "by_type"}, {"auto": "by_stage"}, {"auto": "by_client"},
    {"auto": "conversations", "limit": 20},
    {"heading": "2. What the work was", "paragraphs": ["…"]},
    {"heading": "3. Human involvement and how it moved the AI", "table": {"columns": ["Aspect", "Good effect", "Bad effect", "Measured"], "rows": [["…", "…", "…", "…"]]}},
    {"heading": "4. How to spend fewer tokens", "bullets": ["…"]},
    {"heading": "5. Data limitations", "paragraphs": ["…"], "note": "…"}
  ]
}
```

- A section: `heading` (required) with `paragraphs` (an array of paragraphs), `bullets` (a bulleted list), `table` (`rows` with as many columns as `columns`;
  right-align the columns of numbers with `"numeric": [column numbers…]` — a right-aligned column is never wrapped — keep IDs, statuses and the like on one line with `"nowrap": [column numbers…]`, where column numbers start at 0, and put remarks under the table in `"notes": […]`) or `note` (a small remark)
- The tables go where you put `{"auto": "<name>"}`. The names are `summary` (the total, what is unattributed, what is excluded), `by_issue`, `by_case` (per case), `by_label`, `by_type`, `by_stage`,
  `by_client` and `conversations`. `limit` takes the top N (20 by default for `by_issue` and `conversations`), and `heading` changes the heading.
  Write no `auto` at all and every table goes in after the first section
- `looptrack report pdf` stops unless some section's heading contains "Data limitations" (the writing rules below)

A workable set of sections: 1 an overall summary (the total, the breakdown, how many issues, how many conversations) → the tables → 2 what the work was (what the top issues were about) →
3 human involvement and how it moved the AI (the good and the bad) → 4 suggestions (each backed by a measurement) → 5 data limitations.

### 4. Build the PDF (looptrack report pdf)

```bash
looptrack report pdf --report "$OUT/トークンレポート_$STAMP.集計.json" \
  --content "$OUT/トークンレポート_$STAMP.本文.json" --check          # check it first (the sections, the number of tables, the totals)
looptrack report pdf --report "$OUT/トークンレポート_$STAMP.集計.json" \
  --content "$OUT/トークンレポート_$STAMP.本文.json" --out "$OUT/トークンレポート_$STAMP.pdf"
```

- It is saved under `$TOKEN_REPORT_DIR` (`~/Documents/トークンレポート/<slug>/` by default). **Never inside the repository** (customer names and unfixed vulnerabilities can
  end up in the prose). When the user names another place, use it
- Leave the figures JSON and the prose JSON next to the PDF (so it can be rebuilt). Build a revision under a new date and keep the old one
- Open the PDF you built (a page or two, if you can read a PDF with Read) and check that the tables and the prose are not broken

### 5. Register it in the ledger (required, last, cannot be undone)

```bash
$IM usage ledger add "<report name>" --from-report "$OUT/トークンレポート_$STAMP.集計.json" --note "トークンレポート_$STAMP.pdf"
```

- `--from-report` registers the period, the data end, the excluded conversations and the total exactly as they were when you took the figures. **When you took them with `--request N`, the request number is added too and the request is completed**
  (add `--request-id N` when you answered a request but took the figures some other way)
- The report name is unique within the project (`September 2026` or `2026-09-18 request #3`, for instance). The same name gives a 409
- Without the row, the next "since the last one" starts where the previous one did and the periods overlap. When you could not build the PDF, register nothing
- An AI that only has MCP: `usage_report` (`request_id`, `format: json`) → `add_usage_ledger` (with `request_id`)

### 6. Report back

Tell the user: where the PDF is, the period and the data end, the total tokens, the ledger number (`#N`), the request number (and that it is completed, if there was one),
and the gist of the data limitations. When the work belongs to an issue, record it there in a comment.

## Writing rules (strict)

- **Stay with the facts**. Never make a claim such as "human involvement is indispensable" the title or the conclusion. Give the numbers and what was observed, and keep the reading of them
  to something like "the timing and the granularity of the involvement made the difference"
- Set out **both** the good effects and the bad ones (never build it out of one side alone)
- Back every claim with a measurement (tokens, counts, intervals, minutes). Take the numbers from the figures JSON; never mix in a value you rounded by hand
- Always include "Data limitations". At the very least:
  - Tokens are the differences of the running totals in the conversation records, and an interval is attributed to "whichever issue was being worked on at the time" (parallel work cannot be told apart)
  - What is unattributed (a turn that ended after the issue was closed, for instance) and the conversations kept out of the report are not in the total
  - A snapshot that arrived late, stamped before the previous data end, appears in no report at all
  - Cache reads are billed at a lower rate, so the token count is not proportional to the cost (nothing is converted into money)
  - The number of intervals where the running total went down (an inconsistency), if there are any
- Keep customer names, the details of unfixed vulnerabilities and credentials out of the prose (issue titles do appear in the tables, so check with the request's note or the user whether they may be shown)
- What the human typed is not sent to the server by default. Do not quote it in the prose

## Checklist (before you call it done)

- [ ] The selection of the figures (a request / since the last one / a period) matches what was asked. For a request, `request.id` is in the figures JSON
- [ ] When the total was 0, no PDF and no ledger row were made and you reported it
- [ ] The numbers in the prose agree with the figures JSON (the tables were built by looptrack report pdf)
- [ ] Both the good and the bad effects are there, and so is "Data limitations"
- [ ] The PDF is saved outside the repository, with the figures JSON and the prose JSON kept beside it
- [ ] `usage ledger add` registered it in the ledger (for a request, check that it is gone from `usage requests`)
