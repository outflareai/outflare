---
kind: improvement
area: lifecycle
---
Journeys now ignore events that arrive more than a day after they happened, such as imported history: they are kept for reports, but start, move and end no journey, so a backfill never emails people about the past. Automations, signals and lifecycle stages already worked this way.
