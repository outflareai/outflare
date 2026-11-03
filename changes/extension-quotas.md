---
kind: launch
area: api
---
Extensions can set a quota that decides whether a workspace may add more brands, mailboxes, tracking domains or records, or send more email. Refused additions answer `402 quota_exceeded`; refused emails wait and try again. Self-hosted servers have no quota and nothing changes.
