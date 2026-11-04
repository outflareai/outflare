---
kind: fix
area: workspace
---
Setup health now opens its alerts for workspaces where more than one app or token calls the same deprecated API route. Before, the check failed for those workspaces and no install problems, including broken connections and silent website scripts, reached the status page.
