---
kind: improvement
area: api
---
Inviting someone and changing a member's role take one `role` field: any role's id from `GET /roles`, a starting role like `admin` or one your workspace made. `role_id` is gone, and members and invites return the same `role` they take.
